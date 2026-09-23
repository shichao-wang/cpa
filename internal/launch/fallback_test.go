package launch

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/shichao-wang/cpa/internal/config"
)

func TestBuildSettingsFallbackModel(t *testing.T) {
	cfg := &config.Config{}
	cfg.Defaults.ClaudeSettings = map[string]interface{}{"fallbackModel": []string{"default"}, "permissions": true}
	p := &config.Profile{
		FallbackModel:  []string{"sonnet", "haiku"},
		ClaudeSettings: map[string]interface{}{"fallbackModel": []string{"manual"}},
	}
	blob, err := buildSettings(cfg, p, map[string]string{"A": "B"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["fallbackModel"], []interface{}{"sonnet", "haiku"}) || got["permissions"] != true || got["env"].(map[string]interface{})["A"] != "B" {
		t.Errorf("settings = %v", got)
	}
	p.FallbackModel = nil
	blob, err = buildSettings(cfg, p, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got["fallbackModel"], []interface{}{"manual"}) {
		t.Errorf("fallback from claudeSettings = %v", got["fallbackModel"])
	}
	p.ClaudeSettings = nil
	cfg.Defaults.ClaudeSettings = nil
	blob, err = buildSettings(cfg, p, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) != 0 {
		t.Errorf("unset settings = %s", blob)
	}
}
