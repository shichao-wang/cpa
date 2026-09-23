// Package launch turns a profile into a concrete process: the binary to run,
// its arguments, and the environment it inherits.
//
// Everything cpa injects lives in the child process environment (plus, for
// Claude Code, an inline --settings JSON). Nothing is ever written to
// ~/.claude/settings.json, so the native `claude` command is untouched.
package launch

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/proxy"
)

// MappingSource records how the model slots were decided, so the CLI can be
// transparent about it.
type MappingSource string

const (
	SourceExplicit  MappingSource = "explicit"
	SourceFamily    MappingSource = "family"
	SourceAlias     MappingSource = "claude aliases"
	SourceCatchAll  MappingSource = "catch-all model"
	SourceOnlyModel MappingSource = "only model advertised"
	SourceNone      MappingSource = "unset"
)

// Plan is a fully resolved launch.
type Plan struct {
	Agent       string
	AgentKind   config.Kind
	ProfileName string
	Profile     *config.Profile

	Bin  string
	Args []string

	// Env holds variables to set in the child; Unset holds names to strip
	// from the inherited environment.
	Env   map[string]string
	Unset []string

	Models  map[string]string
	Source  MappingSource
	Notices []string

	// SettingsBlob is the inline Claude Code settings document cpa passes
	// through --settings. It carries the profile's env because Claude Code
	// applies settings.json env on top of the inherited process
	// environment: process variables alone cannot override a global
	// ANTHROPIC_BASE_URL. --settings can.
	SettingsBlob []byte
	// SettingsPath is where Run writes SettingsBlob.
	SettingsPath string
}

// Options are the knobs the CLI exposes.
type Options struct {
	// Available is the endpoint's model list, when discovery succeeded.
	// A nil slice means discovery was skipped or failed; the reasons for
	// that are appended to Plan.Notices.
	Available []proxy.Model
	// AllowSettingsConflict lets a launch proceed when the user passed
	// their own --settings. cpa's settings come last in that case.
	AllowSettingsConflict bool
}

// Build resolves agent + profile into a Plan.
func Build(cfg *config.Config, agentName, profileName string, userArgs []string, opts Options) (*Plan, error) {
	agent, err := cfg.AgentFor(agentName)
	if err != nil {
		return nil, err
	}
	resolvedName, profile, err := cfg.Profile(profileName)
	if err != nil {
		return nil, err
	}

	apiKey, err := profile.ResolveAPIKey()
	if err != nil {
		return nil, fmt.Errorf("profile %q: %w", resolvedName, err)
	}

	plan := &Plan{
		Agent:       agentName,
		AgentKind:   agent.Kind,
		ProfileName: resolvedName,
		Profile:     profile,
		Bin:         agent.Bin,
		Env:         map[string]string{},
	}

	if agent.Kind == config.KindClaude {
		models, source, notices := MapModels(profile, opts.Available)
		plan.Models = models
		plan.Source = source
		plan.Notices = append(plan.Notices, notices...)
		applyClaudeEnv(plan, profile, models, apiKey)
	} else {
		applyOtherEnv(plan, profile, apiKey)
	}

	// Layering: config defaults first, then profile env, so an explicit
	// profile value always wins.
	for k, v := range cfg.Defaults.Env {
		if _, taken := plan.Env[k]; !taken {
			plan.Env[k] = v
		}
	}
	for k, v := range profile.ExtraEnv {
		plan.Env[k] = v
	}
	// A profile that sets these by hand must not be second-guessed.
	if _, ok := plan.Env["ANTHROPIC_API_KEY"]; ok {
		plan.Unset = removeString(plan.Unset, "ANTHROPIC_API_KEY")
	}

	plan.Args = append(plan.Args, agent.Args...)
	plan.Args = append(plan.Args, profile.Args...)

	if agent.Kind == config.KindClaude {
		blob, err := buildSettings(cfg, profile, plan.Env)
		if err != nil {
			return nil, err
		}
		if blob != nil {
			if hasSettingsFlag(userArgs) && !opts.AllowSettingsConflict {
				return nil, fmt.Errorf(
					"you passed --settings, which cpa also needs: it is how the profile's " +
						"endpoint reaches Claude Code (a global settings.json env block would " +
						"otherwise win over the process environment).\n" +
						"Move those settings into this profile's \"claudeSettings\", or pass " +
						"--allow-settings-conflict to let cpa's settings come last")
			}
			plan.SettingsBlob = blob
			plan.SettingsPath = settingsPath()
			plan.Args = append(plan.Args, "--settings", plan.SettingsPath)
		}
	}
	plan.Args = append(plan.Args, userArgs...)
	return plan, nil
}

