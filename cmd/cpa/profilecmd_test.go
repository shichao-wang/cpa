package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// familiesOf offers the handle a person would type, which for a family (a
// substring match) is the part of the id before its first separator.
func TestFamiliesOf(t *testing.T) {
	models := []proxy.Model{
		{ID: "deepseek-chat"}, {ID: "deepseek-reasoner"},
		{ID: "gpt-4o"}, {ID: "o1"}, {ID: "deepseek-chat"},
	}
	got := familiesOf(models)
	want := []string{"deepseek", "gpt", "o1"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("familiesOf = %v, want %v", got, want)
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

func TestSummariseCapsTheList(t *testing.T) {
	models := []proxy.Model{{ID: "c"}, {ID: "a"}, {ID: "b"}, {ID: "d"}}
	got := summarise(models)
	if !strings.Contains(got, "a, b") || !strings.Contains(got, "+2 more") {
		t.Errorf("summarise = %q, want the first two ids and a count", got)
	}
	if got := summarise(nil); got != "no models" {
		t.Errorf("summarise(nil) = %q", got)
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
