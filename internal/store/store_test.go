package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"decision-tree/internal/tree"
)

const id = "8533417c-1b9d-4c3c-a771-f0df5b76dae2"

func addDecision(q string) func(*tree.Tree) error {
	return func(t *tree.Tree) error {
		_, err := t.Record(tree.Call{Question: q, Options: []string{"a", "b"}, At: time.Now()})
		return err
	}
}

func TestUpdateThenLoad(t *testing.T) {
	s := Store{t.TempDir()}
	if _, err := s.Load(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load before any update = %v, want not-exist", err)
	}
	if _, err := s.Update(id, addDecision("Q1")); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != id || len(got.Decisions) != 1 || got.Decisions[0].Question != "Q1" {
		t.Fatalf("loaded %+v", got)
	}
}

func TestFailedUpdateSavesNothing(t *testing.T) {
	s := Store{t.TempDir()}
	boom := errors.New("boom")
	if _, err := s.Update(id, func(*tree.Tree) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Load(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("a failed update created a file")
	}

	s.Update(id, addDecision("Q1"))
	path, _ := s.Path(id)
	before, _ := os.ReadFile(path)
	s.Update(id, func(t *tree.Tree) error {
		addDecision("Q2")(t)
		return boom
	})
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("a failed update changed the file")
	}
}

func TestBadSessionID(t *testing.T) {
	s := Store{t.TempDir()}
	for _, bad := range []string{"", "../x", "a/b", "a b", strings.Repeat("x", 129)} {
		if _, err := s.Update(bad, addDecision("Q")); err == nil {
			t.Errorf("session id %q was accepted", bad)
		}
	}
}

func TestBrokenFileIsLeftAlone(t *testing.T) {
	s := Store{t.TempDir()}
	path, _ := s.Path(id)
	for _, body := range []string{`{"version": 1, "nodes": [`, `{"version": 99, "nodes": [{"id": "n0"}]}`, `{"version": 1}`} {
		os.WriteFile(path, []byte(body), 0o644)
		if _, err := s.Update(id, addDecision("Q")); err == nil {
			t.Errorf("update over %q worked, want an error", body)
		}
		if got, _ := os.ReadFile(path); string(got) != body {
			t.Errorf("file was overwritten: %q", got)
		}
	}
}

func TestManyWritersAtOnce(t *testing.T) {
	s := Store{t.TempDir()}
	const n = 30
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Update(id, addDecision(fmt.Sprint("Q", i))); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	got, err := s.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Decisions) != n {
		t.Fatalf("%d decisions saved, want %d: some writes were lost", len(got.Decisions), n)
	}
	// Only the tree file and its lock file are left: no temp files.
	files, _ := filepath.Glob(filepath.Join(s.Dir, "*"))
	if len(files) != 2 {
		t.Fatalf("files left behind: %v", files)
	}
}
