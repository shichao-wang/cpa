package prompt

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// ErrNotATerminal is returned by New when there is no terminal to drive. The
// caller is expected to take a non-interactive path instead of prompting.
var ErrNotATerminal = errors.New("not a terminal")

// ErrInterrupted is returned when the user abandons a prompt with ctrl-c.
var ErrInterrupted = errors.New("interrupted")

// ErrEOF is returned when the terminal goes away mid-prompt.
var ErrEOF = errors.New("input ended")

// ErrBack is returned when the user asks the caller to return to the previous
// question by pressing escape.
var ErrBack = errors.New("back")

const (
	// how many list options are shown at once, before the list scrolls
	listHeight = 10
	// the marker drawn against the highlighted option
	marker = "❯ "
)

// Prompter owns a terminal for the length of a form. New puts it into raw
// mode, so every Prompter must be Closed; the form helpers do that for the
// caller.
type Prompter struct {
	fd     int
	dec    *decoder
	out    io.Writer
	state  *term.State
	closed bool
	// drawn is how many lines were painted below the current label, so the
	// next redraw knows how far up to travel.
	drawn   int
	indent  string
	answers []answerMark
	// sectionsSinceAnswer counts section headings printed after the most recent
	// confirmed answer, so Back can erase through them when returning farther.
	sectionsSinceAnswer int
}

type answerMark struct {
	sectionsBefore int
}

// New takes the terminal into raw mode. It returns ErrNotATerminal when in is
// not a terminal, which is how a caller learns to fall back to flags.
func New(in *os.File, out io.Writer) (*Prompter, error) {
	fd := int(in.Fd())
	if !term.IsTerminal(fd) {
		return nil, ErrNotATerminal
	}
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, err
	}
	return &Prompter{fd: fd, dec: newTerminalDecoder(in, fd), out: out, state: state}, nil
}

// Close restores the terminal. It is safe to call more than once.
func (p *Prompter) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	return term.Restore(p.fd, p.state)
}

// SetIndent indents subsequent prompts and confirmed answers by n spaces.
func (p *Prompter) SetIndent(n int) {
	if n < 0 {
		n = 0
	}
	p.indent = strings.Repeat(" ", n)
}

// Section prints a heading between questions. Back removes section headings
// printed after the answer it rewinds.
func (p *Prompter) Section(label string) {
	if p.drawn > 0 {
		p.rewind()
	}
	fmt.Fprintf(p.out, "\r\x1b[K%s\x1b[1m%s\x1b[0m\x1b[J\r\n", p.indent, clip(label, max(p.width()-len(p.indent)-1, 1)))
	p.drawn = 0
	p.sectionsSinceAnswer++
}

// Back erases the most recently confirmed answer and anything after it. It is
// intended to be called after Input or Choose returns ErrBack.
func (p *Prompter) Back() {
	if len(p.answers) == 0 {
		return
	}
	mark := p.answers[len(p.answers)-1]
	p.answers = p.answers[:len(p.answers)-1]
	rows := p.sectionsSinceAnswer + 1
	fmt.Fprintf(p.out, "\x1b[%dA\r\x1b[K\x1b[J", rows)
	p.drawn = 0
	p.sectionsSinceAnswer = mark.sectionsBefore
}

// Input asks for a single line. A non-empty def is offered as editable text
// rather than as a hint, so it can be corrected instead of retyped. A
// validate that returns an error keeps the prompt open and shows the message
// beneath it.
//
// The returned value is trimmed; a prompt has no use for trailing spaces.
func (p *Prompter) Input(label, def string, validate func(string) error) (string, error) {
	defer func() { p.drawn = 0 }()

	e := NewEditor(def)
	var errText string
	for {
		p.drawInput(label, e, errText)
		k, r, err := p.dec.Next()
		if err != nil {
			p.abort()
			return "", ErrEOF
		}
		if k == KeyInterrupt {
			p.abort()
			return "", ErrInterrupted
		}
		if k == KeyBack {
			p.eraseActive()
			return "", ErrBack
		}
		if k == KeyEnter {
			value := strings.TrimSpace(e.String())
			if validate != nil {
				if verr := validate(value); verr != nil {
					errText = verr.Error()
					continue
				}
			}
			p.endInput(label, value)
			return value, nil
		}
		errText = "" // any other key acknowledges the message
		e.Apply(k, r)
	}
}

