package launch

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/proxy"
)

func models(ids ...string) []proxy.Model {
	out := make([]proxy.Model, 0, len(ids))
	for _, id := range ids {
		out = append(out, proxy.Model{ID: id})
	}
	return out
}

func TestMapModelsPrecedence(t *testing.T) {
	t.Run("explicit pins win over everything", func(t *testing.T) {
		p := &config.Profile{
			Family: "deepseek",
			Models: map[string]string{"opus": "pinned-opus", "sonnet": "pinned-sonnet",
				"haiku": "pinned-haiku", "fable": "pinned-fable"},
		}
		got, source, _ := MapModels(p, models("deepseek-chat", "deepseek-flash"))
		if source != SourceExplicit {
			t.Fatalf("source = %q, want %q", source, SourceExplicit)
		}
		if got["opus"] != "pinned-opus" {
			t.Errorf("explicit pin ignored: %v", got)
		}
	})

	t.Run("a single family match fills every slot", func(t *testing.T) {
		p := &config.Profile{Family: "deepseek"}
		got, source, _ := MapModels(p, models("deepseek-flash", "claude-opus-5", "gpt-5"))
		if source != SourceFamily {
			t.Fatalf("source = %q, want %q", source, SourceFamily)
		}
		for _, slot := range config.Slots {
			if got[slot] != "deepseek-flash" {
				t.Errorf("slot %s = %q, want deepseek-flash", slot, got[slot])
			}
		}
	})

	t.Run("several family matches are bucketed by capability", func(t *testing.T) {
		p := &config.Profile{Family: "deepseek"}
		got, source, _ := MapModels(p, models("deepseek-v4-pro", "deepseek-flash"))
		if source != SourceFamily {
			t.Fatalf("source = %q, want %q", source, SourceFamily)
		}
		if got["haiku"] != "deepseek-flash" {
			t.Errorf("haiku = %q, want deepseek-flash", got["haiku"])
		}
		if got["opus"] != "deepseek-v4-pro" {
			t.Errorf("opus = %q, want deepseek-v4-pro", got["opus"])
		}
		// Unspecified slots fall back to the strongest model, not to nothing.
		if got["sonnet"] == "" || got["fable"] == "" {
			t.Errorf("unfilled slots: %v", got)
		}
	})

	t.Run("a family with no size hints reports instead of pinning nothing", func(t *testing.T) {
		p := &config.Profile{Family: "acme"}
		got, source, notices := MapModels(p, models("acme-1", "acme-2", "acme-3"))
		if source != SourceNone {
			t.Fatalf("source = %q, want %q", source, SourceNone)
		}
		if len(got) != 0 {
			t.Errorf("mapping = %v, want it empty rather than guessed", got)
		}
		if joined := strings.Join(notices, "; "); !strings.Contains(joined, "acme") {
			t.Errorf("notices = %v, want one naming the family", notices)
		}
	})

	t.Run("an unclassifiable family still honours an explicit catch-all", func(t *testing.T) {
		p := &config.Profile{Family: "acme", Model: "acme-2"}
		got, source, _ := MapModels(p, models("acme-1", "acme-2"))
		if source != SourceCatchAll {
			t.Fatalf("source = %q, want %q", source, SourceCatchAll)
		}
		for _, slot := range config.Slots {
			if got[slot] != "acme-2" {
				t.Errorf("slot %s = %q, want acme-2", slot, got[slot])
			}
		}
	})

	t.Run("falls back to claude-shaped aliases", func(t *testing.T) {
		p := &config.Profile{Family: "gemini"}
		got, source, _ := MapModels(p, models("claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5", "claude-fable-5"))
		if source != SourceAlias {
			t.Fatalf("source = %q, want %q", source, SourceAlias)
		}
		if got["opus"] != "claude-opus-5" || got["fable"] != "claude-fable-5" {
			t.Errorf("alias mapping wrong: %v", got)
		}
	})

	t.Run("catch-all model", func(t *testing.T) {
		p := &config.Profile{Model: "only-this"}
		got, source, _ := MapModels(p, nil)
		if source != SourceCatchAll {
			t.Fatalf("source = %q, want %q", source, SourceCatchAll)
		}
		if got["opus"] != "only-this" || got["haiku"] != "only-this" {
			t.Errorf("catch-all not applied: %v", got)
		}
	})

	t.Run("a lone advertised model is used", func(t *testing.T) {
		p := &config.Profile{}
		got, source, _ := MapModels(p, models("solo"))
		if source != SourceOnlyModel {
			t.Fatalf("source = %q, want %q", source, SourceOnlyModel)
		}
		if got["sonnet"] != "solo" {
			t.Errorf("lone model not used: %v", got)
		}
	})

	t.Run("nothing to go on yields a notice, not a crash", func(t *testing.T) {
		p := &config.Profile{}
		got, source, notices := MapModels(p, models("a", "b"))
		if source != SourceNone {
			t.Fatalf("source = %q, want %q", source, SourceNone)
		}
		if len(got) != 0 {
			t.Errorf("expected no mapping, got %v", got)
		}
		if len(notices) == 0 {
			t.Error("expected a notice explaining the gap")
		}
	})
}

