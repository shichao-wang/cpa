package prompt

import (
	"strings"
	"testing"
)

// keys decodes a byte stream into the sequence of keys it carries.
func keys(t *testing.T, input string) []Key {
	t.Helper()
	d := newDecoder(strings.NewReader(input))
	var got []Key
	for {
		k, _, err := d.Next()
		if err != nil {
			return got
		}
		got = append(got, k)
	}
}

// typed decodes a stream into the characters it carries, with everything else
// reported as its key name.
func typed(t *testing.T, input string) []string {
	t.Helper()
	d := newDecoder(strings.NewReader(input))
	var got []string
	for {
		k, r, err := d.Next()
		if err != nil {
			return got
		}
		if k == KeyRune {
			got = append(got, string(r))
			continue
		}
		got = append(got, keyName(k))
	}
}

func keyName(k Key) string {
	switch k {
	case KeyEnter:
		return "<enter>"
	case KeyBackspace:
		return "<backspace>"
	case KeyDelete:
		return "<delete>"
	case KeyLeft:
		return "<left>"
	case KeyRight:
		return "<right>"
	case KeyWordLeft:
		return "<word-left>"
	case KeyWordRight:
		return "<word-right>"
	case KeyHome:
		return "<home>"
	case KeyEnd:
		return "<end>"
	case KeyUp:
		return "<up>"
	case KeyDown:
		return "<down>"
	case KeyKillLine:
		return "<kill-line>"
	case KeyKillWord:
		return "<kill-word>"
	case KeyInterrupt:
		return "<interrupt>"
	case KeyUnknown:
		return "<unknown>"
	}
	return "<eof>"
}

// The escape sequences a terminal sends for the arrow keys are exactly what
// canonical mode used to hand the program as literal text.
func TestArrowKeysDecode(t *testing.T) {
	got := strings.Join(typed(t, "\x1b[D\x1b[C\x1b[A\x1b[B"), " ")
	if got != "<left> <right> <up> <down>" {
		t.Errorf("arrows decoded as %q", got)
	}
	// Application-cursor mode sends SS3 instead of CSI.
	got = strings.Join(typed(t, "\x1bOD\x1bOC"), " ")
	if got != "<left> <right>" {
		t.Errorf("SS3 arrows decoded as %q", got)
	}
}

func TestEditingKeysDecode(t *testing.T) {
	got := strings.Join(typed(t, "\x7f\x08\x1b[3~\x1b[H\x1b[F\x1b[1~\x1b[4~"), " ")
	want := "<backspace> <backspace> <delete> <home> <end> <home> <end>"
	if got != want {
		t.Errorf("decoded as %q, want %q", got, want)
	}
}

func TestControlKeysDecode(t *testing.T) {
	got := strings.Join(typed(t, "\x01\x05\x15\x17\x03\x04\x1b[1;5D\x1b[1;5C"), " ")
	want := "<home> <end> <kill-line> <kill-word> <interrupt> <interrupt> <word-left> <word-right>"
	if got != want {
		t.Errorf("decoded as %q, want %q", got, want)
	}
}

func TestEnterVariantsDecode(t *testing.T) {
	if got := strings.Join(typed(t, "\r\n"), " "); got != "<enter> <enter>" {
		t.Errorf("decoded as %q", got)
	}
}

// A bare escape must not swallow the keypress that follows it.
func TestLoneEscapeIsDropped(t *testing.T) {
	got := strings.Join(typed(t, "\x1ba"), " ")
	if got != "<unknown> a" {
		t.Errorf("decoded as %q, want the escape ignored and the letter kept", got)
	}
}

func TestMultibyteRunesDecode(t *testing.T) {
	got := strings.Join(typed(t, "aé中"), "")
	if got != "aé中" {
		t.Errorf("decoded as %q", got)
	}
}

// The bug that started this: an arrow key used to be inserted into the line as
// its raw escape bytes.
func TestArrowKeysMoveTheCursorInsteadOfTyping(t *testing.T) {
	e := NewEditor("")
	for _, r := range "abc" {
		e.Insert(r)
	}
	// Two lefts from the end of "abc" leave the cursor between 'a' and 'b'.
	e.Left()
	e.Left()
	if e.Cursor() != 1 {
		t.Fatalf("cursor = %d, want 1", e.Cursor())
	}
	e.Insert('X')
	if got := e.String(); got != "aXbc" {
		t.Errorf("line = %q, want aXbc", got)
	}
	if e.Cursor() != 2 {
		t.Errorf("cursor = %d, want 2", e.Cursor())
	}
}