// Choose asks for one of options and returns its index, starting on the first
// one. Up and down move the highlight; enter accepts it.
func (p *Prompter) Choose(label string, options []string) (int, error) {
	return p.ChooseDefault(label, options, 0)
}

// ChooseDefault is Choose with the highlight starting somewhere other than the
// first option, which is how an already-configured value stays selected.
func (p *Prompter) ChooseDefault(label string, options []string, def int) (int, error) {
	defer func() { p.drawn = 0 }()

	if len(options) == 0 {
		return -1, fmt.Errorf("nothing to choose from for %q", label)
	}
	l := NewList(options, p.listHeight())
	l.SetIndex(def)
	for {
		p.drawList(label, l)
		k, _, err := p.dec.Next()
		if err != nil {
			p.abort()
			return -1, ErrEOF
		}
		switch k {
		case KeyEnter:
			p.endChoose(label, l.Selected())
			return l.Index(), nil
		case KeyInterrupt:
			p.abort()
			return -1, ErrInterrupted
		case KeyBack:
			p.eraseActive()
			return -1, ErrBack
		}
		l.Apply(k)
	}
}

// Confirm asks a yes/no question. The affirmative option is first, so it is
// what a bare enter selects.
func (p *Prompter) Confirm(label, affirmative, negative string) (bool, error) {
	i, err := p.Choose(label, []string{affirmative, negative})
	if err != nil {
		return false, err
	}
	return i == 0, nil
}

// listHeight keeps the list inside a short terminal.
func (p *Prompter) listHeight() int {
	// Rows held back: one for the label above the list, one for whatever
	// follows it, and two for the scroll indicators.
	if _, rows, err := term.GetSize(p.fd); err == nil {
		if h := rows - 6; h < listHeight {
			return max(h, 1)
		}
	}
	return listHeight
}

func (p *Prompter) drawInput(label string, e *Editor, errText string) {
	p.rewind()
	label = clip(label, max((p.width()-len(p.indent))/2, 8))
	// Text wider than the terminal would wrap onto a second row, which the
	// next redraw cannot erase and the cursor arithmetic cannot follow. Show a
	// window that keeps the editing position on screen instead: it scrolls
	// sideways as the line grows. Runes are counted, not display cells, so a
	// line of wide characters lands a little off — acceptable for the ids and
	// URLs typed at these prompts.
	avail := p.width() - len(p.indent) - 3 - len([]rune(label))
	if avail < 1 {
		avail = 1
	}
	text := []rune(e.String())
	start, end := lineWindow(text, e.Cursor(), avail)
	fmt.Fprintf(p.out, "\r\x1b[K%s\x1b[1m?\x1b[0m %s %s", p.indent, label, string(text[start:end]))
	if back := end - e.Cursor(); back > 0 {
		fmt.Fprintf(p.out, "\x1b[%dD", back)
	}
	if errText == "" {
		if p.drawn > 0 {
			fmt.Fprint(p.out, "\x1b[J")
		}
		return
	}
	// Leave the cursor on the message row: rewind() then moves up exactly
	// one row, including when Escape removes this unanswered question.
	fmt.Fprintf(p.out, "\n\r\x1b[K\x1b[31m%s\x1b[0m\x1b[J", clip(errText, p.width()-1))
	p.drawn = 1
}

func (p *Prompter) endInput(label, value string) {
	p.rewind()
	fmt.Fprintf(p.out, "\r\x1b[K%s\x1b[1m✓\x1b[0m %s\x1b[J\r\n", p.indent, clip(label+" "+value, max(p.width()-len(p.indent)-3, 1)))
	p.drawn = 0
	p.confirmAnswer()
}

