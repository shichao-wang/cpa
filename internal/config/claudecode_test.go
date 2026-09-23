package config

import (
	"strings"
	"testing"
)

// Every slot cpa asks about has a Claude Code model to name it by, and the id
// filed under a slot belongs to that slot: a mismatch would label a row with
// the wrong model, which is exactly the kind of quiet wrongness the mapping is
// there to remove.
func TestClaudeCodeDefaultsCoverEverySlot(t *testing.T) {
	for _, slot := range Slots {
		id := ClaudeCodeDefaults[slot]
		if id == "" {
			t.Errorf("slot %q has no default, so its row would have nothing to name", slot)
			continue
		}
		if !strings.HasPrefix(id, "claude-"+slot) {
			t.Errorf("ClaudeCodeDefaults[%q] = %q, which is not that slot's model", slot, id)
		}
	}
	if len(ClaudeCodeDefaults) != len(Slots) {
		t.Errorf("ClaudeCodeDefaults names %d models for %d slots; the two are meant to be the same set",
			len(ClaudeCodeDefaults), len(Slots))
	}
}
