package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
)

func TestProfileCreateChecksNameBeforeOtherFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{"profiles":{"taken":{"baseUrl":"http://old"}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	err := cmdProfileCreate(context.Background(), []string{"--file", path, "--name", "taken"})
	if err == nil || !strings.Contains(err.Error(), "profile edit") {
		t.Fatalf("expected early edit hint, got %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatal("conflict changed the file")
	}
}

func TestProfileEditPatchesOnlySpecifiedFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{"defaultProfile":"dev","profiles":{"dev":{"agent":"claude","baseUrl":"http://old","description":"before","apiKeyEnv":"SECRET","contextWindow":1000000,"futureField":{"nested":true},"claudeSettings":{"theme":"dark"}},"other":{"baseUrl":"http://other"}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdProfileEdit(context.Background(), []string{"dev", "--file", path, "--description", ""}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	profiles := raw["profiles"].(map[string]interface{})
	dev := profiles["dev"].(map[string]interface{})
	if _, ok := dev["description"]; ok {
		t.Fatal("explicit empty description did not clear the field")
	}
	if dev["apiKeyEnv"] != "SECRET" || dev["contextWindow"] != float64(1000000) || dev["baseUrl"] != "http://old" {
		t.Fatalf("untouched fields changed: %v", dev)
	}
	if dev["futureField"].(map[string]interface{})["nested"] != true || dev["claudeSettings"].(map[string]interface{})["theme"] != "dark" {
		t.Fatalf("extension fields changed: %v", dev)
	}
	if profiles["other"].(map[string]interface{})["baseUrl"] != "http://other" || raw["defaultProfile"] != "dev" {
		t.Fatal("neighboring settings changed")
	}
}

func TestProfileEditExplicitlyClearsCredentialSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"profiles":{"dev":{"baseUrl":"http://old","apiKeyEnv":"OLD","apiKeyCmd":"echo old"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdProfileEdit(context.Background(), []string{"dev", "--file", path, "--api-key", ""}); err != nil {
		t.Fatal(err)
	}
	p := readProfiles(t, path)["dev"]
	if p.APIKey != "" || p.APIKeyEnv != "" || p.APIKeyCmd != "" {
		t.Fatalf("credential was not cleared: %+v", p)
	}
}

func TestProfileEditSwitchesCredentialSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"profiles":{"dev":{"baseUrl":"http://old","apiKeyEnv":"OLD"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdProfileEdit(context.Background(), []string{"dev", "--file", path, "--api-key", "env:NEW"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)
	dev := raw["profiles"].(map[string]interface{})["dev"].(map[string]interface{})
	if _, ok := dev["apiKeyEnv"]; ok || dev["apiKey"] != "env:NEW" {
		t.Fatalf("credential sources conflict: %v", dev)
	}
}

func TestProfileEditDoesNotCreateAndDoesNotChangeOnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := `{"profiles":{"dev":{"baseUrl":"http://old"}}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"missing", "--file", path, "--description", "x"},
		{"dev", "--file", path, "--description", "x", "--base-url", "bad"},
		{"dev", "--file", path},
	} {
		if err := cmdProfileEdit(context.Background(), args); err == nil {
			t.Fatalf("expected error for %v", args)
		}
		data, _ := os.ReadFile(path)
		if string(data) != original {
			t.Fatalf("error changed the file: %s", data)
		}
	}
}

func TestProfileEditRejectsUnsupportedFlagsAndLeavesUnchangedContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"profiles":{"dev":{"baseUrl":"http://old","description":"same"}}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, flag := range [][]string{{"--force"}, {"-p", "prod"}, {"-p=prod"}, {"--profile", "prod"}} {
		args := append([]string{"dev", "--file", path}, flag...)
		args = append(args, "--description", "new")
		if err := cmdProfileEdit(context.Background(), args); err == nil {
			t.Fatalf("edit silently accepted %v", flag)
		}
	}
	if err := cmdProfileEdit(context.Background(), []string{"dev", "--file", path, "--description", "same"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(original) {
		t.Fatal("unchanged edit rewrote the file")
	}
}

func TestProfileEditKeepsUnboundAgent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"profiles":{"dev":{"baseUrl":"http://old"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdProfileEdit(context.Background(), []string{"dev", "--file", path, "--description", "updated"}); err != nil {
		t.Fatal(err)
	}
	p := readProfiles(t, path)["dev"]
	if p.Agent != "" || p.Description != "updated" {
		t.Fatalf("unbound profile was changed unexpectedly: %+v", p)
	}
}

func TestProfileEditChangingAgentClearsClaudeSlots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"profiles":{"dev":{"agent":"claude","baseUrl":"http://old","models":{"opus":"old-model"},"fallbackModel":["fallback"],"family":"old"}}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdProfileEdit(context.Background(), []string{"dev", "--file", path, "--agent", "codex"}); err != nil {
		t.Fatal(err)
	}
	p := readProfiles(t, path)["dev"]
	if p.Agent != "codex" || len(p.Models) != 0 || len(p.FallbackModel) != 0 || p.Family != "" {
		t.Fatalf("incompatible Claude fields retained: %+v", p)
	}
}

func TestProfileEditUsesAgentKindFromTargetFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"agents":{"my-claude":{"kind":"claude","bin":"claude"}},"profiles":{"dev":{"agent":"claude","baseUrl":"http://old","family":"upstream","models":{"opus":"pinned-model"},"fallbackModel":["backup-model"]}}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdProfileEdit(context.Background(), []string{"dev", "--file", path, "--agent", "my-claude"}); err != nil {
		t.Fatal(err)
	}
	p := readProfiles(t, path)["dev"]
	if p.Agent != "my-claude" || p.Family != "upstream" || p.Models["opus"] != "pinned-model" || len(p.FallbackModel) != 1 {
		t.Fatalf("Claude routing was lost: %+v", p)
	}
}

func TestProfileCreateUsesAgentKindFromTargetFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"agents":{"my-claude":{"kind":"claude","bin":"claude"}},"profiles":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdProfileCreate(context.Background(), []string{"--file", path, "--name", "dev", "--agent", "my-claude", "--base-url", "http://old", "--model", "upstream", "--no-discover"}); err != nil {
		t.Fatal(err)
	}
	p := readProfiles(t, path)["dev"]
	if p.ClaudeSettings["modelPicker"] == nil {
		t.Fatalf("custom Claude agent did not generate model picker: %+v", p)
	}
}

func TestProfileEditRefusesConcurrentChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	oldData := []byte(`{"profiles":{"dev":{"baseUrl":"http://old"}}}`)
	newData := []byte(`{"profiles":{"dev":{"baseUrl":"http://changed"}}}`)
	if err := os.WriteFile(path, newData, 0o600); err != nil {
		t.Fatal(err)
	}
	old := &config.Profile{BaseURL: "http://old"}
	updated := &config.Profile{BaseURL: "http://new"}
	if err := patchProfileAt(path, "dev", oldData, old, updated); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("expected concurrency error, got %v", err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(newData) {
		t.Fatal("concurrent profile was overwritten")
	}
}

func TestProfileCreateReportsInvalidTargetBeforeDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := []byte("{broken")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdProfileCreate(context.Background(), []string{"--file", path, "--name", "dev"}); err == nil {
		t.Fatal("expected invalid JSON error")
	}
	data, _ := os.ReadFile(path)
	if string(data) != string(original) {
		t.Fatal("invalid target was modified")
	}
}

func TestProfileEditPreservesCustomPicker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := []byte(`{"profiles":{"dev":{"agent":"claude","baseUrl":"http://old","model":"old-model","claudeSettings":{"modelPicker":{"options":[{"model":"manual"}]},"theme":"dark"}}}}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdProfileEdit(context.Background(), []string{"dev", "--file", path, "--model", "new-model"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var raw map[string]interface{}
	_ = json.Unmarshal(data, &raw)
	settings := raw["profiles"].(map[string]interface{})["dev"].(map[string]interface{})["claudeSettings"].(map[string]interface{})
	picker := settings["modelPicker"].(map[string]interface{})
	if picker["options"].([]interface{})[0].(map[string]interface{})["model"] != "manual" || settings["theme"] != "dark" {
		t.Fatalf("custom picker changed: %v", settings)
	}
}

func TestProfileEditUpdatesGeneratedPicker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	old := &config.Profile{Agent: "claude", BaseURL: "http://old", Model: "old-model"}
	old.ClaudeSettings = withModelPicker(nil, pickerRows(old))
	if err := upsertProfileAt(path, "dev", old); err != nil {
		t.Fatal(err)
	}
	if err := cmdProfileEdit(context.Background(), []string{"dev", "--file", path, "--model", "new-model"}); err != nil {
		t.Fatal(err)
	}
	p := readProfiles(t, path)["dev"]
	rows := pickerRows(p)
	if len(rows) != 1 || rows[0].Model != "new-model" {
		t.Fatalf("picker rows = %v", rows)
	}
	picker := p.ClaudeSettings["modelPicker"].(map[string]interface{})
	options := picker["options"].([]interface{})
	if options[0].(map[string]interface{})["model"] != "new-model" {
		t.Fatalf("generated picker not updated: %v", picker)
	}
}
