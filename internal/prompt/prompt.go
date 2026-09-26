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
	err    io.Writer
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

// SetErr points the prompter's notes at a writer other than stderr, which is
// where they go by default. The interactive form is drawn on the terminal while
// its notes are not, so a caller that wants to put a note somewhere else — a
// buffer under test — says so here.
func (p *Prompter) SetErr(w io.Writer) { p.err = w }

// note writes one of the prompter's own remarks: not part of the question, and
// never on the question's stream.
func (p *Prompter) note(format string, args ...interface{}) {
	w := p.err
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintf(w, format, args...)
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
	return p.choose(label, options, def, false)
}

// ChooseSearchDefault filters options as characters are typed, but returns
// their original index so callers can still map the answer to its value.
func (p *Prompter) ChooseSearchDefault(label string, options []string, def int) (int, error) {
	return p.choose(label, options, def, true)
}

// ChooseMultiSearch asks for up to max of options, from a list filtered as
// characters are typed. Tab picks and unpicks the highlighted option; the
// answer is every pick, as original indices, in the order they were picked —
// an order the caller may well mean something by. Enter accepts what is
// picked, including nothing. picked is what starts out picked, in its own
// order.
func (p *Prompter) ChooseMultiSearch(label string, options []string, picked []int, max int) ([]int, error) {
	defer func() { p.drawn = 0 }()

	if len(options) == 0 {
		return nil, fmt.Errorf("nothing to choose from for %q", label)
	}
	// Both the picks and the highlight are held as original indices, and the
	// list is rebuilt from them on each pass. Searching only ever changes
	// which slice of the options is on screen, so what is picked survives
	// typing in the search box; the list's own positions are derived rather
	// than carried, because the search box keeps renumbering them.
	order := append([]int(nil), picked...)
	cursor := 0
	if len(order) > 0 {
		cursor = order[0]
	}
	query := ""
	notice := ""
	for {
		indices := searchIndices(options, query)
		l := NewList(labelsOf(options, indices), p.listHeight())
		l.SetChecked(positionsOf(indices, order))
		if pos := positionsOf(indices, []int{cursor}); len(pos) > 0 {
			l.SetIndex(pos[0])
		}
		p.drawMultiList(label, l, query, pickedNames(options, order), max, notice)
		k, r, err := p.dec.Next()
		if err != nil {
			p.abort()
			return nil, ErrEOF
		}
		switch k {
		case KeyEnter:
			p.endMulti(label, order, options)
			return order, nil
		case KeyInterrupt:
			p.abort()
			return nil, ErrInterrupted
		case KeyBack:
			p.eraseActive()
			return nil, ErrBack
		case KeyTab:
			if l.Len() == 0 {
				continue
			}
			// Tab only ever toggles the highlighted row, and leaves the
			// highlight where it is: a second Tab has to undo the first, which
			// is the move a reader makes the moment they pick the wrong row.
			// Moving on to the next pick is the down key's job.
			next, ok := togglePick(order, indices[l.Index()], max)
			if !ok {
				notice = fmt.Sprintf("! %d already picked; untick one with Tab first", max)
				continue
			}
			notice = ""
			order = next
			cursor = indices[l.Index()]
		case KeyRune, KeyBackspace:
			notice = ""
			if k == KeyBackspace && query == "" {
				continue
			}
			if k == KeyRune {
				query += string(r)
			} else {
				query = string([]rune(query)[:len([]rune(query))-1])
			}
			continue
		default:
			notice = ""
			if l.Apply(k) {
				cursor = indices[l.Index()]
			}
		}
	}
}

func labelsOf(options []string, indices []int) []string {
	labels := make([]string, len(indices))
	for i, orig := range indices {
		labels[i] = options[orig]
	}
	return labels
}

// pickedNames is every pick, in pick order, whether or not the search box
// still shows it. A pick the query has filtered away is dropped by the list —
// there is no row to tick — so this is the only line left saying it is still
// chosen, and it must not go quiet while the reader is typing.
func pickedNames(options []string, order []int) []string {
	names := make([]string, 0, len(order))
	for _, orig := range order {
		names = append(names, options[orig])
	}
	return names
}

// searchIndices is the original indices of the options a query leaves, in
// order. An empty query leaves every option, which is what clearing the
// search box has to restore.
func searchIndices(options []string, query string) []int {
	var indices []int
	for i, option := range options {
		if query == "" || strings.Contains(strings.ToLower(option), strings.ToLower(query)) {
			indices = append(indices, i)
		}
	}
	return indices
}

