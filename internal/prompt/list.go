package prompt

// List is a list of options: the options, which one is highlighted, and which
// slice of them is currently on screen. A list whose picks are set also
// remembers them, for the tick column. Like Editor it holds no terminal state,
// so navigation and scrolling are tested directly.
type List struct {
	items []string
	index int
	// height is how many options fit on screen at once. When the list is
	// longer the window scrolls, and offset is where it currently starts.
	height int
	offset int
	// checked is the picked options, as positions in the list. The list only
	// carries them for the renderer: what a pick means, how many are allowed,
	// and the order they were made in all belong to the caller, which holds
	// them as original indices that survive the search box renumbering rows.
	checked []int
}

// NewList returns a list over items with the first one highlighted. A height
// below one is treated as one.
func NewList(items []string, height int) *List {
	if height < 1 {
		height = 1
	}
	return &List{items: items, height: height}
}

// SetChecked picks the options at these positions. Positions outside the list
// are dropped: a search shortens the list under the caller's feet, and a
// caller that maps its picks through the filtered indices can hand over one
// that is no longer on screen.
func (l *List) SetChecked(positions []int) {
	var checked []int
	for _, i := range positions {
		if i >= 0 && i < len(l.items) {
			checked = append(checked, i)
		}
	}
	l.checked = checked
}

func (l *List) isChecked(i int) bool {
	for _, c := range l.checked {
		if c == i {
			return true
		}
	}
	return false
}

// Len is the number of options.
func (l *List) Len() int { return len(l.items) }

// Index is the highlighted option, counted from zero.
func (l *List) Index() int { return l.index }

// Selected is the highlighted option's text.
func (l *List) Selected() string {
	if len(l.items) == 0 {
		return ""
	}
	return l.items[l.index]
}

// Offset is the first option currently on screen.
func (l *List) Offset() int { return l.offset }

// Visible returns the options on screen, paired with their real index.
func (l *List) Visible() []Item {
	end := l.offset + l.height
	if end > len(l.items) {
		end = len(l.items)
	}
	out := make([]Item, 0, end-l.offset)
	for i := l.offset; i < end; i++ {
		out = append(out, Item{Index: i, Text: l.items[i], Selected: i == l.index, Checked: l.isChecked(i)})
	}
	return out
}

// Hidden reports how many options sit off screen above and below the window.
// The renderer draws a scroll indicator from it, so a short window over a long
// list does not read as the whole list.
func (l *List) Hidden() (above, below int) {
	above = l.offset
	below = len(l.items) - (l.offset + l.height)
	if below < 0 {
		below = 0
	}
	return above, below
}

// Item is one visible option.
type Item struct {
	Index    int
	Text     string
	Selected bool
	Checked  bool
}

// SetIndex highlights a specific option and scrolls it into view.
func (l *List) SetIndex(i int) {
	if i < 0 || i >= len(l.items) {
		return
	}
	l.index = i
	l.scroll()
}

// Up moves the highlight towards the top, stopping there.
func (l *List) Up() { l.SetIndex(l.index - 1) }

// Down moves the highlight towards the bottom, stopping there.
func (l *List) Down() { l.SetIndex(l.index + 1) }

// Top highlights the first option.
func (l *List) Top() { l.SetIndex(0) }

// Bottom highlights the last option.
func (l *List) Bottom() { l.SetIndex(len(l.items) - 1) }

// Apply routes one key to the list, reporting whether the key was consumed.
func (l *List) Apply(k Key) bool {
	switch k {
	case KeyUp:
		l.Up()
	case KeyDown:
		l.Down()
	case KeyHome:
		l.Top()
	case KeyEnd:
		l.Bottom()
	default:
		return false
	}
	return true
}

// scroll keeps the highlight inside the visible window, moving the window by
// as little as possible so the list does not jump under the reader.
func (l *List) scroll() {
	if l.index < l.offset {
		l.offset = l.index
	}
	if l.index >= l.offset+l.height {
		l.offset = l.index - l.height + 1
	}
	if max := len(l.items) - l.height; l.offset > max {
		l.offset = max
	}
	if l.offset < 0 {
		l.offset = 0
	}
}
