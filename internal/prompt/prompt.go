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
	drawn int
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
	return &Prompter{fd: fd, dec: newDecoder(in), out: out, state: state}, nil
}

// Close restores the terminal. It is safe to call more than once.
func (p *Prompter) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	return term.Restore(p.fd, p.state)
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
	if _, rows, err := term.GetSize(p.fd); err == nil {
		if h := rows - 4; h < listHeight {
			return max(h, 1)
		}
	}
	return listHeight
}

func (p *Prompter) drawInput(label string, e *Editor, errText string) {
	p.rewind()
	fmt.Fprintf(p.out, "\r\x1b[K\x1b[1m?\x1b[0m %s %s", label, e.String())
	// The cursor is at the end of the text; walk it back to the editing
	// position. Runes are counted, not display cells, so a line of wide
	// characters lands a little off — acceptable for the ids and URLs typed
	// at these prompts.
	if back := e.Len() - e.Cursor(); back > 0 {
		fmt.Fprintf(p.out, "\x1b[%dD", back)
	}
	if errText == "" {
		if p.drawn > 0 {
			fmt.Fprint(p.out, "\x1b[J")
		}
		return
	}
	// Save the cursor, print the message under the line, come back.
	fmt.Fprint(p.out, "\x1b7")
	fmt.Fprintf(p.out, "\n\r\x1b[K\x1b[31m%s\x1b[0m\x1b[J", errText)
	fmt.Fprint(p.out, "\x1b8")
	p.drawn = 1
}

func (p *Prompter) endInput(label, value string) {
	p.rewind()
	fmt.Fprintf(p.out, "\r\x1b[K\x1b[1m?\x1b[0m %s %s\x1b[J\n", label, value)
	p.drawn = 0
}

func (p *Prompter) drawList(label string, l *List) {
	p.rewind()
	fmt.Fprintf(p.out, "\r\x1b[K\x1b[1m?\x1b[0m %s", label)
	n := 0
	for _, it := range l.Visible() {
		mark := "  "
		if it.Selected {
			mark = marker
		}
		fmt.Fprintf(p.out, "\n\r\x1b[K%s%s", mark, it.Text)
		n++
	}
	p.drawn = n
}

func (p *Prompter) endChoose(label, value string) {
	p.rewind()
	fmt.Fprintf(p.out, "\r\x1b[K\x1b[1m?\x1b[0m %s %s\x1b[J\n", label, value)
	p.drawn = 0
}

// abort reports the interrupt on the line the prompt occupied, so the
// transcript reads as a cancelled question rather than a broken one.
func (p *Prompter) abort() {
	p.rewind()
	fmt.Fprint(p.out, "\r\x1b[K^C\x1b[J\n")
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