func (p *Prompter) drawList(label string, l *List) {
	p.rewind()
	width := p.width()
	fmt.Fprintf(p.out, "\r\x1b[K%s\x1b[1m?\x1b[0m %s", p.indent, clip(label, width-len(p.indent)-2))
	n := 0
	// A window shorter than the list says so on its own first and last rows,
	// dimmed; otherwise the list looks complete at the bottom edge.
	if above, _ := l.Hidden(); above > 0 {
		fmt.Fprintf(p.out, "\n\r\x1b[K%s\x1b[2m  ↑ %d more\x1b[0m", p.indent, above)
		n++
	}
	for _, it := range l.Visible() {
		mark := "  "
		if it.Selected {
			mark = marker
		}
		fmt.Fprintf(p.out, "\n\r\x1b[K%s%s%s", p.indent, mark, clip(it.Text, width-len(p.indent)-2))
		n++
	}
	if _, below := l.Hidden(); below > 0 {
		fmt.Fprintf(p.out, "\n\r\x1b[K%s\x1b[2m  ↓ %d more\x1b[0m", p.indent, below)
		n++
	}
	// Scrolling can drop an indicator and so paint fewer rows than last time;
	// erase whatever is below the list, or the surplus row stays on screen.
	fmt.Fprint(p.out, "\x1b[J")
	p.drawn = n
}

// width is the usable column count. Long options are clipped to it so a
// wrapped row cannot put the redraw a line out.
func (p *Prompter) width() int {
	if cols, _, err := term.GetSize(p.fd); err == nil && cols > 8 {
		return cols
	}
	return 80
}

// lineWindow picks the runes of an input line to draw in avail columns, so
// the whole line is shown when it fits and otherwise a window that keeps the
// cursor in view. The cursor sits at end when it was past the window.
func lineWindow(text []rune, cur, avail int) (start, end int) {
	if avail < 1 {
		avail = 1
	}
	if cur >= avail {
		start = cur - avail + 1
	}
	if start > len(text)-avail {
		start = len(text) - avail
	}
	if start < 0 {
		start = 0
	}
	return start, min(start+avail, len(text))
}

// clip shortens s to at most n columns, marking the cut with an ellipsis. It
// counts runes rather than display cells, so a line of wide characters lands a
// little short of n; the ids shown at these prompts are ASCII in practice.
func clip(s string, n int) string {
	r := []rune(s)
	if n < 1 || len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func (p *Prompter) endChoose(label, value string) {
	p.rewind()
	fmt.Fprintf(p.out, "\r\x1b[K%s\x1b[1m✓\x1b[0m %s\x1b[J\r\n", p.indent, clip(label+" "+value, max(p.width()-len(p.indent)-3, 1)))
	p.drawn = 0
	p.confirmAnswer()
}

func (p *Prompter) confirmAnswer() {
	p.answers = append(p.answers, answerMark{sectionsBefore: p.sectionsSinceAnswer})
	p.sectionsSinceAnswer = 0
}

// eraseActive removes the current unanswered prompt without leaving a cancel
// marker or advancing the cursor, so a caller can redraw it in place.
func (p *Prompter) eraseActive() {
	p.rewind()
	fmt.Fprint(p.out, "\r\x1b[K\x1b[J")
	p.drawn = 0
}

// abort reports the interrupt on the line the prompt occupied, so the
// transcript reads as a cancelled question rather than a broken one.
func (p *Prompter) abort() {
	p.rewind()
	fmt.Fprint(p.out, "\r\x1b[K^C\x1b[J\r\n")
	p.drawn = 0
}

// rewind puts the cursor at the start of the label line.
func (p *Prompter) rewind() {
	if p.drawn > 0 {
		fmt.Fprintf(p.out, "\x1b[%dA", p.drawn)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
