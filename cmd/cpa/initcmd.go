package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shichao-wang/cpa/internal/config"
)

// starterConfig is what `cpa init` writes. It is JSONC on purpose: profiles
// are easier to keep honest when each one can explain itself.
const starterConfig = `{
  // cpa 的配置文件。它只属于 cpa —— 原生 ` + "`claude`" + ` 命令和 ~/.claude/settings.json
  // 不会被读写，两者互不干扰。
  //
  // 可用字段见 examples/settings.json 与 README。
  "$schema": "https://raw.githubusercontent.com/shichao-wang/cpa/main/examples/settings.schema.json",

  // 不给 --profile 时用哪个。
  "defaultProfile": "deepseek",

  // 所有 profile 共享的环境变量。
  "defaults": {
    "env": {
      "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "90"
    }
  },

  "profiles": {
    // 上游全部走 DeepSeek 系列：CPA 上只有 deepseek-* 模型时，
    // family 命中的那一个模型会填满 claude/sonnet/haiku 全部槽位。
    "deepseek": {
      "description": "全量 DeepSeek 上游",
      "baseUrl": "http://127.0.0.1:8317",
      // 不把 key 写进文件：从环境变量取。也可以写 "cmd:pass show cpa"。
      "apiKeyEnv": "CPA_API_KEY",
      "family": "deepseek",
      "subagentModel": "deepseek-flash"
    },

    // 上游全部走 GPT 系列。前提是 CPA 里已经配好对应上游，
    // 且它在 /v1/models 里以 gpt-* 暴露出来。
    "gpt": {
      "description": "全量 GPT 上游",
      "baseUrl": "http://127.0.0.1:8317",
      "apiKeyEnv": "CPA_API_KEY",
      "family": "gpt"
    },

    // 手工钉死每个槽位，完全跳过模型发现。
    "pinned": {
      "description": "手工指定槽位映射",
      "baseUrl": "http://127.0.0.1:8317",
      "apiKeyEnv": "CPA_API_KEY",
      "models": {
        "opus": "deepseek-flash",
        "sonnet": "deepseek-flash",
        "haiku": "deepseek-flash",
        "fable": "deepseek-flash"
      }
    }
  }
}
`

func cmdInit(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	path := config.UserConfigPath()
	if _, err := os.Stat(path); err == nil && !f.force {
		return fmt.Errorf("%s already exists; pass --force to overwrite", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(starterConfig), 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n\n", path)
	fmt.Println("next steps:")
	fmt.Println("  1. point baseUrl at your CPA instance and set the key, e.g.")
	fmt.Println("       export CPA_API_KEY=sk-...            # for apiKeyEnv")
	fmt.Println("  2. cpa doctor                            # check the endpoint")
	fmt.Println("  3. cpa claude --profile deepseek --dry-run")
	return nil
}

// cmdImportClaude turns the ANTHROPIC_* environment of an existing Claude
// Code settings file into a cpa profile. It reads that file; it never writes
// to it.
func cmdImportClaude(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	source := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", source, err)
	}
	var claudeSettings struct {
		Env   map[string]string `json:"env"`
		Model string            `json:"model"`
	}
	if err := json.Unmarshal(data, &claudeSettings); err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	if len(claudeSettings.Env) == 0 {
		return fmt.Errorf("%s has no \"env\" block to import", source)
	}

	name := f.name
	if name == "" {
		name = "imported"
	}

	profile := &config.Profile{Description: "imported from " + source}
	consumed := map[string]bool{}
	take := func(key string) string {
		consumed[key] = true
		return claudeSettings.Env[key]
	}

	profile.BaseURL = take("ANTHROPIC_BASE_URL")
	if v := take("ANTHROPIC_AUTH_TOKEN"); v != "" {
		profile.APIKey = v
	} else if v := take("ANTHROPIC_API_KEY"); v != "" {
		profile.APIKey = v
	}
	profile.Model = take("ANTHROPIC_MODEL")
	profile.SubagentModel = take("CLAUDE_CODE_SUBAGENT_MODEL")
	if v := take("CLAUDE_CODE_MAX_CONTEXT_TOKENS"); v != "" {
		if n, convErr := strconv.Atoi(v); convErr == nil {
			profile.ContextWindow = n
		}
	}
	if v := take("ANTHROPIC_CUSTOM_MODEL_OPTION"); v != "" {
		profile.CustomModelOption = v
	}

	models := map[string]string{}
	for _, slot := range config.Slots {
		upper := strings.ToUpper(slot)
		if v := claudeSettings.Env["ANTHROPIC_DEFAULT_"+upper+"_MODEL"]; v != "" {
			models[slot] = v
			consumed["ANTHROPIC_DEFAULT_"+upper+"_MODEL"] = true
		}
		// The display names are derived by cpa anyway; drop them.
		consumed["ANTHROPIC_DEFAULT_"+upper+"_MODEL_NAME"] = true
		consumed["ANTHROPIC_DEFAULT_"+upper+"_MODEL_DESCRIPTION"] = true
	}
	if len(models) > 0 {
		profile.Models = models
	}
	// Whatever else was in env is carried over verbatim, minus the display
	// strings cpa regenerates.
	extra := map[string]string{}
	for k, v := range claudeSettings.Env {
		if consumed[k] || strings.HasSuffix(k, "_MODEL_NAME") || strings.HasSuffix(k, "_MODEL_DESCRIPTION") {
			continue
		}
		extra[k] = v
	}
	if len(extra) > 0 {
		profile.ExtraEnv = extra
	}

	if err := upsertProfile(name, profile); err != nil {
		return err
	}
	fmt.Printf("imported profile %q into %s\n", name, config.UserConfigPath())
	keyNote := ""
	if profile.APIKey != "" {
		keyNote = "\n  note: the API key was copied into that file as plain text.\n" +
			"        Consider replacing it with \"apiKeyEnv\": \"CPA_API_KEY\"."
	}
	fmt.Printf("  baseUrl: %s\n  models:  %d slot(s) pinned%s\n", profile.BaseURL, len(models), keyNote)
	return nil
}

// upsertProfile merges one profile into the user's settings file.
func upsertProfile(name string, profile *config.Profile) error {
	return upsertProfileAt(config.UserConfigPath(), name, profile)
}

// upsertProfileAt merges one profile into the settings file at path,
// creating the file if needed. It reads and rewrites only that document, so
// profiles defined in a nearer override file are never pulled in, and it
// only ever touches cpa's own config.
func upsertProfileAt(path, name string, profile *config.Profile) error {
	raw := map[string]interface{}{}

	switch data, err := os.ReadFile(path); {
	case err == nil:
		if config.HasComments(data) {
			fmt.Fprintf(os.Stderr, "note: rewriting %s as plain JSON; comments are not preserved\n", path)
		}
		// A document that is literally null decodes to a nil map, which
		// cannot be assigned into below.
		if raw, err = config.ParseRaw(data); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if raw == nil {
			raw = map[string]interface{}{}
		}
	case os.IsNotExist(err):
		// Nothing to merge into; start from an empty document.
	default:
		return err
	}

	profiles, _ := raw["profiles"].(map[string]interface{})
	if profiles == nil {
		profiles = map[string]interface{}{}
	}
	blob, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	var asMap map[string]interface{}
	_ = json.Unmarshal(blob, &asMap)
	profiles[name] = asMap
	raw["profiles"] = profiles
	if _, ok := raw["defaultProfile"]; !ok {
		raw["defaultProfile"] = name
	}

	out, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o600)
}
