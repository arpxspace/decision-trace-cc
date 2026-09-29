package tmux

import (
	"os/exec"
	"testing"
)

func TestNewestPane(t *testing.T) {
	cases := map[string]string{
		"1790692370 %23\n":                  "%23",
		"100 %1\n300 %7\n200 %3\n":          "%7",
		"":                                  "",
		"junk\n100 notapane\nabc %4\n50 %9": "%9",
	}
	for in, want := range cases {
		if got := newestPane(in); got != want {
			t.Errorf("newestPane(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNoServerIsNotAnError(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// A private socket nobody started: the same as tmux not running.
	pane, err := Tmux{Socket: "decision-tree-test-nobody"}.ActivePane()
	if pane != "" || err != nil {
		t.Fatalf("ActivePane = %q, %v; want \"\", nil", pane, err)
	}
}