func hasSettingsFlag(args []string) bool {
	for _, a := range args {
		if a == "--settings" || strings.HasPrefix(a, "--settings=") {
			return true
		}
	}
	return false
}

// buildSettings assembles the --settings document: the profile's own Claude
// Code settings, plus the launch environment. An env block written by hand
// in claudeSettings wins over the generated one.
func buildSettings(cfg *config.Config, p *config.Profile, env map[string]string) ([]byte, error) {
	settings := map[string]interface{}{}
	for k, v := range cfg.Defaults.ClaudeSettings {
		settings[k] = v
	}
	for k, v := range p.ClaudeSettings {
		settings[k] = v
	}

	merged := map[string]interface{}{}
	for k, v := range env {
		merged[k] = v
	}
	if manual, ok := settings["env"].(map[string]interface{}); ok {
		for k, v := range manual {
			merged[k] = fmt.Sprint(v)
		}
	}
	if len(merged) > 0 {
		settings["env"] = merged
	}
	if len(settings) == 0 {
		return nil, nil
	}
	return marshalNoEscape(settings, "")
}

// settingsPath is a private, per-process file. Keeping the document on disk
// rather than inline in argv keeps the API key out of `ps` output.
func settingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), fmt.Sprintf("cpa-settings-%d.json", os.Getpid()))
	}
	return filepath.Join(home, ".cpa", "run", fmt.Sprintf("settings-%d.json", os.Getpid()))
}

