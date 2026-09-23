package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/launch"
	"github.com/shichao-wang/cpa/internal/proxy"
)

// flags holds cpa's own options. Anything not recognized here is forwarded
// to the agent untouched, so agent flags never need escaping.
type flags struct {
	profile               string
	dryRun                bool
	noDiscover            bool
	jsonOut               bool
	force                 bool
	allowSettingsConflict bool
	name                  string
	file                  string
	baseURL               string
	apiKey                string
	description           string
	agent                 string
	family                string
	model                 string
	rest                  []string
}

func parseFlags(args []string) (*flags, error) {
	f := &flags{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			f.rest = append(f.rest, args[i+1:]...)
			break
		}
		takeValue := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return args[i], nil
		}
		var err error
		switch {
		case a == "--profile":
			f.profile, err = takeValue()
		case strings.HasPrefix(a, "--profile="):
			f.profile = strings.TrimPrefix(a, "--profile=")
		case a == "--name":
			f.name, err = takeValue()
		case strings.HasPrefix(a, "--name="):
			f.name = strings.TrimPrefix(a, "--name=")
		case a == "--file":
			f.file, err = takeValue()
		case strings.HasPrefix(a, "--file="):
			f.file = strings.TrimPrefix(a, "--file=")
		case a == "--base-url":
			f.baseURL, err = takeValue()
		case strings.HasPrefix(a, "--base-url="):
			f.baseURL = strings.TrimPrefix(a, "--base-url=")
		case a == "--api-key":
			f.apiKey, err = takeValue()
		case strings.HasPrefix(a, "--api-key="):
			f.apiKey = strings.TrimPrefix(a, "--api-key=")
		case a == "--description":
			f.description, err = takeValue()
		case strings.HasPrefix(a, "--description="):
			f.description = strings.TrimPrefix(a, "--description=")
		case a == "--agent":
			f.agent, err = takeValue()
		case strings.HasPrefix(a, "--agent="):
			f.agent = strings.TrimPrefix(a, "--agent=")
		case a == "--family":
			f.family, err = takeValue()
		case strings.HasPrefix(a, "--family="):
			f.family = strings.TrimPrefix(a, "--family=")
		case a == "--model":
			f.model, err = takeValue()
		case strings.HasPrefix(a, "--model="):
			f.model = strings.TrimPrefix(a, "--model=")
		case a == "--dry-run":
			f.dryRun = true
		case a == "--no-discover":
			f.noDiscover = true
		case a == "--json":
			f.jsonOut = true
		case a == "--force":
			f.force = true
		default:
			f.rest = append(f.rest, a)
		}
		if err != nil {
			return nil, err
		}
	}
	return f, nil
}

// loadConfig wraps the "no config yet" case with an actionable hint.
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err == nil {
		return cfg, nil
	}
	if os.IsNotExist(err) {
		warnLegacyConfig()
		return nil, fmt.Errorf("no settings file found; run `cpa init` to create %s", config.UserConfigPath())
	}
	return nil, err
}

// warnLegacyConfig points users at the move when their settings are still at
// the pre-XDG location and nothing sits at the new one. It stays silent once
// the file has been moved, and on machines that never had one.
func warnLegacyConfig() {
	legacy, current := config.LegacyConfigPath(), config.UserConfigPath()
	if legacy == "" || current == "" {
		return
	}
	if _, err := os.Stat(legacy); err != nil {
		return
	}
	if _, err := os.Stat(current); err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "note: %s is no longer read; move it to %s\n", legacy, current)
}

// discover fetches the gateway's model catalogue for a profile. Failure is
// not fatal: a profile with hand-pinned models still launches fine offline.
func discover(ctx context.Context, p *config.Profile) ([]proxy.Model, string) {
	if p.BaseURL == "" {
		return nil, "profile has no baseUrl; skipping model discovery"
	}
	key, err := p.ResolveAPIKey()
	if err != nil {
		return nil, fmt.Sprintf("could not resolve api key: %v", err)
	}
	models, err := proxy.New(p.BaseURL, key).ListModels(ctx)
	if err != nil {
		return nil, fmt.Sprintf("model discovery failed: %v", err)
	}
	return models, ""
}

func cmdLaunch(ctx context.Context, agentName string, args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	agent, err := cfg.AgentFor(agentName)
	if err != nil {
		return err
	}
	name, profile, err := cfg.Profile(f.profile)
	if err != nil {
		return err
	}

	var available []proxy.Model
	var notice string
	if !f.noDiscover && agent.Kind == config.KindClaude {
		available, notice = discover(ctx, profile)
		if notice != "" && profile.Family != "" && !profile.HasExplicitModels() {
			fmt.Fprintf(os.Stderr, "cpa: warning: %s\n", notice)
		}
	}

	plan, err := launch.Build(cfg, agentName, name, f.rest, launch.Options{
		Available:             available,
		AllowSettingsConflict: f.allowSettingsConflict,
	})
	if err != nil {
		return err
	}
	if notice != "" {
		plan.Notices = append(plan.Notices, notice)
	}

	if f.dryRun {
		printPlan(plan)
		return nil
	}
	for _, n := range plan.Notices {
		fmt.Fprintf(os.Stderr, "cpa: note: %s\n", n)
	}
	return plan.Run()
}

