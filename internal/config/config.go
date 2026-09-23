// Package config loads cpa's own settings file.
//
// cpa keeps its profiles in its own file and never writes to Claude Code's
// configuration. That separation is the whole point: the native `claude`
// command keeps working against whatever the user already has configured.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Kind describes how a profile is translated into environment variables for
// the launched agent.
type Kind string

const (
	// KindClaude injects ANTHROPIC_* variables and maps models onto the
	// opus/sonnet/haiku/fable slots Claude Code understands.
	KindClaude Kind = "claude"
	// KindOpenAI injects OPENAI_* variables.
	KindOpenAI Kind = "openai"
	// KindGeneric injects nothing implicitly; only the profile's own env.
	KindGeneric Kind = "generic"
)

// Agent is a launchable CLI tool.
type Agent struct {
	Bin  string   `json:"bin,omitempty"`
	Kind Kind     `json:"kind,omitempty"`
	Args []string `json:"args,omitempty"`
}

// Defaults are applied to every profile of a config.
type Defaults struct {
	Env            map[string]string      `json:"env,omitempty"`
	ClaudeSettings map[string]interface{} `json:"claudeSettings,omitempty"`
}

// Profile is one named upstream configuration, e.g. "deepseek" or "gpt".
type Profile struct {
	Description string `json:"description,omitempty"`

	// BaseURL is the CPA (or any Anthropic/OpenAI-compatible) endpoint.
	BaseURL string `json:"baseUrl,omitempty"`
	// APIKey is the client key cpa sends upstream. Prefer APIKeyEnv or
	// APIKeyCmd so the key never lands in a file you might commit.
	// "env:NAME" and "cmd:shell command" shorthands are also accepted.
	APIKey    string `json:"apiKey,omitempty"`
	APIKeyEnv string `json:"apiKeyEnv,omitempty"`
	APIKeyCmd string `json:"apiKeyCmd,omitempty"`

	// Family selects models from the endpoint's /v1/models listing by
	// substring. When exactly one model matches, it fills every slot —
	// which is what "all upstream traffic goes to DeepSeek" looks like.
	Family string `json:"family,omitempty"`
	// Model is the catch-all model used for any slot not set explicitly.
	Model string `json:"model,omitempty"`
	// Models pins slots explicitly: {"opus": "...", "sonnet": "...",
	// "haiku": "...", "fable": "..."}.
	Models map[string]string `json:"models,omitempty"`
	// ModelNames overrides the human-readable label shown in Claude Code's
	// model picker, per slot.
	ModelNames map[string]string `json:"modelNames,omitempty"`
	// SubagentModel is the model Claude Code uses for subagents.
	SubagentModel string `json:"subagentModel,omitempty"`
	// CustomModelOption surfaces one model as a hand-picked entry in Claude
	// Code's model picker (ANTHROPIC_CUSTOM_MODEL_OPTION).
	CustomModelOption string `json:"customModelOption,omitempty"`
	// ContextWindow sets CLAUDE_CODE_MAX_CONTEXT_TOKENS for every model.
	ContextWindow int `json:"contextWindow,omitempty"`

	ExtraEnv       map[string]string      `json:"env,omitempty"`
	ClaudeSettings map[string]interface{} `json:"claudeSettings,omitempty"`
	Args           []string               `json:"args,omitempty"`
}

// Config is the root of a settings file.
type Config struct {
	Schema         string              `json:"$schema,omitempty"`
	DefaultProfile string              `json:"defaultProfile,omitempty"`
	DefaultAgent   string              `json:"defaultAgent,omitempty"`
	Agents         map[string]Agent    `json:"agents,omitempty"`
	Defaults       Defaults            `json:"defaults,omitempty"`
	Profiles       map[string]*Profile `json:"profiles,omitempty"`

	// Path is where this config was loaded from; empty for a synthesized one.
	Path string `json:"-"`
}

// Slots are the model slots Claude Code resolves through ANTHROPIC_DEFAULT_*.
var Slots = []string{"opus", "sonnet", "haiku", "fable"}

// userConfigPath returns ~/.cpa/settings.json.
func userConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".cpa", "settings.json")
}

// Candidates lists the files consulted, in increasing order of precedence.
func Candidates() []string {
	var out []string
	if p := userConfigPath(); p != "" {
		out = append(out, p)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		out = append(out, filepath.Join(xdg, "cpa", "settings.json"))
	}
	// Project-local overrides, nearest last.
	out = append(out, ".cpa/settings.json", "cpa.settings.json")
	return out
}

// Load reads the first config found. $CPA_SETTINGS pins an exact file and is
// an error when missing; the conventional paths are optional.
func Load() (*Config, error) {
	if pinned := os.Getenv("CPA_SETTINGS"); pinned != "" {
		cfg, err := loadFile(pinned)
		if err != nil {
			return nil, fmt.Errorf("CPA_SETTINGS=%s: %w", pinned, err)
		}
		return cfg, nil
	}

	var found *Config
	for _, path := range Candidates() {
		cfg, err := loadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if found == nil {
			found = cfg
			continue
		}
		// A nearer file wins per profile name and for the scalar defaults.
		found.Merge(cfg)
	}
	if found == nil {
		return nil, os.ErrNotExist
	}
	return found, nil
}

func loadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(stripJSONC(data), &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Path = path
	return &cfg, nil
}

// Merge layers other on top of c. Profiles replace by name; it keeps the
// mental model simple and predictable.
func (c *Config) Merge(other *Config) {
	if other.DefaultProfile != "" {
		c.DefaultProfile = other.DefaultProfile
	}
	if other.DefaultAgent != "" {
		c.DefaultAgent = other.DefaultAgent
	}
	if c.Agents == nil {
		c.Agents = map[string]Agent{}
	}
	for name, a := range other.Agents {
		c.Agents[name] = a
	}
	if c.Defaults.Env == nil {
		c.Defaults.Env = map[string]string{}
	}
	for k, v := range other.Defaults.Env {
		c.Defaults.Env[k] = v
	}
	c.Defaults.ClaudeSettings = mergeMaps(c.Defaults.ClaudeSettings, other.Defaults.ClaudeSettings)

	if c.Profiles == nil {
		c.Profiles = map[string]*Profile{}
	}
	for name, p := range other.Profiles {
		c.Profiles[name] = p
	}
}

func mergeMaps(a, b map[string]interface{}) map[string]interface{} {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := map[string]interface{}{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// ProfileNames returns the profile names in stable order.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for name := range c.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AgentFor resolves an agent by name, falling back to a sensible default so
// `cpa claude` works with no configuration at all.
func (c *Config) AgentFor(name string) (Agent, error) {
	if a, ok := c.Agents[name]; ok {
		if a.Bin == "" {
			a.Bin = name
		}
		if a.Kind == "" {
			a.Kind = defaultKind(name)
		}
		return a, nil
	}
	switch name {
	case "claude":
		return Agent{Bin: "claude", Kind: KindClaude}, nil
	case "codex":
		return Agent{Bin: "codex", Kind: KindOpenAI}, nil
	}
	return Agent{}, fmt.Errorf("unknown agent %q: define it under \"agents\" in %s", name, c.displayPath())
}

func defaultKind(name string) Kind {
	switch name {
	case "claude":
		return KindClaude
	case "codex":
		return KindOpenAI
	}
	return KindGeneric
}

// Profile resolves a profile by name, applying defaultProfile when name is
// empty.
func (c *Config) Profile(name string) (string, *Profile, error) {
	if name == "" {
		name = c.DefaultProfile
	}
	if name == "" {
		return "", nil, fmt.Errorf("no profile given and no \"defaultProfile\" set in %s", c.displayPath())
	}
	p, ok := c.Profiles[name]
	if !ok {
		return "", nil, fmt.Errorf("unknown profile %q (available: %s)", name, strings.Join(c.ProfileNames(), ", "))
	}
	return name, p, nil
}

func (c *Config) displayPath() string {
	if c.Path == "" {
		return "cpa settings"
	}
	return c.Path
}

// ResolveAPIKey returns the client key for this profile, honoring the
// literal / env / command indirections in that order.
func (p *Profile) ResolveAPIKey() (string, error) {
	fromLiteral := func(v string) (string, error) {
		switch {
		case strings.HasPrefix(v, "env:"):
			name := strings.TrimPrefix(v, "env:")
			got := os.Getenv(name)
			if got == "" {
				return "", fmt.Errorf("apiKey references env:%s, which is empty", name)
			}
			return got, nil
		case strings.HasPrefix(v, "cmd:"):
			return runKeyCommand(strings.TrimPrefix(v, "cmd:"))
		default:
			return v, nil
		}
	}

	switch {
	case p.APIKey != "":
		return fromLiteral(p.APIKey)
	case p.APIKeyEnv != "":
		got := os.Getenv(p.APIKeyEnv)
		if got == "" {
			return "", fmt.Errorf("apiKeyEnv %s is empty", p.APIKeyEnv)
		}
		return got, nil
	case p.APIKeyCmd != "":
		return runKeyCommand(p.APIKeyCmd)
	}
	// Endpoints that need no client key are legitimate (a bare CPA behind a
	// trusted tunnel, for instance).
	return "", nil
}

func runKeyCommand(cmd string) (string, error) {
	out, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil {
		return "", fmt.Errorf("apiKeyCmd %q failed: %w", cmd, err)
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", fmt.Errorf("apiKeyCmd %q produced no output", cmd)
	}
	return key, nil
}

// ModelFor returns the model pinned to a slot, if any.
func (p *Profile) ModelFor(slot string) string {
	if v, ok := p.Models[slot]; ok && v != "" {
		return v
	}
	return ""
}

// HasExplicitModels reports whether the profile pins models by hand, which
// suppresses discovery.
func (p *Profile) HasExplicitModels() bool {
	return len(p.Models) > 0
}
