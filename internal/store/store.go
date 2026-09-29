// Package store keeps trees on disk, one file per session:
// ~/.local/state/decision-tree/<session-id>.json.
//
// Two programs write these files: the MCP server (Claude's calls) and the
// split (Amir's fixes). Every write takes a lock, reads the file fresh,
// changes it, and swaps in the whole new file. So neither program wipes out
// the other's change, and a reader never sees half a file.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"decision-tree/internal/tree"
)

// Store is a folder of tree files.
type Store struct{ Dir string }

// Default is ~/.local/state/decision-tree (or $XDG_STATE_HOME/decision-tree).
func Default() Store {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return Store{filepath.Join(d, "decision-tree")}
	}
	home, _ := os.UserHomeDir()
	return Store{filepath.Join(home, ".local", "state", "decision-tree")}
}

// Session ids are UUIDs. Anything else could point outside the folder.
var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Path is where a session's tree lives.
func (s Store) Path(sessionID string) (string, error) {
	if !validID.MatchString(sessionID) {
		return "", fmt.Errorf("bad session id %q", sessionID)
	}
	return filepath.Join(s.Dir, sessionID+".json"), nil
}

// Load reads a session's tree. If there is none yet, the error wraps
// os.ErrNotExist.
func (s Store) Load(sessionID string) (*tree.Tree, error) {
	path, err := s.Path(sessionID)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return decode(path, b)
}

// Update changes a session's tree under the lock. A missing tree starts
// empty. If fn returns an error, nothing is saved.
func (s Store) Update(sessionID string, fn func(*tree.Tree) error) (*tree.Tree, error) {
	path, err := s.Path(sessionID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return nil, err
	}
	unlock, err := lock(path + ".lock")
	if err != nil {
		return nil, err
	}
	defer unlock()

	var t *tree.Tree
	switch b, err := os.ReadFile(path); {
	case errors.Is(err, os.ErrNotExist):
		t = tree.New(sessionID, time.Now())
	case err != nil:
		return nil, err
	default:
		// A file we cannot read is left alone, never overwritten.
		if t, err = decode(path, b); err != nil {
			return nil, err
		}
	}
	if err := fn(t); err != nil {
		return nil, err
	}
	return t, write(path, t)
}

func decode(path string, b []byte) (*tree.Tree, error) {
	var t tree.Tree
	if err := json.Unmarshal(b, &t); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if t.Version > tree.Version {
		return nil, fmt.Errorf("%s was written by a newer decision-tree (format %d)", path, t.Version)
	}
	if len(t.Nodes) == 0 {
		return nil, fmt.Errorf("%s has no start node", path)
	}
	return &t, nil
}

// write saves to a temp file, then renames it over the real one. A rename
// is all or nothing, so readers see the old file or the new one.
func write(path string, t *tree.Tree) error {
	b, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // a no-op after the rename
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// lock takes an exclusive lock on a lock file, waiting if needed. The lock
// goes away when the program exits, even if it crashes.
func lock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
