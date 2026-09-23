package prompt

// Editor is one line of input: the characters typed so far and where the
// cursor sits among them. It holds no terminal state, so the editing rules
// are tested by calling the methods directly.
type Editor struct {
	buf []rune
	pos int
}

// NewEditor returns an editor holding an optional initial value, with the
// cursor at the end — where a person expects it when a prompt is prefilled.
func NewEditor(initial string) *Editor {
	e := &Editor{}
	e.Set(initial)
	return e
}

// Set replaces the contents and puts the cursor at the end.
func (e *Editor) Set(s string) {
	e.buf = []rune(s)
	e.pos = len(e.buf)
}

// String is the line as it would be submitted.
func (e *Editor) String() string { return string(e.buf) }

// Cursor is the cursor's offset in runes from the start of the line.
func (e *Editor) Cursor() int { return e.pos }

// Len is the number of runes on the line.
func (e *Editor) Len() int { return len(e.buf) }

// Insert puts a character at the cursor.
func (e *Editor) Insert(r rune) {
	e.buf = append(e.buf, 0)
	copy(e.buf[e.pos+1:], e.buf[e.pos:])
	e.buf[e.pos] = r
	e.pos++
}

// Backspace removes the character before the cursor.
func (e *Editor) Backspace() {
	if e.pos == 0 {
		return
	}
	e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
	e.pos--
}

// Delete removes the character under the cursor.
func (e *Editor) Delete() {
	if e.pos >= len(e.buf) {
		return
	}
	e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
}

// Left moves the cursor one character towards the start.
func (e *Editor) Left() {
	if e.pos > 0 {
		e.pos--
	}
}

// Right moves the cursor one character towards the end.
func (e *Editor) Right() {
	if e.pos < len(e.buf) {
		e.pos++
	}
}

// Home moves the cursor to the start of the line.
func (e *Editor) Home() { e.pos = 0 }

// End moves the cursor to the end of the line.
func (e *Editor) End() { e.pos = len(e.buf) }

// KillLine removes everything before the cursor. This is what ctrl-u does in
// a shell, and it is the fastest way to abandon a half-typed answer.
func (e *Editor) KillLine() {
	e.buf = append([]rune{}, e.buf[e.pos:]...)
	e.pos = 0
}

// KillWord removes the word before the cursor, along with the spaces
// separating it from whatever precedes it.
func (e *Editor) KillWord() {
	i := e.pos
	for i > 0 && isSpace(e.buf[i-1]) {
		i--
	}
	for i > 0 && !isSpace(e.buf[i-1]) {
		i--
	}
	e.buf = append(e.buf[:i], e.buf[e.pos:]...)
	e.pos = i
}

// WordLeft moves the cursor to the start of the word before it.
func (e *Editor) WordLeft() {
	i := e.pos
	for i > 0 && isSpace(e.buf[i-1]) {
		i--
	}
	for i > 0 && !isSpace(e.buf[i-1]) {
		i--
	}
	e.pos = i
}

// WordRight moves the cursor past the word after it.
func (e *Editor) WordRight() {
	i := e.pos
	for i < len(e.buf) && !isSpace(e.buf[i]) {
		i++
	}
	for i < len(e.buf) && isSpace(e.buf[i]) {
		i++
	}
	e.pos = i
}

// Apply routes one key to the editor, reporting whether the key was consumed.
// KeyEnter and KeyInterrupt are the caller's business, not the editor's.
func (e *Editor) Apply(k Key, r rune) bool {
	switch k {
	case KeyRune:
		e.Insert(r)
	case KeyBackspace:
		e.Backspace()
	case KeyDelete:
		e.Delete()
	case KeyLeft:
		e.Left()
	case KeyRight:
		e.Right()
	case KeyWordLeft:
		e.WordLeft()
	case KeyWordRight:
		e.WordRight()
	case KeyHome:
		e.Home()
	case KeyEnd:
		e.End()
	case KeyKillLine:
		e.KillLine()
	case KeyKillWord:
		e.KillWord()
	default:
		return false
	}
	return true
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' }
