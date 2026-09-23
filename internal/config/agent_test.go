package config

import "testing"

// TestEffectiveAgent pins down which agent a profile belongs to. A profile
// carrying Claude Code's own fields is a Claude Code profile whether or not it
// says so — that inference is what keeps settings written before the "agent"
// field existed from being applied to another agent.
func TestEffectiveAgent(t *testing.T) {
	cases := []struct {
		name string
		p    Profile
		want string
	}{
		{"a declared agent wins", Profile{Agent: "codex", Models: map[string]string{"opus": "x"}}, "codex"},
		{"nothing declared, nothing implied", Profile{BaseURL: "http://gw", Model: "gpt-6-sol"}, ""},
		{"a catch-all model implies nothing", Profile{Model: "gpt-6-sol"}, ""},
		{"a family fills Claude Code's slots", Profile{Family: "deepseek"}, "claude"},
		{"pinned slots are Claude Code's", Profile{Models: map[string]string{"opus": "x"}}, "claude"},
		{"fallback models are Claude Code's", Profile{FallbackModel: []string{"sonnet"}}, "claude"},
		{"empty fallback models imply nothing", Profile{FallbackModel: []string{}}, ""},
		{"slot labels are Claude Code's", Profile{ModelNames: map[string]string{"opus": "x"}}, "claude"},
		{"a subagent model is Claude Code's", Profile{SubagentModel: "m"}, "claude"},
		{"a picker entry is Claude Code's", Profile{CustomModelOption: "m"}, "claude"},
		{"a context window is Claude Code's", Profile{ContextWindow: 1000000}, "claude"},
		{"extra Claude Code settings are Claude Code's", Profile{ClaudeSettings: map[string]interface{}{"permissions": true}}, "claude"},
	}
	for _, tc := range cases {
		if got := tc.p.EffectiveAgent(); got != tc.want {
			t.Errorf("%s: EffectiveAgent = %q, want %q", tc.name, got, tc.want)
		}
	}
}
