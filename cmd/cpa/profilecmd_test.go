package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/proxy"
)

func readProfiles(t *testing.T, path string) map[string]*config.Profile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	cfg, err := config.Parse(data)
	if err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
	return cfg.Profiles
}

// The tests below drive `profile create` the way a script does: every field
// arrives as a flag, and the test binary's stdin is not a terminal, so the
// interactive path is never entered.

func TestProfileCreateWritesProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cpa", "settings.json")
	err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "devbox",
		"--base-url", "http://127.0.0.1:18317",
		"--api-key", "env:CPA_KEY",
		"--description", "devbox gateway",
		"--family", "deepseek",
		"--model", "deepseek-flash[1m]",
		"--no-discover",
	})
	if err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}

	p, ok := readProfiles(t, path)["devbox"]
	if !ok {
		t.Fatal("the profile was not written")
	}
	if p.BaseURL != "http://127.0.0.1:18317" {
		t.Errorf("baseUrl = %q", p.BaseURL)
	}
	if p.APIKey != "env:CPA_KEY" {
		t.Errorf("apiKey = %q", p.APIKey)
	}
	if p.Description != "devbox gateway" {
		t.Errorf("description = %q", p.Description)
	}
	if p.Family != "deepseek" {
		t.Errorf("family = %q", p.Family)
	}
	if p.Model != "deepseek-flash[1m]" {
		t.Errorf("model = %q", p.Model)
	}
}

func TestProfileCreateKeepsOtherContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{"defaultProfile": "first", "profiles": {"first": {"baseUrl": "http://first"}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "second",
		"--base-url", "http://second", "--no-discover",
	})
	if err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}

	profiles := readProfiles(t, path)
	if len(profiles) != 2 {
		t.Fatalf("got %d profiles, want 2", len(profiles))
	}
	if profiles["first"].BaseURL != "http://first" {
		t.Error("the neighbouring profile was disturbed")
	}
}

func TestProfileCreateRefusesOverwriteWithoutConsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{"profiles": {"devbox": {"baseUrl": "http://keep-me"}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "devbox",
		"--base-url", "http://new", "--no-discover",
	})
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error = %v, want it to name the way out", err)
	}
	if got := readProfiles(t, path)["devbox"].BaseURL; got != "http://keep-me" {
		t.Errorf("baseUrl = %q; the existing profile was modified anyway", got)
	}
}

func TestProfileCreateForceOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"profiles": {"devbox": {"baseUrl": "http://old"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "devbox",
		"--base-url", "http://new", "--force", "--no-discover",
	}); err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}
	if got := readProfiles(t, path)["devbox"].BaseURL; got != "http://new" {
		t.Errorf("baseUrl = %q, want the new one", got)
	}
}

func TestProfileCreateNeedsAName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--base-url", "http://a", "--no-discover",
	})
	if err == nil {
		t.Fatal("expected an error for a missing name")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("a rejected profile should not create the file")
	}
}

func TestProfileCreateNeedsABaseURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "devbox", "--no-discover",
	}); err == nil {
		t.Fatal("expected an error for a missing base URL")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("a rejected profile should not create the file")
	}
}

func TestProfileCreateRejectsABadBaseURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "typo", "--base-url", "devbox", "--no-discover",
	})
	if err == nil {
		t.Fatal("expected a rejection, got nil")
	}
	if !strings.Contains(err.Error(), "does not look like a URL") {
		t.Errorf("error = %v, want it to name the problem", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("a rejected profile should not create the file")
	}
}

func TestProfileCreateDefaultsToTheUserConfig(t *testing.T) {
	// With no --file the profile belongs in $XDG_CONFIG_HOME/cpa/settings.json,
	// the same place cpa reads from.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := cmdProfileCreate(context.Background(), []string{
		"--name", "xdg", "--base-url", "http://xdg", "--no-discover",
	}); err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}
	want := filepath.Join(dir, "cpa", "settings.json")
	if _, ok := readProfiles(t, want)["xdg"]; !ok {
		t.Error("the profile did not land in the user config")
	}
}

// A script must never be left waiting on a prompt it cannot answer.
func TestProfileCreateDoesNotPromptWithoutATerminal(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	// Nothing will ever be written to this pipe: a prompt would block.
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	path := filepath.Join(t.TempDir(), "settings.json")
	if err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "piped",
		"--base-url", "http://piped", "--no-discover",
	}); err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}
	if _, ok := readProfiles(t, path)["piped"]; !ok {
		t.Error("the profile was not written")
	}
}

func TestProfileRejectsUnknownSubcommand(t *testing.T) {
	if err := cmdProfile(context.Background(), []string{"bogus"}); err == nil {
		t.Error("an unknown subcommand should error")
	}
	if err := cmdProfile(context.Background(), []string{"help"}); err != nil {
		t.Errorf("help should not error: %v", err)
	}
}