func testConfig() *config.Config {
	return &config.Config{
		DefaultProfile: "deepseek",
		Profiles: map[string]*config.Profile{
			"deepseek": {
				BaseURL:       "http://127.0.0.1:8317",
				APIKey:        "k",
				Family:        "deepseek",
				SubagentModel: "deepseek-flash",
				ExtraEnv:      map[string]string{"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "90"},
			},
		},
	}
}

func TestBuildInjectsClaudeEnv(t *testing.T) {
	plan, err := Build(testConfig(), "claude", "", []string{"--resume"}, Options{
		Available: models("deepseek-flash"),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"ANTHROPIC_BASE_URL":              "http://127.0.0.1:8317",
		"ANTHROPIC_AUTH_TOKEN":            "k",
		"ANTHROPIC_MODEL":                 "deepseek-flash",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":    "deepseek-flash",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":   "deepseek-flash",
		"CLAUDE_CODE_SUBAGENT_MODEL":      "deepseek-flash",
		"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "90",
	}
	for k, v := range want {
		if plan.Env[k] != v {
			t.Errorf("env %s = %q, want %q", k, plan.Env[k], v)
		}
	}
	if plan.Bin != "claude" {
		t.Errorf("bin = %q", plan.Bin)
	}
	// The profile's env rides along in --settings; user args still come last.
	if got := plan.Args[len(plan.Args)-1]; got != "--resume" {
		t.Errorf("user args not forwarded: %v", plan.Args)
	}
	if !strings.Contains(strings.Join(plan.Args, " "), "--settings "+plan.SettingsPath) {
		t.Errorf("expected --settings <path> in argv, got %v", plan.Args)
	}
}

// Claude Code applies settings.json env on top of the inherited process
// environment, so a global ANTHROPIC_BASE_URL beats anything cpa exports.
// --settings is the only channel that reliably wins; this test pins that
// down so the launch mechanism cannot regress into plain env injection.
func TestBuildCarriesEnvThroughSettingsDocument(t *testing.T) {
	plan, err := Build(testConfig(), "claude", "", nil, Options{Available: models("deepseek-flash")})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.SettingsBlob) == 0 {
		t.Fatal("no settings document: a global settings.json env block would win")
	}
	var doc struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(plan.SettingsBlob, &doc); err != nil {
		t.Fatalf("settings document is not valid JSON: %v", err)
	}
	for k, want := range map[string]string{
		"ANTHROPIC_BASE_URL":   "http://127.0.0.1:8317",
		"ANTHROPIC_AUTH_TOKEN": "k",
		"ANTHROPIC_MODEL":      "deepseek-flash",
	} {
		if doc.Env[k] != want {
			t.Errorf("settings env %s = %q, want %q", k, doc.Env[k], want)
		}
	}
}

// A hand-written env block in claudeSettings is an explicit instruction and
// outranks the generated one.
func TestManualEnvInClaudeSettingsWins(t *testing.T) {
	cfg := testConfig()
	cfg.Profiles["deepseek"].ClaudeSettings = map[string]interface{}{
		"env": map[string]interface{}{"ANTHROPIC_BASE_URL": "http://manual"},
	}
	plan, err := Build(cfg, "claude", "", nil, Options{Available: models("deepseek-flash")})
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Env map[string]string `json:"env"`
	}
	_ = json.Unmarshal(plan.SettingsBlob, &doc)
	if doc.Env["ANTHROPIC_BASE_URL"] != "http://manual" {
		t.Errorf("manual env should win, got %q", doc.Env["ANTHROPIC_BASE_URL"])
	}
}

