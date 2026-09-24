package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
)

func TestInitSetsAutoModeServerDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := cmdInit(nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Defaults struct {
			Env map[string]string `json:"env"`
		} `json:"defaults"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if got := settings.Defaults.Env["CLAUDE_CODE_AUTO_MODE_SERVER"]; got != "0" {
		t.Errorf("CLAUDE_CODE_AUTO_MODE_SERVER = %q, want %q", got, "0")
	}
	if got := settings.Defaults.Env["CLAUDE_AUTOCOMPACT_PCT_OVERRIDE"]; got != "90" {
		t.Errorf("CLAUDE_AUTOCOMPACT_PCT_OVERRIDE = %q, want %q", got, "90")
	}

	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(example, &settings); err != nil {
		t.Fatal(err)
	}
	if got := settings.Defaults.Env["CLAUDE_CODE_AUTO_MODE_SERVER"]; got != "0" {
		t.Errorf("example CLAUDE_CODE_AUTO_MODE_SERVER = %q, want %q", got, "0")
	}
}
