package launch

import (
	"strings"

	"github.com/shichao-wang/cpa/internal/config"
)

// PickerRow is one model the profile can route to. BehavesAs is omitted for
// Claude Code's own ids and when a contextWindow must take precedence.
type PickerRow struct {
	Model     string `json:"model"`
	BehavesAs string `json:"behavesAs,omitempty"`
	Label     string `json:"label,omitempty"`
}

// PickerRows lists the resolved models in slot order, once per id. An explicit
// catch-all and custom option remain reachable even if no slot points to them.
// Passing the profile's Models map makes this usable before discovery (create);
// passing the resolved mapping also covers family/alias profiles at launch.
func PickerRows(p *config.Profile, models map[string]string) []PickerRow {
	seen := map[string]bool{}
	var rows []PickerRow
	for _, slot := range config.Slots {
		id := strings.TrimSpace(models[slot])
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		row := PickerRow{Model: id, Label: strings.TrimSpace(p.ModelNames[slot])}
		// A catch-all with no slot pin has no particular Claude counterpart:
		// MapModels fills all slots with it, but that does not make it Opus.
		catchAll := id == strings.TrimSpace(p.Model) && p.ModelFor(slot) == "" && p.Family == ""
		if target := config.ClaudeCodeDefaults[slot]; target != "" && p.ContextWindow == 0 && !catchAll && !strings.HasPrefix(strings.ToLower(id), "claude-") {
			row.BehavesAs = target
		}
		rows = append(rows, row)
	}
	for _, id := range []string{p.Model, p.CustomModelOption} {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			rows = append(rows, PickerRow{Model: id})
		}
	}
	return rows
}

// PickerSettings replaces Claude Code's built-in and gateway-discovered rows
// with only the models the profile can route to. The Default row stays.
func PickerSettings(rows []PickerRow) map[string]interface{} {
	return map[string]interface{}{
		"replaceBuiltInOptions": true,
		"options":               rows,
	}
}