func printPlan(plan *launch.Plan) {
	fmt.Printf("# cpa profile: %s (agent: %s)\n", plan.ProfileName, plan.Agent)
	if plan.Source != "" {
		fmt.Printf("# model mapping: %s\n", plan.Source)
	}
	slots := make([]string, 0, len(plan.Models))
	for slot, model := range plan.Models {
		slots = append(slots, fmt.Sprintf("%s=%s", slot, model))
	}
	sort.Strings(slots)
	if len(slots) > 0 {
		fmt.Printf("# models: %s\n", strings.Join(slots, " "))
	}
	for _, n := range plan.Notices {
		fmt.Printf("# note: %s\n", n)
	}
	fmt.Println("# environment:  (+ set  = already matches  - removed)")
	for _, c := range plan.EnvListing() {
		fmt.Printf("#   %s\n", c)
	}
	fmt.Println(plan.CommandLine())
	if len(plan.SettingsBlob) > 0 {
		fmt.Printf("#\n# --settings (%s):\n", plan.SettingsPath)
		var pretty interface{}
		if json.Unmarshal(plan.SettingsBlob, &pretty) == nil {
			if blob, err := launch.PrettyJSON(pretty, "# "); err == nil {
				fmt.Println(string(blob))
			}
		}
	}
}

func cmdModels(ctx context.Context, args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	name, profile, err := cfg.Profile(f.profile)
	if err != nil {
		return err
	}
	models, notice := discover(ctx, profile)
	if notice != "" {
		return fmt.Errorf("%s", notice)
	}
	mapping, source, _ := launch.MapModels(profile, models)

	if f.jsonOut {
		out := map[string]interface{}{
			"profile": name,
			"baseUrl": profile.BaseURL,
			"source":  string(source),
			"models":  models,
			"slots":   mapping,
		}
		blob, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(blob))
		return nil
	}

	fmt.Printf("profile %s -> %s  (%d models)\n", name, profile.BaseURL, len(models))
	// The slot mapping is Claude Code's; saying so beats printing arrows that
	// mean nothing to the agent this profile is actually for.
	if target := profile.EffectiveAgent(); target != "" {
		if a, err := cfg.AgentFor(target); err == nil && a.Kind != config.KindClaude {
			fmt.Printf("note: this profile is for agent %q (kind %s), which has no model slots; "+
				"the mapping below is Claude Code's and does not apply\n", target, a.Kind)
		}
	}
	fmt.Println()
	slotOf := map[string][]string{}
	for slot, model := range mapping {
		slotOf[model] = append(slotOf[model], slot)
	}
	for _, m := range models {
		label := m.ID
		if m.OwnedBy != "" {
			label += fmt.Sprintf("  [%s]", m.OwnedBy)
		}
		if slots := slotOf[m.ID]; len(slots) > 0 {
			sort.Strings(slots)
			label += "  -> " + strings.Join(slots, ",")
		}
		fmt.Printf("  %s\n", label)
	}
	fmt.Printf("\nmapping source: %s\n", source)
	return nil
}

func cmdDoctor(ctx context.Context, args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	fmt.Printf("settings: %s\n\n", cfg.Path)

	names := cfg.ProfileNames()
	if f.profile != "" {
		names = []string{f.profile}
	}
	if len(names) == 0 {
		fmt.Println("no profiles configured")
		return nil
	}

	failed := 0
	for _, n := range names {
		p := cfg.Profiles[n]
		if p == nil {
			fmt.Printf("  %-14s FAIL  unknown profile\n", n)
			failed++
			continue
		}
		switch {
		case p.BaseURL == "":
			fmt.Printf("  %-14s FAIL  no baseUrl\n", n)
			failed++
			continue
		}
		key, keyErr := p.ResolveAPIKey()
		if keyErr != nil {
			fmt.Printf("  %-14s FAIL  %v\n", n, keyErr)
			failed++
			continue
		}
		models, err := proxy.New(p.BaseURL, key).ListModels(ctx)
		if err != nil {
			fmt.Printf("  %-14s FAIL  %v\n", n, err)
			failed++
			continue
		}
		keyState := "no key"
		if key != "" {
			keyState = "key ok"
		}
		fmt.Printf("  %-14s OK    %s  %d models, %s\n", n, p.BaseURL, len(models), keyState)
	}
	fmt.Println()
	if failed > 0 {
		fmt.Printf("%d of %d profiles unreachable\n", failed, len(names))
		return nil
	}
	fmt.Printf("all %d profiles reachable\n", len(names))
	return nil
}
