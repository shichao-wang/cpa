package prompt

import (
	"errors"
	"os"
	"testing"
)

// The caller relies on this to choose the flag-driven path, so a pipe has to
// be recognised as "no terminal" rather than opening a prompt that can never
// be answered.
func TestNewRefusesAPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	pr, err := New(r, os.Stdout)
	if !errors.Is(err, ErrNotATerminal) {
		t.Fatalf("New(pipe) = %v, want ErrNotATerminal", err)
	}
	if pr != nil {
		t.Error("a prompter was returned for a pipe")
	}
}
