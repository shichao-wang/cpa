package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
)

// feedStdin points os.Stdin at a pipe carrying input, so the interactive
// prompts can be driven from a test.
func feedStdin(t *testing.T, input string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		r.Close()
	})
	go func() {
		io.WriteString(w, input)
		w.Close()
	}()
}

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

func TestProfileCreateWritesProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cpa", "settings.json")
	// baseUrl, apiKey, description, family, model — name comes from the flag.
	feedStdin(t, "http://127.0.0.1:18317\nenv:CPA_KEY\ndevbox gateway\n\ndeepseek-flash[1m]\n")

	err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "devbox", "--no-discover",
	})
	if err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}

	p, ok := readProfiles(t, path)["devbox"]
	if !ok {
		t.Fatal("profile devbox was not written")
	}
	if p.BaseURL != "http://127.0.0.1:18317" {
		t.Errorf("baseUrl = %q", p.BaseURL)
	}
	// The env: shorthand must survive verbatim; resolving it here would put
	// the secret in the file.
	if p.APIKey != "env:CPA_KEY" {
		t.Errorf("apiKey = %q, want env:CPA_KEY", p.APIKey)
	}
	if p.Description != "devbox gateway" {
		t.Errorf("description = %q", p.Description)
	}
	if p.Family != "" {
		t.Errorf("family = %q, want empty (blank line takes the default)", p.Family)
	}
	if p.Model != "deepseek-flash[1m]" {
		t.Errorf("model = %q", p.Model)
	}
}

func TestProfileCreateDefaultsToLocalGateway(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// Blank baseUrl takes the default; the rest are blank too.
	feedStdin(t, "\n\n\n\n\n")

	if err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "local", "--no-discover",
	}); err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}
	if got := readProfiles(t, path)["local"].BaseURL; got != "http://127.0.0.1:8317" {
		t.Errorf("baseUrl = %q, want the local default", got)
	}
}

func TestProfileCreateKeepsOtherContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	original := `{
  // a file the user hand-edited
  "defaultProfile": "first",
  "keepMe": {"nested": true},
  "profiles": {"first": {"baseUrl": "http://first"}}
}
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	feedStdin(t, "http://second\n\n\n\n\n")

	if err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "second", "--no-discover",
	}); err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}

	data, _ := os.ReadFile(path)
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("rewritten file is not valid JSON: %v\n%s", err, data)
	}
	if _, ok := raw["keepMe"]; !ok {
		t.Error("an unrelated top-level key was dropped")
	}
	if raw["defaultProfile"] != "first" {
		t.Errorf("defaultProfile = %v, want the existing one kept", raw["defaultProfile"])
	}
	profiles := readProfiles(t, path)
	if len(profiles) != 2 {
		t.Errorf("have %d profiles, want both: %v", len(profiles), profiles)
	}
	if profiles["first"].BaseURL != "http://first" {
		t.Error("the pre-existing profile was clobbered")
	}
}

func TestProfileCreateRefusesOverwriteWithoutConsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{"profiles": {"devbox": {"baseUrl": "http://keep-me"}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	feedStdin(t, "no\n")

	err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "devbox", "--no-discover",
	})
	if err == nil {
		t.Fatal("expected a refusal, got nil")
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
	feedStdin(t, "http://new\n\n\n\n\n")

	if err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "devbox", "--force", "--no-discover",
	}); err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}
	if got := readProfiles(t, path)["devbox"].BaseURL; got != "http://new" {
		t.Errorf("baseUrl = %q, want the new one", got)
	}
}

func TestProfileCreateNeedsAName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// A blank line takes the default, and the name prompt has none.
	feedStdin(t, "   \n")
	if err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--no-discover",
	}); err == nil {
		t.Fatal("expected an error for a blank name")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a rejected profile should not create the file")
	}
}

func TestProfileCreateReadsNameFromPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	// The last line has no trailing newline: a pipe that ends mid-line still
	// has to yield its content.
	feedStdin(t, "piped\nhttp://piped\n\n\n\n")

	if err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--no-discover",
	}); err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}
	if _, ok := readProfiles(t, path)["piped"]; !ok {
		t.Error("the prompted name was not used")
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

func TestUpsertProfileAtWarnsOnComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{\n  // keep the gateway honest\n  \"profiles\": {}\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := upsertProfileAt(path, "a", &config.Profile{BaseURL: "http://a"}); err != nil {
		t.Fatalf("upsertProfileAt: %v", err)
	}
	if _, ok := readProfiles(t, path)["a"]; !ok {
		t.Error("the profile was not written into the JSONC file")
	}
}

func TestProfileCreateDefaultsToTheUserConfig(t *testing.T) {
	// With no --file the profile belongs in $XDG_CONFIG_HOME/cpa/settings.json,
	// the same place cpa reads from.
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	feedStdin(t, "http://xdg\n\n\n\n\n")

	if err := cmdProfileCreate(context.Background(), []string{
		"--name", "xdg", "--no-discover",
	}); err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}
	want := filepath.Join(dir, "cpa", "settings.json")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("expected the profile at %s: %v", want, err)
	}
	if _, ok := readProfiles(t, want)["xdg"]; !ok {
		t.Error("the profile did not land in the user config")
	}
}

func TestProfileCreateRejectsABadBaseURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	feedStdin(t, "devbox\n") // a name typed into the baseUrl prompt
	err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "typo", "--no-discover",
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
