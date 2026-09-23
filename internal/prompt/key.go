// Package prompt drives a small interactive form on a terminal: single-line
// input with real line editing, and single-choice lists. It exists because
// reading a terminal in canonical mode hands the program arrow keys as literal
// escape bytes, and because the standard library has no raw-mode line editor.
//
// The package is deliberately split in two. key.go decodes bytes into keys,
// editor.go and list.go are pure state machines over those keys, and prompt.go
// owns the terminal: raw mode, drawing, and restoring the terminal on the way
// out. Only the last part needs a terminal, so the editing rules are tested by
// feeding them keys directly.
//
// The only dependency is golang.org/x/term, which the Go team maintains.
// Prompt libraries built on lipgloss/termenv were rejected: linking them makes
// every invocation of the binary — including the ones that never prompt —
// query the terminal for its background colour, which costs seconds on a
// terminal that does not answer.
package prompt

import (
	"io"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// Key is one decoded keypress.
type Key int

const (
	KeyUnknown Key = iota
	KeyRune        // a printable character, returned alongside Key
	KeyEnter
	KeyBackspace
	KeyDelete
	KeyLeft
	KeyRight
	KeyWordLeft
	KeyWordRight
	KeyHome
	KeyEnd
	KeyUp
	KeyDown
	KeyTab
	KeyKillLine // ctrl-u / ctrl-k: erase back to the start of the line
	KeyKillWord // ctrl-w: erase the word behind the cursor
	KeyInterrupt
	KeyBack
	KeyEOF
)

// decoder turns a byte stream into keys. It reads one byte at a time so the
// terminal's fd readiness check can distinguish a bare ESC from an arrow
// sequence without a buffered read consuming the next key.
type decoder struct {
	in      io.Reader
	pending []byte
	pollFD  int
}

func newDecoder(r io.Reader) *decoder {
	return &decoder{in: r, pollFD: -1}
}

func newTerminalDecoder(r io.Reader, fd int) *decoder {
	return &decoder{in: r, pollFD: fd}
}

func (d *decoder) readByte() (byte, error) {
	if len(d.pending) > 0 {
		b := d.pending[0]
		d.pending = d.pending[1:]
		return b, nil
	}
	var buf [1]byte
	n, err := io.ReadFull(d.in, buf[:])
	if n > 0 {
		return buf[0], nil
	}
	return 0, err
}

func (d *decoder) unreadByte(b byte) {
	d.pending = append(d.pending, 0)
	copy(d.pending[1:], d.pending[:len(d.pending)-1])
	d.pending[0] = b
}

// Next returns the next key. The second result carries the character for
// KeyRune. Sequences that mean nothing here come back as KeyUnknown rather
// than as an error, so an unrecognised key is ignored instead of aborting
// the prompt.
func (d *decoder) Next() (Key, rune, error) {
	b, err := d.readByte()
	if err != nil {
		return KeyEOF, 0, err
	}

	switch b {
	case 0x1b:
		return d.escape()
	case '\r', '\n':
		return KeyEnter, 0, nil
	case 0x7f, 0x08:
		// 0x7f is what a modern terminal sends for backspace; 0x08 is what
		// the tty layer produces when it is left in cooked mode.
		return KeyBackspace, 0, nil
	case 0x03:
		return KeyInterrupt, 0, nil
	case 0x04:
		// ctrl-d: the usual "I am done with this input" key.
		return KeyInterrupt, 0, nil
	case 0x01:
		return KeyHome, 0, nil
	case 0x05:
		return KeyEnd, 0, nil
	case 0x02:
		return KeyLeft, 0, nil
	case 0x06:
		return KeyRight, 0, nil
	case 0x0b, 0x15:
		return KeyKillLine, 0, nil
	case 0x17:
		return KeyKillWord, 0, nil
	case 0x09:
		return KeyTab, 0, nil
	}

	if b < 0x20 {
		return KeyUnknown, 0, nil // any other control character
	}
	if b < utf8.RuneSelf {
		return KeyRune, rune(b), nil
	}
	// A multi-byte character: collect enough bytes to decode one rune. If a
	// malformed sequence includes bytes after its replacement rune, put those
	// bytes back so they are not lost as unrelated keypresses.
	seq := []byte{b}
	for !utf8.FullRune(seq) {
		next, err := d.readByte()
		if err != nil {
			break
		}
		seq = append(seq, next)
	}
	r, size := utf8.DecodeRune(seq)
	if size < len(seq) {
		d.pending = append(seq[size:], d.pending...)
	}
	return KeyRune, r, nil
}

const escapePollTimeout = 40 * time.Millisecond

// escape distinguishes a standalone ESC from a terminal escape sequence. The
// terminal path polls briefly before reading another byte; stream-based tests
// use the immediate reader and preserve any ordinary key following ESC.
func (d *decoder) escape() (Key, rune, error) {
	if d.pollFD >= 0 {
		ready, err := pollReadable(d.pollFD, escapePollTimeout)
		if err != nil {
			return KeyUnknown, 0, err
		}
		if !ready {
			return KeyBack, 0, nil
		}
	}

	b, err := d.readByte()
	if err != nil {
		if err == io.EOF {
			return KeyBack, 0, nil
		}
		return KeyUnknown, 0, err
	}
	// SS3 ('O') sequences share the final-byte letters below, so both
	// introducers lead into the same switch. Anything else follows a standalone
	// ESC and remains available as the next key.
	if b != '[' && b != 'O' {
		d.unreadByte(b)
		return KeyBack, 0, nil
	}

	n, err := d.readByte()
	if err != nil {
		return KeyUnknown, 0, nil
	}
	switch n {
	case 'A':
		return KeyUp, 0, nil
	case 'B':
		return KeyDown, 0, nil
	case 'C':
		return KeyRight, 0, nil
	case 'D':
		return KeyLeft, 0, nil
	case 'H':
		return KeyHome, 0, nil
	case 'F':
		return KeyEnd, 0, nil
	}
	if n < '0' || n > '9' {
		return KeyUnknown, 0, nil
	}

	// A parameterised sequence: ESC [ <digits and ;> <final>. xterm sends
	// ctrl-arrow as ESC [ 1;5 C and Delete as ESC [ 3 ~.
	params := []byte{n}
	for {
		c, err := d.readByte()
		if err != nil {
			return KeyUnknown, 0, nil
		}
		if (c >= '0' && c <= '9') || c == ';' {
			params = append(params, c)
			continue
		}
		switch c {
		case '~':
			switch string(params) {
			case "3":
				return KeyDelete, 0, nil
			case "1", "7":
				return KeyHome, 0, nil
			case "4", "8":
				return KeyEnd, 0, nil
			}
		case 'C':
			return KeyWordRight, 0, nil
		case 'D':
			return KeyWordLeft, 0, nil
		}
		return KeyUnknown, 0, nil
	}
}

func pollReadable(fd int, timeout time.Duration) (bool, error) {
	millis := int(timeout / time.Millisecond)
	for {
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(fds, millis)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		return n > 0, nil
	}
}
