package launch

import (
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
)

// A profile is written for one downstream application. Launching it with an
// agent of another kind must fail rather than quietly hand Claude Code's model
// slots and settings to a tool that cannot read them.
func TestBuildRefusesAProfileForAnotherAgent(t *testing.T) {
	t.Run("a declared agent of another kind is refused", func(t *testing.T) {
		cfg := testConfig()
		cfg.Profiles["deepseek"].Agent = "codex"
		_, err := Build(cfg, "claude", "", nil, Options{})
		if err == nil {
			t.Fatal("a codex profile was applied to claude")
		}
		for _, want := range []string{"deepseek", "codex", "claude"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not name %q: %v", want, err)
			}
		}
	})

	t.Run("Claude-only fields make a profile Claude Code's", func(t *testing.T) {
		// testConfig's profile sets subagentModel, so it is a Claude Code
		// profile even though it declares no agent.
		if _, err := Build(testConfig(), "codex", "", nil, Options{}); err == nil {
			t.Fatal("a Claude Code profile was applied to codex")
		}
	})

	t.Run("a family alone makes a profile Claude Code's", func(t *testing.T) {
		// The shape `profile create` leaves behind when every slot is left on
		// "follow the family": no pinned models, just the family that will
		// resolve them. There is nowhere for it to go but Claude Code's slots.
		cfg := &config.Config{Profiles: map[string]*config.Profile{
			"famonly": {BaseURL: "http://gw", Family: "gpt"},
		}}
		if _, err := Build(cfg, "codex", "famonly", nil, Options{}); err == nil {
			t.Fatal("a family-only profile was applied to codex")
		}
	})

	t.Run("another agent of the same kind is fine", func(t *testing.T) {
		cfg := testConfig()
		cfg.Agents = map[string]config.Agent{"claude-dev": {Bin: "claude", Kind: config.KindClaude}}
		if _, err := Build(cfg, "claude-dev", "", nil, Options{}); err != nil {
			t.Errorf("a profile was refused an agent of its own kind: %v", err)
		}
	})

	t.Run("a declared agent nobody defines is reported", func(t *testing.T) {
		cfg := testConfig()
		cfg.Profiles["deepseek"].Agent = "claud"
		_, err := Build(cfg, "claude", "", nil, Options{})
		if err == nil {
			t.Fatal("a typo'd agent name was accepted")
		}
		if !strings.Contains(err.Error(), "claud") {
			t.Errorf("error does not name the unknown agent: %v", err)
		}
	})

	t.Run("a profile that names no agent fits any", func(t *testing.T) {
		cfg := &config.Config{Profiles: map[string]*config.Profile{
			"bare": {BaseURL: "http://gw", Model: "m"},
		}}
		if _, err := Build(cfg, "codex", "bare", nil, Options{}); err != nil {
			t.Errorf("an unbound profile was refused: %v", err)
		}
	})
}

// The binding is also what makes a non-Claude profile legible: it gets the
// OpenAI variables and none of the slot machinery.
func TestBuildALegibleCodexProfile(t *testing.T) {
	cfg := &config.Config{Profiles: map[string]*config.Profile{
		"codex": {Agent: "codex", BaseURL: "http://gw", APIKey: "k", Model: "gpt-6-sol"},
	}}
	plan, err := Build(cfg, "codex", "codex", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"OPENAI_BASE_URL": "http://gw",
		"OPENAI_API_KEY":  "k",
		"OPENAI_MODEL":    "gpt-6-sol",
	} {
		if plan.Env[k] != want {
			t.Errorf("env %s = %q, want %q", k, plan.Env[k], want)
		}
	}
	if len(plan.Models) != 0 {
		t.Errorf("a codex profile was given Claude Code slots: %v", plan.Models)
	}
}