func TestBuildAvoidsAmbiguousCredentials(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "stale-key")
	plan, err := Build(testConfig(), "claude", "", nil, Options{Available: models("deepseek-flash")})
	if err != nil {
		t.Fatal(err)
	}
	var removed bool
	for _, k := range plan.Unset {
		if k == "ANTHROPIC_API_KEY" {
			removed = true
		}
	}
	if !removed {
		t.Fatal("ANTHROPIC_API_KEY should be stripped when a profile supplies its own token")
	}

	env := plan.Environ()
	for _, kv := range env {
		if strings.HasPrefix(kv, "ANTHROPIC_API_KEY=") {
			t.Errorf("stale key survived into the child environment: %s", kv)
		}
	}
	if len(env) != len(uniqueKeys(env)) {
		t.Error("duplicate environment entries")
	}
}

func uniqueKeys(env []string) map[string]bool {
	out := map[string]bool{}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			out[kv[:i]] = true
		}
	}
	return out
}

func TestBuildOneMSuffixSetsContextWindow(t *testing.T) {
	cfg := testConfig()
	cfg.Profiles["deepseek"].Models = map[string]string{
		"opus": "deepseek-flash[1m]", "sonnet": "deepseek-flash[1m]",
		"haiku": "deepseek-flash", "fable": "deepseek-flash[1m]",
	}
	plan, err := Build(cfg, "claude", "", nil, Options{Available: models("deepseek-flash")})
	if err != nil {
		t.Fatal(err)
	}
	if got := plan.Env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"]; got != "1000000" {
		t.Errorf("CLAUDE_CODE_MAX_CONTEXT_TOKENS = %q, want 1000000", got)
	}
}

func TestBuildCarriesExtraClaudeSettings(t *testing.T) {
	cfg := testConfig()
	cfg.Profiles["deepseek"].ClaudeSettings = map[string]interface{}{
		"permissions": map[string]interface{}{"defaultMode": "acceptEdits"},
	}
	plan, err := Build(cfg, "claude", "", nil, Options{Available: models("deepseek-flash")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan.SettingsBlob), "acceptEdits") {
		t.Errorf("claudeSettings missing from the document: %s", plan.SettingsBlob)
	}
}

// Two writers for one flag would silently pick the wrong upstream, so the
// conflict is reported instead of guessed at.
func TestBuildRefusesCompetingSettingsFlag(t *testing.T) {
	_, err := Build(testConfig(), "claude", "", []string{"--settings", "mine.json"}, Options{})
	if err == nil {
		t.Fatal("expected an error when the user passes their own --settings")
	}
	if !strings.Contains(err.Error(), "allow-settings-conflict") {
		t.Errorf("error should name the escape hatch, got: %v", err)
	}

	plan, err := Build(testConfig(), "claude", "", []string{"--settings", "mine.json"},
		Options{AllowSettingsConflict: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan.Args, " "), "--settings mine.json") {
		t.Errorf("user --settings should survive: %v", plan.Args)
	}
}

func TestPlanNeverWritesClaudeConfig(t *testing.T) {
	// A regression guard for the project's central promise: building a plan
	// is a pure function of its inputs.
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := home + "/.claude"
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := claudeDir + "/settings.json"
	original := []byte(`{"model":"opus"}`)
	if err := os.WriteFile(settings, original, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Build(testConfig(), "claude", "", nil, Options{Available: models("deepseek-flash")}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatalf("claude settings.json was modified:\n%s", after)
	}
}