// A row is named by the model the agent itself means, so the answer reads as
// "Claude Code's opus becomes this" instead of as a bare slot name.
func TestSlotLabelNamesTheAgentsOwnModel(t *testing.T) {
	for _, slot := range config.Slots {
		want := config.ClaudeCodeDefaults[slot]
		if want == "" {
			t.Fatalf("slot %q has no Claude Code default to name it by", slot)
		}
		got := slotLabel(slot)
		if !strings.Contains(got, slot) || !strings.Contains(got, want) {
			t.Errorf("slotLabel(%q) = %q, want it to name %q", slot, got, want)
		}
	}
	if got := slotLabel("not-a-slot"); got != "not-a-slot" {
		t.Errorf("slotLabel of a slot Claude Code does not have = %q, want it unchanged", got)
	}
}

// A candidate the gateway files under another slot says so: that hint is the
// difference between reading one row and reading the whole catalogue.
func TestCandidateLabelHintsAtAnotherSlot(t *testing.T) {
	flash := proxy.Model{ID: "deepseek-v4-flash"}
	if got := candidateLabel(flash, "opus"); !strings.HasSuffix(got, " → haiku") {
		t.Errorf("candidateLabel(flash, opus) = %q, want the haiku hint", got)
	}
	if got := candidateLabel(flash, "haiku"); got != "deepseek-v4-flash" {
		t.Errorf("candidateLabel(flash, haiku) = %q, want no hint on its own row", got)
	}
	if got := candidateLabel(proxy.Model{ID: "gpt-6-sol"}, "opus"); got != "gpt-6-sol" {
		t.Errorf("candidateLabel(a name with no size in it) = %q, want the plain label", got)
	}
}

// The provider rides on the row ahead of the slot hint, so a long row clipped
// on a narrow terminal loses the hint rather than who serves the model.
func TestCandidateLabelCarriesTheProvider(t *testing.T) {
	served := proxy.Model{ID: "deepseek-v4-flash", OwnedBy: "commandcode"}
	if got := candidateLabel(served, "opus"); got != "deepseek-v4-flash  [commandcode] → haiku" {
		t.Errorf("candidateLabel = %q, want the provider before the hint", got)
	}
	if got := candidateLabel(served, "haiku"); got != "deepseek-v4-flash  [commandcode]" {
		t.Errorf("candidateLabel = %q, want the provider on its own row too", got)
	}
}

// The options offered at the prompt have to be the models the launcher will
// actually pick, or the preview would lie.
func TestMatchingAgreesWithTheLauncher(t *testing.T) {
	models := []proxy.Model{{ID: "deepseek-chat"}, {ID: "DeepSeek-Reasoner"}, {ID: "gpt-4o"}}
	if got := len(matching(models, "deepseek")); got != 2 {
		t.Errorf("matching(deepseek) = %d models, want 2 (case should not matter)", got)
	}
	if got := len(matching(models, "")); got != len(models) {
		t.Errorf("an empty family should not filter; got %d", got)
	}
	if got := len(matching(models, "nothing")); got != 0 {
		t.Errorf("matching(nothing) = %d models, want 0", got)
	}
}

// Each row starts on the gateway's model for that same slot, since mapping a
// slot onto itself is the answer that needs no thought. The two sides do not
// always agree on the version — a gateway's claude-opus-5 against an alias's
// claude-opus-5-5 — so the claude-<slot> prefix the launcher matches is the
// fallback, and a slot the gateway does not serve starts unset rather than
// starting wrong.
func TestDefaultSlotChoice(t *testing.T) {
	available := []proxy.Model{
		{ID: "gpt-6-sol"},
		{ID: "claude-opus-5"},
		{ID: "claude-sonnet-5"},
	}
	cases := []struct {
		name    string
		slot    string
		current string
		want    int
	}{
		{"gpt-6-sol is not an opus, so the exact id is not there to pick", "opus", "", 2},
		{"an exact match on the agent's own id wins", "sonnet", "", 3},
		{"a slot the gateway does not serve starts unset", "haiku", "", 0},
		{"a pin already in the profile stays selected", "haiku", "gpt-6-sol", 1},
		{"a pin the gateway no longer serves falls back", "haiku", "long-gone", 0},
	}
	for _, tc := range cases {
		if got := defaultSlotChoice(tc.slot, available, tc.current); got != tc.want {
			t.Errorf("%s: defaultSlotChoice(%q, 3 models, %q) = %d, want %d",
				tc.name, tc.slot, tc.current, got, tc.want)
		}
	}
}