// applyClaudeEnv fills the ANTHROPIC_* variables Claude Code reads.
func applyClaudeEnv(plan *Plan, p *config.Profile, models map[string]string, apiKey string) {
	if p.BaseURL != "" {
		plan.Env["ANTHROPIC_BASE_URL"] = p.BaseURL
	}
	if apiKey != "" {
		plan.Env["ANTHROPIC_AUTH_TOKEN"] = apiKey
		// Two credentials in the environment is an ambiguity we can just
		// avoid: the native config may still carry an ANTHROPIC_API_KEY.
		plan.Unset = append(plan.Unset, "ANTHROPIC_API_KEY")
	}

	main := firstNonEmpty(models["opus"], models["sonnet"], p.Model)
	if main != "" {
		plan.Env["ANTHROPIC_MODEL"] = main
	}
	for _, slot := range config.Slots {
		model := models[slot]
		if model == "" {
			continue
		}
		upper := strings.ToUpper(slot)
		plan.Env["ANTHROPIC_DEFAULT_"+upper+"_MODEL"] = model
		plan.Env["ANTHROPIC_DEFAULT_"+upper+"_MODEL_NAME"] = modelLabel(p, slot, model)
		plan.Env["ANTHROPIC_DEFAULT_"+upper+"_MODEL_DESCRIPTION"] =
			fmt.Sprintf("cpa profile %s -> %s", plan.ProfileName, p.BaseURL)
	}

	if p.CustomModelOption != "" {
		plan.Env["ANTHROPIC_CUSTOM_MODEL_OPTION"] = p.CustomModelOption
		plan.Env["ANTHROPIC_CUSTOM_MODEL_OPTION_NAME"] = modelLabel(p, mainSlot(models), p.CustomModelOption)
		plan.Env["ANTHROPIC_CUSTOM_MODEL_OPTION_DESCRIPTION"] =
			fmt.Sprintf("cpa profile %s", plan.ProfileName)
	}

	sub := firstNonEmpty(p.SubagentModel, models["haiku"], main)
	if sub != "" {
		plan.Env["CLAUDE_CODE_SUBAGENT_MODEL"] = sub
	}

	// An explicit contextWindow wins; otherwise a "[1m]" model suffix is
	// Claude Code's own 1M-context notation and implies the flag.
	if p.ContextWindow > 0 {
		plan.Env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = strconv.Itoa(p.ContextWindow)
	} else if has1M(models) {
		plan.Env["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = "1000000"
	}
}

// applyOtherEnv handles non-Claude agents with the same profile data.
func applyOtherEnv(plan *Plan, p *config.Profile, apiKey string) {
	switch plan.AgentKind {
	case config.KindOpenAI:
		if p.BaseURL != "" {
			plan.Env["OPENAI_BASE_URL"] = p.BaseURL
		}
		if apiKey != "" {
			plan.Env["OPENAI_API_KEY"] = apiKey
		}
		if main := firstNonEmpty(p.Model, p.Models["opus"]); main != "" {
			plan.Env["OPENAI_MODEL"] = main
		}
	}
}

func modelLabel(p *config.Profile, slot, model string) string {
	if v, ok := p.ModelNames[slot]; ok && v != "" {
		return v
	}
	return model
}

func mainSlot(models map[string]string) string {
	for _, slot := range []string{"opus", "sonnet", "haiku", "fable"} {
		if models[slot] != "" {
			return slot
		}
	}
	return "opus"
}

func has1M(models map[string]string) bool {
	for _, m := range models {
		if strings.Contains(m, "[1m]") {
			return true
		}
	}
	return false
}

// MapModels decides which upstream model backs each Claude Code slot.
//
// Precedence: hand-pinned slots, then the profile's family matched against
// the endpoint's catalogue, then Claude-shaped aliases the gateway may have
// created, then the catch-all model. A family that resolves to a single
// model fills every slot — that is the "all traffic goes to DeepSeek" case.
func MapModels(p *config.Profile, available []proxy.Model) (map[string]string, MappingSource, []string) {
	var notices []string
	models := map[string]string{}
	for _, slot := range config.Slots {
		if v := p.ModelFor(slot); v != "" {
			models[slot] = v
		}
	}
	if len(models) == len(config.Slots) {
		return models, SourceExplicit, notices
	}

	if p.Family != "" && len(available) > 0 {
		var hits []proxy.Model
		family := strings.ToLower(p.Family)
		for _, m := range available {
			if strings.Contains(strings.ToLower(m.ID), family) {
				hits = append(hits, m)
			}
		}
		switch len(hits) {
		case 0:
			notices = append(notices, fmt.Sprintf("no advertised model matches family %q", p.Family))
		case 1:
			for _, slot := range config.Slots {
				if models[slot] == "" {
					models[slot] = hits[0].ID
				}
			}
			return models, SourceFamily, notices
		default:
			bySlot := classifyAll(hits)
			for _, slot := range config.Slots {
				if models[slot] == "" && bySlot[slot] != "" {
					models[slot] = bySlot[slot]
				}
			}
			fillMissing(models, strongest(bySlot))
			return models, SourceFamily, notices
		}
	} else if p.Family != "" {
		notices = append(notices, fmt.Sprintf("family %q set but the model catalogue is unavailable", p.Family))
	}

	// Gateways commonly alias the upstream onto Claude-shaped names.
	if filled := fillFromAliases(models, available); filled {
		fillMissing(models, strongest(models))
		return models, SourceAlias, notices
	}

	if p.Model != "" {
		fillMissing(models, p.Model)
		return models, SourceCatchAll, notices
	}

	if len(available) == 1 {
		fillMissing(models, available[0].ID)
		return models, SourceOnlyModel, notices
	}

	if missing := missingSlots(models); len(missing) > 0 {
		notices = append(notices, "no model resolved for slot(s) "+strings.Join(missing, ", ")+
			"; Claude Code will use its own defaults for those")
	}
	return models, SourceNone, notices
}

// fillFromAliases looks for claude-<slot>-* entries in the catalogue.
func fillFromAliases(models map[string]string, available []proxy.Model) bool {
	filled := false
	for _, slot := range config.Slots {
		if models[slot] != "" {
			continue
		}
		want := "claude-" + slot
		for _, m := range available {
			if strings.HasPrefix(strings.ToLower(m.ID), want) {
				models[slot] = m.ID
				filled = true
				break
			}
		}
	}
	return filled
}

// classifyAll buckets advertised models by rough capability.
func classifyAll(hits []proxy.Model) map[string]string {
	bySlot := map[string]string{}
	for _, m := range hits {
		slot := classify(m.ID)
		if slot != "" && bySlot[slot] == "" {
			bySlot[slot] = m.ID
		}
	}
	return bySlot
}

var (
	smallHints = []string{"haiku", "flash", "mini", "nano", "lite", "small", "tiny", "edge", "8b"}
	largeHints = []string{"opus", "pro", "max", "ultra", "large", "heavy", "70b", "72b", "235b", "405b", "671b"}
	midHints   = []string{"sonnet", "medium", "balanced", "32b", "34b"}
)

// classify guesses which slot a model belongs in from its name. It is a
// heuristic on purpose: an explicit `models` map always beats it.
func classify(id string) string {
	l := strings.ToLower(id)
	for _, h := range smallHints {
		if strings.Contains(l, h) {
			return "haiku"
		}
	}
	for _, h := range largeHints {
		if strings.Contains(l, h) {
			return "opus"
		}
	}
	for _, h := range midHints {
		if strings.Contains(l, h) {
			return "sonnet"
		}
	}
	return ""
}

func rank(model string) int {
	switch classify(model) {
	case "opus":
		return 3
	case "sonnet":
		return 2
	case "haiku":
		return 1
	}
	return 0
}

// strongest returns the highest-capability model in the set, which is the
// least surprising stand-in for slots nobody specified.
func strongest(models map[string]string) string {
	best, bestRank := "", -1
	for _, slot := range config.Slots {
		m := models[slot]
		if m == "" {
			continue
		}
		if r := rank(m); r > bestRank {
			best, bestRank = m, r
		}
	}
	return best
}

func fillMissing(models map[string]string, fallback string) {
	if fallback == "" {
		return
	}
	for _, slot := range config.Slots {
		if models[slot] == "" {
			models[slot] = fallback
		}
	}
}

func missingSlots(models map[string]string) []string {
	var out []string
	for _, slot := range config.Slots {
		if models[slot] == "" {
			out = append(out, slot)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func removeString(list []string, target string) []string {
	out := list[:0:0]
	for _, v := range list {
		if v != target {
			out = append(out, v)
		}
	}
	return out
}

// EnvListing renders every variable the plan controls, marked "+" when it
// differs from the current environment, "=" when it already matches, and "-"
// when the plan strips it. Showing the full set (not just the diff) is what
// makes --dry-run trustworthy: you can see the whole launch environment.
func (p *Plan) EnvListing() []string {
	out := []string{}
	names := make([]string, 0, len(p.Env))
	for k := range p.Env {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		marker := "+"
		if cur, ok := lookupEnv(k); ok && cur == p.Env[k] {
			marker = "="
		}
		out = append(out, fmt.Sprintf("%s %s=%s", marker, k, p.Env[k]))
	}
	unset := append([]string(nil), p.Unset...)
	sort.Strings(unset)
	for _, k := range unset {
		if _, ok := lookupEnv(k); ok {
			out = append(out, fmt.Sprintf("- %s", k))
		} else {
			out = append(out, fmt.Sprintf("  %s (already unset)", k))
		}
	}
	return out
}