func TestEditorPrefillPutsTheCursorAtTheEnd(t *testing.T) {
	e := NewEditor("http://127.0.0.1:8317")
	if e.Cursor() != e.Len() {
		t.Errorf("cursor = %d, want it at the end (%d)", e.Cursor(), e.Len())
	}
	e.Insert('x')
	if got := e.String(); got != "http://127.0.0.1:8317x" {
		t.Errorf("line = %q", got)
	}
}

func TestEditorEdges(t *testing.T) {
	e := NewEditor("abc")
	e.Home()
	e.Backspace() // nothing before the cursor
	if got := e.String(); got != "abc" {
		t.Errorf("backspace at the start changed the line to %q", got)
	}
	e.End()
	e.Delete() // nothing under the cursor
	if got := e.String(); got != "abc" {
		t.Errorf("delete at the end changed the line to %q", got)
	}
	e.Left()
	e.Delete()
	if got := e.String(); got != "ab" {
		t.Errorf("delete removed the wrong character: %q", got)
	}
}

func TestEditorKillLineAndWord(t *testing.T) {
	e := NewEditor("keep drop tail")
	e.End()
	e.KillWord()
	if got := e.String(); got != "keep drop " {
		t.Errorf("kill-word gave %q", got)
	}
	e.KillLine()
	if got := e.String(); got != "" {
		t.Errorf("kill-line gave %q", got)
	}

	// kill-line only erases what is behind the cursor.
	e = NewEditor("left right")
	e.WordLeft()
	e.KillLine()
	if got := e.String(); got != "right" {
		t.Errorf("kill-line mid-line gave %q, want right", got)
	}
}

func TestEditorWordMovement(t *testing.T) {
	e := NewEditor("one two three")
	e.Home()
	e.WordRight()
	if e.Cursor() != 4 {
		t.Errorf("cursor = %d, want 4", e.Cursor())
	}
	e.WordLeft()
	if e.Cursor() != 0 {
		t.Errorf("cursor = %d, want 0", e.Cursor())
	}
}

func TestListNavigationStopsAtTheEnds(t *testing.T) {
	l := NewList([]string{"a", "b", "c"}, 10)
	l.Up()
	if l.Index() != 0 {
		t.Errorf("up at the top moved to %d", l.Index())
	}
	l.Bottom()
	l.Down()
	if l.Index() != 2 {
		t.Errorf("down at the bottom moved to %d", l.Index())
	}
	if l.Selected() != "c" {
		t.Errorf("selected %q", l.Selected())
	}
}

// A list longer than the window has to scroll, and it should move by as
// little as possible so the options do not jump under the reader.
func TestListScrollsToKeepTheSelectionVisible(t *testing.T) {
	items := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}
	l := NewList(items, 3)
	if l.Offset() != 0 {
		t.Errorf("offset = %d, want 0", l.Offset())
	}
	l.SetIndex(2)
	if l.Offset() != 0 {
		t.Errorf("offset = %d; it scrolled before it had to", l.Offset())
	}
	l.SetIndex(3)
	if l.Offset() != 1 {
		t.Errorf("offset = %d, want 1", l.Offset())
	}
	l.Bottom()
	if l.Offset() != 7 {
		t.Errorf("offset = %d, want 7", l.Offset())
	}
	visible := l.Visible()
	if len(visible) != 3 {
		t.Fatalf("visible = %d rows, want 3", len(visible))
	}
	last := visible[len(visible)-1]
	if last.Index != 9 || !last.Selected {
		t.Errorf("the last row is %+v, want index 9 selected", last)
	}
}

func TestListWithFewerItemsThanRows(t *testing.T) {
	l := NewList([]string{"a", "b"}, 10)
	if got := len(l.Visible()); got != 2 {
		t.Errorf("visible = %d rows, want 2", got)
	}
	l.Bottom()
	if l.Offset() != 0 {
		t.Errorf("offset = %d, want 0", l.Offset())
	}
}
