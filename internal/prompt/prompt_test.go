package prompt

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
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
	case KeyBack:
		return "<back>"
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

// A bare escape means back, without swallowing the keypress that follows it.
func TestLoneEscapeReturnsBackAndPreservesFollowingKey(t *testing.T) {
	got := strings.Join(typed(t, "\x1ba"), " ")
	if got != "<back> a" {
		t.Errorf("decoded as %q, want back followed by the letter", got)
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

func TestListReportsOptionsHiddenByTheWindow(t *testing.T) {
	l := NewList([]string{"a", "b", "c", "d", "e", "f"}, 3)

	if above, below := l.Hidden(); above != 0 || below != 3 {
		t.Errorf("at the top: %d above, %d below; want 0, 3", above, below)
	}
	l.SetIndex(4)
	if above, below := l.Hidden(); above != 2 || below != 1 {
		t.Errorf("mid-list: %d above, %d below; want 2, 1", above, below)
	}
	l.Bottom()
	if above, below := l.Hidden(); above != 3 || below != 0 {
		t.Errorf("at the bottom: %d above, %d below; want 3, 0", above, below)
	}
}

func TestListHidesNothingWhenItAllFits(t *testing.T) {
	l := NewList([]string{"a", "b", "c"}, 3)
	l.Bottom()
	if above, below := l.Hidden(); above != 0 || below != 0 {
		t.Errorf("hidden = %d above, %d below; want none", above, below)
	}
}

func TestClipShortensOptionsWiderThanTheLine(t *testing.T) {
	if got := clip("abcdef", 4); got != "abc…" {
		t.Errorf("clip = %q, want %q", got, "abc…")
	}
	if got := clip("abc", 4); got != "abc" {
		t.Errorf("clip shortened a line that fits: %q", got)
	}
	if got := clip("abcdef", 6); got != "abcdef" {
		t.Errorf("clip shortened an exact fit: %q", got)
	}
	if got := clip("abcdef", 0); got != "abcdef" {
		t.Errorf("clip with no room = %q, want it left alone", got)
	}
}

func TestLineWindowKeepsTheCursorVisible(t *testing.T) {
	text := []rune("http://127.0.0.1:18999/v1")
	cur := len(text) // typing at the end
	start, end := lineWindow(text, cur, 10)
	if end != len(text) {
		t.Errorf("end = %d, want the whole line %d", end, len(text))
	}
	if got := end - cur; got != 0 {
		t.Errorf("cursor lands %d columns past the end", got)
	}
	if start != len(text)-10 {
		t.Errorf("start = %d, want %d so the tail shows", start, len(text)-10)
	}
}

func TestLineWindowShowsAWholeShortLine(t *testing.T) {
	text := []rune("ab")
	if start, end := lineWindow(text, 1, 10); start != 0 || end != 2 {
		t.Errorf("window = [%d,%d), want the whole line", start, end)
	}
}

func TestLineWindowClampsAtTheEnds(t *testing.T) {
	text := []rune("abcdefghij")
	// cursor at the far left of a long line: the window must not go negative
	if start, end := lineWindow(text, 0, 4); start != 0 || end != 4 {
		t.Errorf("cursor at 0: window = [%d,%d), want [0,4)", start, end)
	}
	// empty line, and no room at all
	if start, end := lineWindow(nil, 0, 0); start != 0 || end != 0 {
		t.Errorf("empty: window = [%d,%d), want [0,0)", start, end)
	}
}

func TestStandaloneEscapeUsesTerminalFDTimeout(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err := w.Write([]byte("\x1b")); err != nil {
		t.Fatal(err)
	}

	d := newTerminalDecoder(r, int(r.Fd()))
	start := time.Now()
	k, _, err := d.Next()
	if err != nil {
		t.Fatalf("Next() error = %v", err)
	}
	if k != KeyBack {
		t.Fatalf("Next() key = %v, want KeyBack", k)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("standalone ESC took %v to decode; want a brief poll timeout", elapsed)
	}
}

func TestInputAndChooseReturnErrBackOnEscape(t *testing.T) {
	var out bytes.Buffer
	p := &Prompter{dec: newDecoder(strings.NewReader("\x1b")), out: &out}
	if _, err := p.Input("Name", "", nil); !errors.Is(err, ErrBack) {
		t.Errorf("Input error = %v, want ErrBack", err)
	}
	if strings.Contains(out.String(), "^C") {
		t.Error("escape was rendered as an interrupt")
	}

	out.Reset()
	p = &Prompter{dec: newDecoder(strings.NewReader("\x1b")), out: &out}
	if _, err := p.Choose("Model", []string{"a", "b"}); !errors.Is(err, ErrBack) {
		t.Errorf("Choose error = %v, want ErrBack", err)
	}
	if strings.Contains(out.String(), "^C") {
		t.Error("escape was rendered as an interrupt")
	}
}

func TestPromptMarkersIndentAndSection(t *testing.T) {
	var out bytes.Buffer
	p := &Prompter{out: &out}
	p.SetIndent(2)
	p.endInput("Name", "sample")
	p.Section("Model settings")
	p.drawInput("Model", NewEditor(""), "")

	got := out.String()
	for _, want := range []string{
		"  \x1b[1m✓\x1b[0m Name sample",
		"  \x1b[1mModel settings\x1b[0m",
		"  \x1b[1m?\x1b[0m Model",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rendered output does not contain %q: %q", want, got)
		}
	}
}

func TestBackErasesPriorAnswerAndInterveningSections(t *testing.T) {
	var out bytes.Buffer
	p := &Prompter{out: &out}
	p.endInput("Name", "sample")
	p.Section("Model settings")
	p.drawInput("Model", NewEditor(""), "")
	p.eraseActive()
	p.Back()

	if !strings.Contains(out.String(), "\x1b[2A\r\x1b[K\x1b[J") {
		t.Errorf("Back did not erase through the intervening section: %q", out.String())
	}
}

func TestConfirmedAnswersStayOnOneRow(t *testing.T) {
	var out bytes.Buffer
	p := &Prompter{out: &out}
	long := strings.Repeat("x", 200)
	p.endInput("Gateway base URL", long)
	p.endChoose("opus", long)
	p.Back()
	for _, row := range strings.Split(out.String(), "\r\n")[:2] {
		if strings.Contains(row, long) || !strings.Contains(row, "…") {
			t.Errorf("long answer was not clipped to one row: %q", row)
		}
	}
	if !strings.HasSuffix(out.String(), "\x1b[1A\r\x1b[K\x1b[J") {
		t.Errorf("Back should rewind exactly one confirmed row: %q", out.String())
	}
}

func TestEscapeAfterValidationErrorRewindsOneMessageRow(t *testing.T) {
	var out bytes.Buffer
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("bad\r\x1b"))}
	_, err := p.Input("Gateway base URL", "", func(string) error { return errors.New("invalid address") })
	if !errors.Is(err, ErrBack) {
		t.Fatalf("Input error = %v, want ErrBack", err)
	}
	if strings.Contains(out.String(), "\x1b8") || !strings.HasSuffix(out.String(), "\x1b[1A\r\x1b[K\x1b[J") {
		t.Errorf("validation message left the cursor on the wrong row: %q", out.String())
	}
}