// positionsOf maps original indices onto where they sit in the filtered view,
// dropping the ones the filter has taken off screen. A highlight that the
// search box has just hidden is therefore not restored when the query clears,
// which is the honest answer: the reader cannot see what they are pointing at.
func positionsOf(indices, order []int) []int {
	position := make(map[int]int, len(indices))
	for pos, orig := range indices {
		position[orig] = pos
	}
	var positions []int
	for _, orig := range order {
		if pos, ok := position[orig]; ok {
			positions = append(positions, pos)
		}
	}
	return positions
}

// togglePick adds an original index to the picks, or removes it, keeping the
// order the picks were made in. It reports false when the pick would exceed
// max, which the caller says out loud rather than silently dropping.
func togglePick(order []int, orig, max int) ([]int, bool) {
	for i, o := range order {
		if o == orig {
			next := make([]int, 0, len(order)-1)
			next = append(next, order[:i]...)
			return append(next, order[i+1:]...), true
		}
	}
	if len(order) >= max {
		return order, false
	}
	next := make([]int, 0, len(order)+1)
	next = append(next, order...)
	return append(next, orig), true
}

func (p *Prompter) choose(label string, options []string, def int, searchable bool) (int, error) {
	defer func() { p.drawn = 0 }()

	if len(options) == 0 {
		return -1, fmt.Errorf("nothing to choose from for %q", label)
	}
	l := NewList(options, p.listHeight())
	l.SetIndex(def)
	selected := l.Index()
	indices := make([]int, len(options))
	for i := range indices {
		indices[i] = i
	}
	var query []rune
	for {
		if searchable {
			p.drawSearchList(label, l, string(query))
		} else {
			p.drawList(label, l)
		}
		k, r, err := p.dec.Next()
		if err != nil {
			p.abort()
			return -1, ErrEOF
		}
		switch k {
		case KeyEnter:
			if l.Len() == 0 {
				continue
			}
			picked := indices[l.Index()]
			p.endChoose(label, options[picked])
			return picked, nil
		case KeyInterrupt:
			p.abort()
			return -1, ErrInterrupted
		case KeyBack:
			p.eraseActive()
			return -1, ErrBack
		case KeyRune, KeyBackspace:
			if searchable {
				if l.Len() > 0 {
					selected = indices[l.Index()]
				}
				if k == KeyRune {
					query = append(query, r)
				} else if len(query) > 0 {
					query = query[:len(query)-1]
				} else {
					continue
				}
				l, indices = searchOptions(options, string(query), selected, p.listHeight())
				continue
			}
		}
		l.Apply(k)
	}
}

func searchOptions(options []string, query string, selected, height int) (*List, []int) {
	var labels []string
	var indices []int
	selectedIndex := 0
	query = strings.ToLower(query)
	for i, option := range options {
		if !strings.Contains(strings.ToLower(option), query) {
			continue
		}
		if i == selected {
			selectedIndex = len(labels)
		}
		labels = append(labels, option)
		indices = append(indices, i)
	}
	l := NewList(labels, height)
	l.SetIndex(selectedIndex)
	return l, indices
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
	p.drawListWithSearch(label, l, "", false)
}

func (p *Prompter) drawSearchList(label string, l *List, query string) {
	p.drawListWithSearch(label, l, query, true)
}