func TestUpsertProfileAtRefusesACommentedFile(t *testing.T) {
	// Settings are plain JSON now, and a rewrite must never silently drop a
	// file's comments. Refusing is the safe half of that: the error explains
	// the format change and the file keeps its contents.
	path := filepath.Join(t.TempDir(), "settings.json")
	original := "{\n  // keep the gateway honest\n  \"profiles\": {}\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	err := upsertProfileAt(path, "a", &config.Profile{BaseURL: "http://a"})
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	if !strings.Contains(err.Error(), "plain JSON") {
		t.Errorf("error does not explain the format change: %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != original {
		t.Error("the file was modified despite the parse failure")
	}
}

func TestUpsertProfileAtStampsTheSchema(t *testing.T) {
	// A generated file should describe itself, since that is where the field
	// semantics live now.
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := upsertProfileAt(path, "a", &config.Profile{BaseURL: "http://a"}); err != nil {
		t.Fatalf("upsertProfileAt: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["$schema"] != config.SchemaURL {
		t.Errorf("$schema = %v, want %s", raw["$schema"], config.SchemaURL)
	}
}

func TestUpsertProfileAtKeepsAnExistingSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"$schema": "./local.schema.json", "profiles": {}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertProfileAt(path, "a", &config.Profile{BaseURL: "http://a"}); err != nil {
		t.Fatalf("upsertProfileAt: %v", err)
	}
	data, _ := os.ReadFile(path)
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["$schema"] != "./local.schema.json" {
		t.Errorf("$schema = %v; an existing pointer was overwritten", raw["$schema"])
	}
}

func TestUpsertProfileAtSurvivesANullDocument(t *testing.T) {
	// "null" is valid JSON and decodes to a nil map; assigning into it would
	// panic rather than report anything useful.
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertProfileAt(path, "a", &config.Profile{BaseURL: "http://a"}); err != nil {
		t.Fatalf("upsertProfileAt: %v", err)
	}
	if _, ok := readProfiles(t, path)["a"]; !ok {
		t.Error("the profile was not written")
	}
}

func TestPickerRows(t *testing.T) {
	cases := []struct {
		name string
		p    *config.Profile
		want []pickerRow
	}{
		{
			"nothing pinned means nothing to list",
			&config.Profile{},
			nil,
		},
		{
			"each slot gets a row naming the agent's own model for it, in slot order",
			&config.Profile{Models: map[string]string{"haiku": "gpt-6-luna", "opus": "gpt-6-sol"}},
			[]pickerRow{
				{Model: "gpt-6-sol", BehavesAs: "claude-opus-5-5"},
				{Model: "gpt-6-luna", BehavesAs: "claude-haiku-4-5"},
			},
		},
		{
			// One id gets one row, and the row is the larger claim: it is
			// the only way the picker can reach the model.
			"a model serving two slots is listed once, as the stronger one",
			&config.Profile{Models: map[string]string{"opus": "gpt-6-sol", "sonnet": "gpt-6-sol"}},
			[]pickerRow{{Model: "gpt-6-sol", BehavesAs: "claude-opus-5-5"}},
		},
		{
			// The row still has to be written — it is how the picker offers
			// the model — but it makes no claim about what it behaves as.
			"a slot pinned to the agent's own id is listed without behavesAs",
			&config.Profile{Models: map[string]string{"haiku": "claude-haiku-4-5"}},
			[]pickerRow{{Model: "claude-haiku-4-5"}},
		},
		{
			// Claude Code is the authority on its own ids: claude-opus-5 is
			// not claude-opus-5-5, and a row saying so would be cpa talking
			// over the agent about the agent's own namespace.
			"an id in the agent's own namespace makes no behavesAs claim",
			&config.Profile{Models: map[string]string{"opus": "claude-opus-5"}},
			[]pickerRow{{Model: "claude-opus-5"}},
		},
		{
			// The catch-all is what every unpinned slot resolves to, so it is
			// the one model this profile routes to and the picker has to
			// reach it.
			"a catch-all with no slot beside it is still listed",
			&config.Profile{Model: "deepseek-flash"},
			[]pickerRow{{Model: "deepseek-flash"}},
		},
		{
			"a name the profile asked for becomes the row's label",
			&config.Profile{
				Models:     map[string]string{"opus": "gpt-6-sol"},
				ModelNames: map[string]string{"opus": "Sol"},
			},
			[]pickerRow{{Model: "gpt-6-sol", BehavesAs: "claude-opus-5-5", Label: "Sol"}},
		},
		{
			// behavesAs hands the window back to the model it names, which
			// outranks CLAUDE_CODE_MAX_CONTEXT_TOKENS. The rows stay; the
			// claim is what goes.
			"a contextWindow profile lists its models without behavesAs",
			&config.Profile{
				Models:        map[string]string{"opus": "gpt-6-sol"},
				ContextWindow: 1000000,
			},
			[]pickerRow{{Model: "gpt-6-sol"}},
		},
	}
	for _, tc := range cases {
		if got := pickerRows(tc.p); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: pickerRows = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProfileCreateWritesPickerRows(t *testing.T) {
	// The interactive path is what fills Models, so the write is driven
	// through commitProfile — the one path a terminal-driven run also uses.
	path := filepath.Join(t.TempDir(), "cpa", "settings.json")
	p := &config.Profile{
		Agent:   "claude",
		BaseURL: "http://127.0.0.1:18317",
		Models:  map[string]string{"opus": "gpt-6-sol", "haiku": "gpt-6-luna"},
	}
	if err := commitProfile(context.Background(), path, "openai", p, &flags{}, nil); err != nil {
		t.Fatalf("commitProfile: %v", err)
	}

	got, ok := readProfiles(t, path)["openai"]
	if !ok {
		t.Fatal("the profile was not written")
	}
	if got.Models["opus"] != "gpt-6-sol" {
		t.Errorf("models = %v; the mapping itself was lost", got.Models)
	}
	picker, ok := got.ClaudeSettings["modelPicker"]
	if !ok {
		t.Fatalf("claudeSettings has no modelPicker: %v", got.ClaudeSettings)
	}
	blob, err := json.Marshal(picker)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ReplaceBuiltInOptions bool        `json:"replaceBuiltInOptions"`
		Options               []pickerRow `json:"options"`
	}
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("modelPicker = %s: %v", blob, err)
	}
	if !decoded.ReplaceBuiltInOptions {
		t.Errorf("modelPicker = %s; the rows must replace the built-in lineup, not join it", blob)
	}
	want := []pickerRow{
		{Model: "gpt-6-sol", BehavesAs: "claude-opus-5-5"},
		{Model: "gpt-6-luna", BehavesAs: "claude-haiku-4-5"},
	}
	if !reflect.DeepEqual(decoded.Options, want) {
		t.Errorf("modelPicker options = %v, want %v", decoded.Options, want)
	}
}

func TestProfileCreateListsTheCatchAllWithoutSlots(t *testing.T) {
	// A catch-all model serves every slot, so there is no one model it
	// behaves as; and the flag-driven flow never pins slots at all. It is
	// still the model this profile sends, so it is the one its picker has to
	// be able to offer — carried verbatim, "[1m]" and all, exactly as the
	// profile hands it to Claude Code.
	path := filepath.Join(t.TempDir(), "cpa", "settings.json")
	if err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "devbox",
		"--base-url", "http://127.0.0.1:18317",
		"--model", "deepseek-flash[1m]",
		"--no-discover",
	}); err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}
	got := readProfiles(t, path)["devbox"]
	picker, ok := got.ClaudeSettings["modelPicker"]
	if !ok {
		t.Fatalf("claudeSettings has no modelPicker: %v", got.ClaudeSettings)
	}
	blob, err := json.Marshal(picker)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Options []pickerRow `json:"options"`
	}
	if err := json.Unmarshal(blob, &decoded); err != nil {
		t.Fatalf("modelPicker = %s: %v", blob, err)
	}
	want := []pickerRow{{Model: "deepseek-flash[1m]"}}
	if !reflect.DeepEqual(decoded.Options, want) {
		t.Errorf("modelPicker options = %v, want %v", decoded.Options, want)
	}
}