// draw renders a list the way the prompter would, for tests that need to see
// what actually reaches the terminal.
func draw(l *List) string {
	var buf bytes.Buffer
	p := &Prompter{out: &buf}
	p.drawList("opus", l)
	return buf.String()
}

func TestDrawListAnnouncesHiddenOptions(t *testing.T) {
	items := []string{"a", "b", "c", "d", "e", "f"}

	top := draw(NewList(items, 3))
	if !strings.Contains(top, "↓ 3 more") {
		t.Errorf("the top of the list does not say what is below it: %q", top)
	}
	if strings.Contains(top, "↑") {
		t.Errorf("the top of the list claims something is above it: %q", top)
	}

	l := NewList(items, 3)
	l.Bottom()
	bottom := draw(l)
	if !strings.Contains(bottom, "↑ 3 more") {
		t.Errorf("the bottom of the list does not say what is above it: %q", bottom)
	}
	if strings.Contains(bottom, "↓") {
		t.Errorf("the bottom of the list claims something is below it: %q", bottom)
	}
}

func TestDrawListErasesRowsAShorterRepaintDoesNotCover(t *testing.T) {
	// Scrolling to the end drops the "↓ more" row, so the paint is one row
	// shorter than the last one and has to erase the leftover.
	if out := draw(NewList([]string{"a", "b", "c"}, 3)); !strings.HasSuffix(out, "\x1b[J") {
		t.Errorf("the list does not erase below itself, so a surplus row would be left on screen: %q", out)
	}
}

func TestDrawListClipsRowsTooWideForTheTerminal(t *testing.T) {
	long := strings.Repeat("x", 200)
	out := draw(NewList([]string{long}, 1))
	if strings.Contains(out, long) {
		t.Errorf("a %d-column option was drawn whole; it would wrap and put the redraw out", len(long))
	}
	if !strings.Contains(out, "…") {
		t.Errorf("the clipped option is not marked as clipped: %q", out)
	}
}