// drawMultiList paints a pickable list: the same rows as a single-choice one,
// with a tick column, a line naming what is picked and in what order, and —
// when the reader tries to pick past the limit — why that did nothing.
//
// picked is every pick by name, not the rows the list happens to be showing:
// the search box can filter a picked row away, and the line has to keep saying
// it is picked. It is drawn above the notice so the count a refusal complains
// about is on screen beside the complaint.
func (p *Prompter) drawMultiList(label string, l *List, query string, picked []string, limit int, notice string) {
	p.rewind()
	width := p.width()
	indent := p.indent
	avail := max(width-len(indent)-2, 1)

	hint := fmt.Sprintf(" [Tab to pick, up to %d]", limit)
	search := ""
	if query != "" {
		search = " [search: " + query + "]"
	}
	// The hint is what a reader needs first; a long search term is dropped to
	// fit rather than allowed to wrap the heading, which would put the
	// row-based redraw a line out.
	label = clip(label+hint, avail)
	if remaining := avail - searchColumns(label); remaining > 1 {
		label += clip(search, remaining-1)
	}
	fmt.Fprintf(p.out, "\r\x1b[K%s\x1b[1m?\x1b[0m %s", indent, label)
	n := 0

	if len(picked) > 0 {
		line := fmt.Sprintf("picked %d/%d: %s", len(picked), limit, strings.Join(picked, ", "))
		fmt.Fprintf(p.out, "\n\r\x1b[K%s\x1b[2m%s\x1b[0m", indent, clip(line, avail))
		n++
	}
	if notice != "" {
		fmt.Fprintf(p.out, "\n\r\x1b[K%s\x1b[31m%s\x1b[0m", indent, clip(notice, avail))
		n++
	}
	if l.Len() == 0 {
		fmt.Fprintf(p.out, "\n\r\x1b[K%s%s", indent, clip("No matching entries", avail))
		n++
	}
	// A window shorter than the list says so on its own first and last rows,
	// dimmed; otherwise the list looks complete at the bottom edge.
	if above, _ := l.Hidden(); above > 0 {
		fmt.Fprintf(p.out, "\n\r\x1b[K%s\x1b[2m  ↑ %d more\x1b[0m", indent, above)
		n++
	}
	for _, it := range l.Visible() {
		mark, tick := "  ", "  "
		if it.Selected {
			mark = marker
		}
		if it.Checked {
			tick = "✓ "
		} else {
			tick = "○ "
		}
		fmt.Fprintf(p.out, "\n\r\x1b[K%s%s%s%s", indent, mark, tick, clip(it.Text, width-len(indent)-4))
		n++
	}
	if _, below := l.Hidden(); below > 0 {
		fmt.Fprintf(p.out, "\n\r\x1b[K%s\x1b[2m  ↓ %d more\x1b[0m", indent, below)
		n++
	}
	// Scrolling can drop an indicator and so paint fewer rows than last time;
	// erase whatever is below the list, or the surplus row stays on screen.
	fmt.Fprint(p.out, "\x1b[J")
	p.drawn = n
}

func (p *Prompter) drawListWithSearch(label string, l *List, query string, searchable bool) {
	p.rewind()
	width := p.width()
	if searchable {
		search := " [type to search]"
		if query != "" {
			search = " [search: " + query + "]"
		}
		avail := max(width-len(p.indent)-2, 1)
		// Conservatively count non-ASCII runes as two columns so a wide
		// search term cannot wrap the heading and break row-based redraws.
		if searchColumns(search) > avail {
			prefix := "…"
			if avail == 1 {
				prefix = "."
			}
			r := []rune(search)
			for len(r) > 0 && searchColumns(prefix+string(r)) > avail {
				r = r[1:]
			}
			search = prefix + string(r)
		}
		if remaining := avail - searchColumns(search); remaining > 1 {
			// clip uses a wide ellipsis, so leave it one extra column.
			label = clip(label, remaining-1) + search
		} else {
			label = search
		}
	} else {
		label = clip(label, max(width-len(p.indent)-2, 1))
	}
	fmt.Fprintf(p.out, "\r\x1b[K%s\x1b[1m?\x1b[0m %s", p.indent, label)
	n := 0
	if searchable && l.Len() == 0 {
		fmt.Fprintf(p.out, "\n\r\x1b[K%s  %s", p.indent, clip("No matching entries", max(width-len(p.indent)-2, 1)))
		n++
	}
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

func searchColumns(s string) int {
	columns := 0
	for _, r := range s {
		columns++
		if r > 127 {
			columns++
		}
	}
	return columns
}

func (p *Prompter) endChoose(label, value string) {
	p.rewind()
	fmt.Fprintf(p.out, "\r\x1b[K%s\x1b[1m✓\x1b[0m %s\x1b[J\r\n", p.indent, clip(label+" "+value, max(p.width()-len(p.indent)-3, 1)))
	p.drawn = 0
	p.confirmAnswer()
}

// endMulti confirms a pickable list. What is reported is the picked models in
// pick order, which is the order the caller will write them down in.
func (p *Prompter) endMulti(label string, order []int, options []string) {
	names := make([]string, 0, len(order))
	for _, orig := range order {
		names = append(names, options[orig])
	}
	value := "none"
	if len(names) > 0 {
		value = strings.Join(names, ", ")
	}
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
