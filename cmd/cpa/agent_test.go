package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// `profile create` records the one agent a profile is for, so a profile is
// never ambiguous about its downstream — including the default, which is the
// agent cpa is exercised against.
func TestProfileCreateRecordsTheAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")

	err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "devbox",
		"--base-url", "http://127.0.0.1:18317",
		"--no-discover",
	})
	if err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}
	if got := readProfiles(t, path)["devbox"].Agent; got != "claude" {
		t.Errorf("agent = %q, want the default %q", got, "claude")
	}

	err = cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "codex",
		"--agent", "codex",
		"--base-url", "http://127.0.0.1:18317",
		"--model", "gpt-6-sol",
		"--no-discover",
	})
	if err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}
	p := readProfiles(t, path)["codex"]
	if p.Agent != "codex" {
		t.Errorf("agent = %q, want codex", p.Agent)
	}
	if p.Model != "gpt-6-sol" {
		t.Errorf("model = %q", p.Model)
	}
}

// The agent lands in the file as a field of its own, under the name the schema
// documents, rather than being left to inference.
func TestAgentIsWrittenAsItsOwnField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := cmdProfileCreate(context.Background(), []string{
		"--file", path, "--name", "tuned", "--agent", "claude",
		"--base-url", "http://127.0.0.1:18317", "--no-discover",
	}); err != nil {
		t.Fatalf("cmdProfileCreate: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Profiles map[string]map[string]interface{} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("settings are not valid JSON: %v\n%s", err, data)
	}
	if doc.Profiles["tuned"]["agent"] != "claude" {
		t.Errorf("the written profile has no agent field: %v", doc.Profiles["tuned"])
	}
}

// `import-claude` reads Claude Code's own settings, so what it writes is a
// Claude Code profile even when nothing in the imported env says so on its own.
func TestImportClaudeBindsTheProfileToClaude(t *testing.T) {
	home, configHome := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", configHome)

	source := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	// An endpoint and a key are all the imported profile gets, and neither
	// field is Claude Code's alone.
	body := `{"env": {"ANTHROPIC_BASE_URL": "http://gw", "ANTHROPIC_AUTH_TOKEN": "k"}}`
	if err := os.WriteFile(source, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdImportClaude([]string{"--name", "imported"}); err != nil {
		t.Fatalf("cmdImportClaude: %v", err)
	}
	path := filepath.Join(configHome, "cpa", "settings.json")
	if got := readProfiles(t, path)["imported"].Agent; got != "claude" {
		t.Errorf("agent = %q, want claude", got)
	}
}
