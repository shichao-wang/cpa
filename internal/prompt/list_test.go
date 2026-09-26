package prompt

import (
	"reflect"
	"testing"
)

func TestListTracksCheckedRows(t *testing.T) {
	l := NewList([]string{"a", "b", "c"}, 3)
	if checked, _ := checkedRows(l); len(checked) != 0 {
		t.Fatalf("a fresh list starts unpicked, got %v", checked)
	}
	l.SetChecked([]int{2, 0})
	// The list reports checks by row, not in pick order — the order is the
	// caller's to keep — so what it carries is the set, for the tick column.
	checked, text := checkedRows(l)
	if !reflect.DeepEqual(checked, []int{0, 2}) || !reflect.DeepEqual(text, []string{"a", "c"}) {
		t.Errorf("checked rows = %v (%v), want [0 2] ([a c])", checked, text)
	}
}

// checkedRows is what the renderer draws: the ticked rows in list order, with
// their text, paired position by position.
func checkedRows(l *List) ([]int, []string) {
	var indices []int
	var text []string
	for _, it := range l.Visible() {
		if it.Checked {
			indices = append(indices, it.Index)
			text = append(text, it.Text)
		}
	}
	return indices, text
}

// A search shortens the list under the caller's feet; a position that is no
// longer on screen must not be drawn as picked or kept in the answer.
func TestListDropsChecksOutsideTheList(t *testing.T) {
	l := NewList([]string{"a", "b"}, 2)
	l.SetChecked([]int{0, 5, -1, 1})
	if checked, _ := checkedRows(l); !reflect.DeepEqual(checked, []int{0, 1}) {
		t.Errorf("checked rows = %v, want the in-range picks only", checked)
	}
}

func TestTogglePickAddsRemovesAndRefusesPastTheLimit(t *testing.T) {
	order, ok := togglePick(nil, 3, 2)
	if !ok || !reflect.DeepEqual(order, []int{3}) {
		t.Fatalf("first pick = %v, ok=%v", order, ok)
	}
	order, ok = togglePick(order, 1, 2)
	if !ok || !reflect.DeepEqual(order, []int{3, 1}) {
		t.Fatalf("second pick = %v, ok=%v", order, ok)
	}
	if order, ok := togglePick(order, 7, 2); ok || !reflect.DeepEqual(order, []int{3, 1}) {
		t.Errorf("a third pick past the limit must be refused: %v, ok=%v", order, ok)
	}
	order, ok = togglePick(order, 3, 2)
	if !ok || !reflect.DeepEqual(order, []int{1}) {
		t.Errorf("unpicking keeps the remaining order: %v, ok=%v", order, ok)
	}
	// The freed slot is usable again.
	if order, ok := togglePick(order, 7, 2); !ok || !reflect.DeepEqual(order, []int{1, 7}) {
		t.Errorf("a freed slot must be reusable: %v, ok=%v", order, ok)
	}
}

// togglePick must not write into the caller's slice: the caller keeps the
// order it passed while a redraw may still be reading it.
func TestTogglePickDoesNotAliasTheInput(t *testing.T) {
	order := []int{1, 2, 3}
	next, _ := togglePick(order, 4, 9)
	next[0] = 99
	if !reflect.DeepEqual(order, []int{1, 2, 3}) {
		t.Errorf("the input order was mutated through the result: %v", order)
	}
	freed, _ := togglePick(order, 2, 9)
	freed[0] = 99
	if !reflect.DeepEqual(order, []int{1, 2, 3}) {
		t.Errorf("removing a pick mutated the input: %v", order)
	}
}