func TestProfileCreateKeepsRowsButDropsBehavesAsWithAContextWindow(t *testing.T) {
	// behavesAs makes Claude Code read the window off the model a row names,
	// which outranks CLAUDE_CODE_MAX_CONTEXT_TOKENS: writing both would throw
	// the window away. The row itself still has to be there — it is how the
	// picker offers this profile's model at all.
	path := filepath.Join(t.TempDir(), "cpa", "settings.json")
	p := &config.Profile{
		Agent:         "claude",
		BaseURL:       "http://127.0.0.1:18317",
		Models:        map[string]string{"opus": "gpt-6-sol"},
		ContextWindow: 1000000,
	}
	if err := commitProfile(context.Background(), path, "openai", p, &flags{}, nil); err != nil {
		t.Fatalf("commitProfile: %v", err)
	}
	got := readProfiles(t, path)["openai"]
	picker, ok := got.ClaudeSettings["modelPicker"]
	if !ok {
		t.Fatalf("claudeSettings has no modelPicker: %v", got.ClaudeSettings)
	}
	blob, err := json.Marshal(picker)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "behavesAs") {
		t.Errorf("modelPicker = %s; a contextWindow profile must not also declare behavesAs", blob)
	}
	if !strings.Contains(string(blob), "gpt-6-sol") {
		t.Errorf("modelPicker = %s; the row is how the picker reaches gpt-6-sol", blob)
	}
	if got.ContextWindow != 1000000 {
		t.Errorf("contextWindow = %d, want 1000000", got.ContextWindow)
	}
}