func TestSearchOptionsPreservesOriginalIndicesAndHighlight(t *testing.T) {
	options := []string{"leave unset", "claude-opus", "DeepSeek V4", "deepseek-r1"}
	l, indices := searchOptions(options, "DEEP", 3, 1)
	if got := l.Selected(); got != "deepseek-r1" || l.Offset() != 1 {
		t.Errorf("selected = %q, offset = %d; want deepseek-r1 at 1", got, l.Offset())
	}
	if len(indices) != 2 || indices[0] != 2 || indices[1] != 3 {
		t.Fatalf("filtered indices = %v, want [2 3]", indices)
	}
	l, indices = searchOptions(options, "deep", 1, 2)
	if l.Index() != 0 || indices[l.Index()] != 2 {
		t.Errorf("missing highlight should move to first result: index %d, indices %v", l.Index(), indices)
	}
	l, indices = searchOptions(options, "", indices[l.Index()], 2)
	if l.Len() != len(options) || indices[l.Index()] != 2 {
		t.Errorf("clearing query should restore all and keep selection: length %d, selected %d", l.Len(), indices[l.Index()])
	}
}

func TestChooseSearchDefaultFiltersAndReturnsOriginalIndex(t *testing.T) {
	options := []string{"leave unset", "claude-opus", "DeepSeek V4", "deepseek-r1"}
	var out bytes.Buffer
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("DEEP\x1b[B\r"))}
	got, err := p.ChooseSearchDefault("opus", options, 1)
	if err != nil || got != 3 {
		t.Fatalf("chosen index = %d, error = %v; want 3", got, err)
	}
	if !strings.Contains(out.String(), "[search: DEEP]") || !strings.Contains(out.String(), "deepseek-r1") {
		t.Errorf("search display or selected model missing: %q", out.String())
	}
}

func TestChooseSearchDefaultBackspaceAndNoMatches(t *testing.T) {
	var out bytes.Buffer
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("中x\ré\x7f\x7f\r"))}
	got, err := p.ChooseSearchDefault("opus", []string{"leave unset", "中文-model"}, 1)
	if err != nil || got != 1 {
		t.Fatalf("chosen index = %d, error = %v; want 1", got, err)
	}
	if !strings.Contains(out.String(), "No matching models") || !strings.Contains(out.String(), "[search: 中]") {
		t.Errorf("missing search or empty-state feedback: %q", out.String())
	}
	if !strings.Contains(out.String(), "\x1b[J") {
		t.Errorf("shorter filtered list left old rows: %q", out.String())
	}
}

func TestChooseSearchDefaultRestoresSelectionAfterNoMatches(t *testing.T) {
	var out bytes.Buffer
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("z\x7f\r"))}
	got, err := p.ChooseSearchDefault("opus", []string{"leave unset", "saved-model"}, 1)
	if err != nil || got != 1 {
		t.Fatalf("restored choice = %d, error = %v; want existing model at 1", got, err)
	}
}

func TestChooseSearchDefaultEscapeAndPlainChoose(t *testing.T) {
	var out bytes.Buffer
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("deep\x1b"))}
	if _, err := p.ChooseSearchDefault("opus", []string{"deepseek"}, 0); !errors.Is(err, ErrBack) {
		t.Errorf("search escape = %v, want ErrBack", err)
	}
	out.Reset()
	p = &Prompter{out: &out, dec: newDecoder(strings.NewReader("x\r"))}
	if got, err := p.ChooseDefault("confirm", []string{"yes", "no"}, 1); err != nil || got != 1 {
		t.Errorf("plain choose index = %d, error = %v; want 1", got, err)
	}
	if strings.Contains(out.String(), "search:") {
		t.Errorf("plain choice unexpectedly became searchable: %q", out.String())
	}
}

func TestDrawSearchListClipsLongQuery(t *testing.T) {
	for _, query := range []string{strings.Repeat("x", 100), strings.Repeat("中", 40)} {
		var out bytes.Buffer
		p := &Prompter{out: &out}
		p.drawSearchList(strings.Repeat("slot", 30), NewList(nil, 2), query)
		if !strings.Contains(out.String(), "No matching models") || !strings.Contains(out.String(), "…") || p.drawn != 1 {
			t.Errorf("empty search rendering = %q, drawn = %d", out.String(), p.drawn)
		}
		if !strings.HasSuffix(out.String(), "\x1b[J") {
			t.Errorf("empty search did not clear old rows: %q", out.String())
		}
		row := strings.SplitN(out.String(), "\n", 2)[0]
		row = strings.ReplaceAll(row, "\r\x1b[K\x1b[1m?\x1b[0m ", "")
		if columns := searchColumns(row) + 2; columns > p.width() {
			t.Errorf("search heading occupies %d columns (width %d): %q", columns, p.width(), row)
		}
	}
}

func TestChooseMultiSearchPicksInTabOrder(t *testing.T) {
	var out bytes.Buffer
	// Tab on the first row, down, Tab on the second, enter.
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("\t\x1b[B\t\r"))}
	got, err := p.ChooseMultiSearch("fallbacks", []string{"alpha", "beta", "gamma"}, nil, 3)
	if err != nil || !reflect.DeepEqual(got, []int{0, 1}) {
		t.Fatalf("picks = %v, error = %v; want [0 1]", got, err)
	}
	if !strings.Contains(out.String(), "picked 2/3: alpha, beta") {
		t.Errorf("the picked line does not name what is picked, in order: %q", out.String())
	}
}

func TestChooseMultiSearchSearchKeepsPicksAndReturnsOriginalIndices(t *testing.T) {
	var out bytes.Buffer
	// Tab on "alpha", type "gam" to search, Tab on the remaining row, enter.
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("\tgam\t\r"))}
	got, err := p.ChooseMultiSearch("fallbacks", []string{"alpha", "beta", "gamma"}, nil, 3)
	if err != nil || !reflect.DeepEqual(got, []int{0, 2}) {
		t.Fatalf("picks = %v, error = %v; want the original indices [0 2]", got, err)
	}
	// The line keeps naming the pick the search has filtered away: its row is
	// gone, so nothing else on screen says it is still chosen.
	if !strings.Contains(out.String(), "picked 2/3: alpha, gamma") {
		t.Errorf("the pick made before the search was lost: %q", out.String())
	}
}

func TestChooseMultiSearchStartsFromTheExistingPicks(t *testing.T) {
	var out bytes.Buffer
	// The existing pick is shown; a bare enter keeps it, and a second tab
	// on it removes it.
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("\r"))}
	got, err := p.ChooseMultiSearch("fallbacks", []string{"alpha", "beta"}, []int{1}, 3)
	if err != nil || !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("picks = %v, error = %v; want the passed-in [1]", got, err)
	}
}

func TestChooseMultiSearchReportsTheLimitInTheHeading(t *testing.T) {
	var out bytes.Buffer
	// Pick one of two past a limit of one, then try a second: the second is
	// refused and said out loud, and the answer stays the first pick.
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("\t\x1b[B\t\r"))}
	got, err := p.ChooseMultiSearch("fallbacks", []string{"alpha", "beta"}, nil, 1)
	if err != nil || !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("picks = %v, error = %v; want just the first pick", got, err)
	}
	if !strings.Contains(out.String(), "up to 1") || !strings.Contains(out.String(), "already picked") {
		t.Errorf("the heading and the refusal are not shown: %q", out.String())
	}
}

// Tab toggles the row under the highlight and leaves the highlight on it, so
// a second Tab undoes the first without the reader hunting for the row again.
func TestChooseMultiSearchTabTwiceUndoesThePick(t *testing.T) {
	var out bytes.Buffer
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("\t\t\r"))}
	got, err := p.ChooseMultiSearch("fallbacks", []string{"alpha", "beta"}, nil, 3)
	if err != nil || len(got) != 0 {
		t.Fatalf("picks = %v, error = %v; want none after picking and unpicking", got, err)
	}
	if !strings.Contains(out.String(), "picked 1/3: alpha") {
		t.Errorf("the pick was never shown, so the undo cannot be seen either: %q", out.String())
	}
}

// Enter with a search that matches nothing accepts what is already picked:
// the search box is a way to reach a row, not a way to lose an answer.
func TestChooseMultiSearchEnterDuringNoMatchesKeepsPicks(t *testing.T) {
	var out bytes.Buffer
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("\tzzz\r"))}
	got, err := p.ChooseMultiSearch("fallbacks", []string{"alpha", "beta"}, nil, 3)
	if err != nil || !reflect.DeepEqual(got, []int{0}) {
		t.Fatalf("picks = %v, error = %v; want the pick made before the search", got, err)
	}
	if !strings.Contains(out.String(), "No matching models") {
		t.Errorf("an empty result is not said out loud: %q", out.String())
	}
}

func TestChooseMultiSearchEscapeAndInterrupt(t *testing.T) {
	var out bytes.Buffer
	p := &Prompter{out: &out, dec: newDecoder(strings.NewReader("\x1b"))}
	if _, err := p.ChooseMultiSearch("fallbacks", []string{"alpha"}, nil, 3); !errors.Is(err, ErrBack) {
		t.Errorf("escape = %v, want ErrBack", err)
	}
	out.Reset()
	p = &Prompter{out: &out, dec: newDecoder(strings.NewReader("\x03"))}
	if _, err := p.ChooseMultiSearch("fallbacks", []string{"alpha"}, nil, 3); !errors.Is(err, ErrInterrupted) {
		t.Errorf("ctrl-c = %v, want ErrInterrupted", err)
	}
}
