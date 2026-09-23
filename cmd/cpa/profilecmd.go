package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/launch"
	"github.com/shichao-wang/cpa/internal/prompt"
	"github.com/shichao-wang/cpa/internal/proxy"
)

const profileHelp = `cpa profile - inspect and create launch profiles

USAGE
  cpa profile list [--json]     list the profiles cpa can see
  cpa profile create [flags]    create one; prompts on a terminal

CREATE FLAGS
  --name <name>          answer the name prompt up front
  --description <text>   ditto for the description
  --agent <name>         ditto for the agent the profile is for (default: claude)
  --base-url <url>       ditto for the gateway address
  --api-key <key>        ditto for the key ("env:NAME" / "cmd:..." also work)
  --family <family>      narrow the listed models to this family
  --model <model>        pin one model onto every slot
  --file <path>          write somewhere other than the user config
  --force                overwrite an existing profile without asking
  --no-discover          do not query the gateway

A created profile lands in $XDG_CONFIG_HOME/cpa/settings.json
(~/.config/cpa/settings.json); --file writes somewhere else instead. It
records the one agent it is for; launching it with an agent of another kind is
an error, since a profile's model slots and settings mean nothing to another.

With a terminal attached the fields are asked for interactively: first the
name, description, agent, gateway address and key, then — once the gateway has
been queried — which of its models serves each of the agent's own. Claude Code
is asked one row per slot, named by the model Claude Code itself resolves for
it and answered from the models the gateway actually advertises. A profile for
another agent is asked for a single model instead, since only Claude Code has
slots. The mapping is then written into the profile as claudeSettings
modelPicker rows, which is how Claude Code is told what those ids stand in
for; without them it calls every id it does not know unknown, and assumes a
200k window for it. Without a terminal (a pipe, a script, CI) there are no
prompts at all: every field comes from the flags above, and a missing required
one is an error.

The prompts are line edited: left/right move the cursor, home/end and
ctrl-a/ctrl-e jump to the ends, ctrl-w and ctrl-u erase, and ctrl-c abandons
the profile without writing anything.
`

// cmdProfile dispatches the `cpa profile` subcommands.
func cmdProfile(ctx context.Context, args []string) error {
	if len(args) == 0 {
		fmt.Print(profileHelp)
		return nil
	}
	switch args[0] {
	case "list":
		return cmdProfileList(args[1:])
	case "create":
		return cmdProfileCreate(ctx, args[1:])
	case "help", "-h", "--help":
		fmt.Print(profileHelp)
		return nil
	default:
		return fmt.Errorf("unknown subcommand %q; want `cpa profile list` or `cpa profile create`", args[0])
	}
}

// cmdProfileList prints the profiles the current settings resolve to.
func cmdProfileList(args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if len(f.rest) > 0 {
		return fmt.Errorf("unexpected argument %q; `cpa profile list` takes no positional arguments", f.rest[0])
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	names := cfg.ProfileNames()

	if f.jsonOut {
		blob, _ := json.MarshalIndent(cfg.Profiles, "", "  ")
		fmt.Println(string(blob))
		return nil
	}

	fmt.Printf("settings: %s\n", cfg.Path)
	if cfg.DefaultProfile != "" {
		fmt.Printf("default profile: %s\n", cfg.DefaultProfile)
	}
	fmt.Println()
	for _, n := range names {
		p := cfg.Profiles[n]
		marker := "  "
		if n == cfg.DefaultProfile {
			marker = "* "
		}
		desc := p.Description
		if desc == "" {
			desc = p.Family
		}
		// The agent column shows what the profile is bound to, and "-" for a
		// profile that says nothing about its downstream and so fits any.
		agent := p.EffectiveAgent()
		if agent == "" {
			agent = "-"
		}
		fmt.Printf("%s%-14s %-9s %-32s %s\n", marker, n, agent, p.BaseURL, desc)
	}
	return nil
}

// cmdProfileCreate builds one profile, then merges it into a settings file.
// It writes only through upsertProfileAt, so the blast radius is the target
// document and nothing else.
//
// With a terminal attached the fields are collected by prompts, and the model
// behind each Claude Code slot is chosen from what the gateway advertises.
// Without one — a pipe, a script, CI — the same fields come from flags and a
// missing required value is an error rather than a prompt.
func cmdProfileCreate(ctx context.Context, args []string) error {
	f, err := parseFlags(args)
	if err != nil {
		return err
	}
	if len(f.rest) > 0 {
		return fmt.Errorf("unexpected argument %q; `cpa profile create` takes no positional arguments", f.rest[0])
	}

	path := f.file
	if path == "" {
		path = config.UserConfigPath()
	}
	if path == "" {
		return fmt.Errorf("cannot determine where to write; pass --file <path>")
	}

	name := f.name
	p := &config.Profile{
		Agent:       f.agent,
		Description: f.description,
		BaseURL:     f.baseURL,
		APIKey:      f.apiKey,
		Family:      f.family,
		Model:       f.model,
	}
	// A profile belongs to one agent, so this is never left open. Claude Code
	// is the agent cpa is exercised against, which makes it the default rather
	// than a question with no answer.
	if p.Agent == "" {
		p.Agent = "claude"
	}

	pr, err := prompt.New(os.Stdin, os.Stdout)
	if errors.Is(err, prompt.ErrNotATerminal) {
		if !f.noDiscover && resolveKind(p.Agent) == config.KindClaude {
			previewMapping(ctx, p)
		}
		return commitProfile(ctx, path, name, p, f, nil)
	}
	if err != nil {
		return err
	}
	defer pr.Close()

	if err := promptBasics(pr, &name, p); err != nil {
		return err
	}
	if err := promptDownstream(ctx, pr, p, f.noDiscover); err != nil {
		return err
	}
	return commitProfile(ctx, path, name, p, f, pr)
}

// promptDownstream asks the questions that fit the agent. Only Claude Code has
// the four model slots, so a profile for another agent is asked for one
// catch-all model instead: opus/sonnet/haiku/fable would be asking for
// something that agent cannot read.
func promptDownstream(ctx context.Context, pr *prompt.Prompter, p *config.Profile, noDiscover bool) error {
	switch resolveKind(p.Agent) {
	case config.KindClaude:
		if noDiscover {
			return nil
		}
		return promptModels(ctx, pr, p)
	case config.KindOpenAI:
		model, err := pr.Input("Model (optional)", p.Model, nil)
		if err != nil {
			return aborted(err)
		}
		p.Model = model
	}
	return nil
}

// resolveKind answers what kind of agent a name is, which is what decides the
// questions `profile create` asks. The settings files come first, since that is
// what a launch resolves against; the two names cpa knows without any
// configuration come next. Anything else is generic, and gets asked nothing
// agent-specific.
func resolveKind(name string) config.Kind {
	if cfg, err := config.Load(); err == nil {
		if a, err := cfg.AgentFor(name); err == nil {
			return a.Kind
		}
	}
	a, err := (&config.Config{}).AgentFor(name)
	if err != nil {
		return config.KindGeneric
	}
	return a.Kind
}

// promptBasics collects the fields that do not depend on the gateway. The base
// URL arrives prefilled with the usual local gateway, so pressing enter
// through the form still produces a working profile.
func promptBasics(pr *prompt.Prompter, name *string, p *config.Profile) error {
	if p.BaseURL == "" {
		p.BaseURL = "http://127.0.0.1:8317"
	}

	picked, err := pr.Input("Profile name", *name, notBlank("a profile needs a name"))
	if err != nil {
		return aborted(err)
	}
	*name = picked

	// The key need not be typed: the env: and cmd: shorthands are resolved at
	// launch, which keeps the secret out of a file you might commit.
	if p.Description, err = pr.Input("Description (optional)", p.Description, nil); err != nil {
		return aborted(err)
	}
	// Asked before the gateway is queried, because it decides which of the
	// later questions apply at all.
	if p.Agent, err = pr.Input("Agent this profile is for (claude, codex, or a name from \"agents\")",
		p.Agent, notBlank("a profile needs an agent")); err != nil {
		return aborted(err)
	}
	if p.BaseURL, err = pr.Input("Gateway base URL", p.BaseURL, validBaseURL); err != nil {
		return aborted(err)
	}
	if p.APIKey, err = pr.Input("API key (optional; env:NAME and cmd:... also work)", p.APIKey, nil); err != nil {
		return aborted(err)
	}
	return nil
}

// promptModels asks which model on the gateway serves each model the agent
// itself resolves. Everything offered is something the server actually
// serves, so an answer cannot be typo'd into a model that does not exist.
func promptModels(ctx context.Context, pr *prompt.Prompter, p *config.Profile) error {
	available, note := discover(ctx, p)
	if note != "" {
		fmt.Fprintf(os.Stderr, "note: %s\n", note)
	}
	if len(available) == 0 {
		// No catalogue to choose from. A profile pinned to explicit models,
		// or one pointing at a gateway that is currently down, still has to
		// be creatable, so fall back to typing the values.
		var err error
		if p.Family, err = pr.Input("Upstream family (optional)", p.Family, nil); err != nil {
			return aborted(err)
		}
		if p.Model, err = pr.Input("Model for every slot (optional)", p.Model, nil); err != nil {
			return aborted(err)
		}
		return nil
	}

	// A family passed on the command line still narrows the catalogue. The
	// form asks no family question of its own: naming the model behind each
	// slot is what says which upstream the profile talks to.
	candidates := matching(available, p.Family)
	if len(candidates) == 0 {
		fmt.Fprintf(os.Stderr, "note: no advertised model matches family %q; leaving the slots unset\n", p.Family)
		return nil
	}
	return promptSlots(pr, p, candidates)
}

// promptSlots asks for the mapping one row at a time: the model the agent
// itself resolves on the left, the gateway model that should serve it on the
// right. Each row starts on the gateway's model for that same slot — the
// answer that needs no thought — so a profile that maps everything onto
// itself is four enters. A slot the gateway does not serve starts unset rather
// than starting wrong.
func promptSlots(pr *prompt.Prompter, p *config.Profile, candidates []proxy.Model) error {
	pinned := map[string]string{}
	for _, slot := range config.Slots {
		labels := []string{leaveUnset}
		values := []string{""}
		for _, m := range candidates {
			labels = append(labels, candidateLabel(m, slot))
			values = append(values, m.ID)
		}
		def := defaultSlotChoice(slot, candidates, p.Models[slot])

		picked, err := pr.ChooseDefault(slotLabel(slot), labels, def)
		if err != nil {
			return aborted(err)
		}
		if values[picked] != "" {
			pinned[slot] = values[picked]
		}
	}

	// A profile that pins nothing is the normal case; leaving the map empty
	// keeps it out of the written JSON.
	if len(pinned) > 0 {
		p.Models = pinned
	}
	return nil
}

// slotLabel names a row the way the model is known to whoever is answering: by
// the agent's own name for it, with the id that name resolves to.
func slotLabel(slot string) string {
	if id := config.ClaudeCodeDefaults[slot]; id != "" {
		return fmt.Sprintf("%s (Claude Code default: %s)", slot, id)
	}
	return slot
}

// candidateLabel marks a candidate the gateway itself files under another
// slot, so a catalogue of two dozen models does not have to be read in full to
// find the one that belongs here.
func candidateLabel(m proxy.Model, slot string) string {
	label := m.Label()
	if other := launch.Classify(m.ID); other != "" && other != slot {
		return label + " → " + other
	}
	return label
}

// defaultSlotChoice is a row's starting answer, as an index into the options
// promptSlots builds: 0 is the unset option, then the candidates in order. A
// pin already in the profile stays selected, so re-running create does not
// silently reset a choice that was made deliberately. Otherwise the gateway's
// own model for that slot is offered — by exact id, or failing that by the
// claude-<slot> prefix the launcher itself matches. A gateway serving no such
// model leaves the row unset.
func defaultSlotChoice(slot string, candidates []proxy.Model, current string) int {
	if current != "" {
		for i, m := range candidates {
			if m.ID == current {
				return i + 1
			}
		}
	}
	if id := config.ClaudeCodeDefaults[slot]; id != "" {
		for i, m := range candidates {
			if m.ID == id {
				return i + 1
			}
		}
	}
	want := "claude-" + strings.ToLower(slot)
	for i, m := range candidates {
		if strings.HasPrefix(strings.ToLower(m.ID), want) {
			return i + 1
		}
	}
	return 0
}

// commitProfile validates the collected profile, optionally confirms that an
// existing one may be replaced, and merges it into the settings file. It is
// the single write path for `cpa profile create`, so the flag-driven and the
// interactive flow report and refuse identically. A nil prompter means there
// is no terminal to ask on, which makes an existing profile an error.
func commitProfile(ctx context.Context, path, name string, p *config.Profile, f *flags, pr *prompt.Prompter) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("a profile needs a name; pass --name <name>")
	}
	p.BaseURL = strings.TrimSpace(p.BaseURL)
	if p.BaseURL == "" {
		return fmt.Errorf("a profile needs a base URL; pass --base-url <url>")
	}
	p.Agent = strings.TrimSpace(p.Agent)
	if p.Agent == "" {
		return fmt.Errorf("a profile needs the agent it is for; pass --agent <name>")
	}
	// The result of the write is only visible afterwards, so a typo would
	// otherwise land in the file as a "baseUrl" that is not a URL and only
	// surface later as a confusing launch failure.
	if err := validBaseURL(p.BaseURL); err != nil {
		return err
	}

	old, exists := existingProfile(path, name)
	if exists && !f.force {
		if pr == nil {
			return fmt.Errorf("profile %q already exists in %s; pass --force to overwrite it", name, path)
		}
		ok, err := pr.Confirm(
			fmt.Sprintf("Profile %q already exists in %s (currently %s). Overwrite it?", name, path, describeProfile(old)),
			"Overwrite", "Cancel")
		if err != nil {
			return aborted(err)
		}
		if !ok {
			return fmt.Errorf("aborted; %q left untouched in %s", name, path)
		}
	}

	// Every answer the prompt needed is in hand. Hand the terminal back
	// before writing and reporting: raw mode drops the carriage return from a
	// newline, so anything printed from here on would stair-step down the
	// screen. Close is idempotent, so the caller's defer still holds.
	if pr != nil {
		pr.Close()
	}

	// A model id that only the gateway serves is one Claude Code does not
	// know: it assumes a 200k window and warns at every launch that the id is
	// not in its catalogue. A modelPicker row carrying behavesAs tells it
	// which of its own models this one stands in for, and the slot mapping
	// just collected says exactly that — so the rows are derived rather than
	// asked for, and a profile that pinned nothing stays untouched.
	//
	// But not when the profile carries a contextWindow. behavesAs makes
	// Claude Code read the window off the model it names, and that reading
	// wins over CLAUDE_CODE_MAX_CONTEXT_TOKENS: measured, a profile asking
	// for 1M through contextWindow showed 200k once behavesAs rows were
	// added. Each knob silences the notice on its own, so the one that also
	// widens the window is the one that gets to stay.
	//
	// And only for a Claude Code profile: behavesAs is a settings key that
	// agent alone reads, so writing it onto a profile for another one would
	// change which agent the profile is even for.
	rows := []behavesAsRow(nil)
	if p.ContextWindow == 0 && resolveKind(p.Agent) == config.KindClaude {
		rows = behavesAsRows(p.Models)
	}
	if len(rows) > 0 {
		p.ClaudeSettings = withModelPicker(p.ClaudeSettings, rows)
	}

	replacing := ""
	if exists {
		replacing = fmt.Sprintf(" (was: %s)", describeProfile(old))
	}
	if err := upsertProfileAt(path, name, p); err != nil {
		return err
	}
	fmt.Printf("\nwrote profile %q to %s%s\n", name, path, replacing)
	for _, line := range behavesAsReport(rows) {
		fmt.Printf("  %s\n", line)
	}
	fmt.Printf("  cpa profile list\n  cpa %s --profile %s\n", p.Agent, name)
	return nil
}

// behavesAsRow is one entry of Claude Code's settings `modelPicker.options`:
// the id the profile sends, and the Claude model it stands in for. No label
// or description is written — cpa already names these models through
// ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME, and a label here would win over it.
type behavesAsRow struct {
	Model     string `json:"model"`
	BehavesAs string `json:"behavesAs"`
}

// behavesAsRows turns a slot mapping into the rows that stop Claude Code
// treating the mapped ids as unknown. Slots are walked in their own order —
// strongest first — so a gateway model serving both opus and sonnet is
// declared as the opus one: an id carries one behavesAs, and opus is the
// larger claim. A slot whose meaning cpa has no id for is left alone.
func behavesAsRows(models map[string]string) []behavesAsRow {
	if len(models) == 0 {
		return nil
	}
	claimed := map[string]bool{}
	var rows []behavesAsRow
	for _, slot := range config.Slots {
		id, target := models[slot], config.ClaudeCodeDefaults[slot]
		if id == "" || target == "" || claimed[id] || isAgentsOwnID(id) {
			continue
		}
		claimed[id] = true
		rows = append(rows, behavesAsRow{Model: id, BehavesAs: target})
	}
	return rows
}

// isAgentsOwnID reports whether an id is one of the agent's own. Claude Code
// is the authority on the ids named claude-*: it either knows one or has a
// reason not to, and a behavesAs row would only talk over that — claude-opus-5
// is not claude-opus-5-5, however the slot it fills is labelled. An id from
// anyone else's namespace is the case the row exists for, and pointing a slot
// at a gateway is what produces those.
func isAgentsOwnID(id string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(id)), "claude-")
}

// withModelPicker returns settings carrying the behavesAs rows, leaving any
// other Claude Code settings in place.
func withModelPicker(settings map[string]interface{}, rows []behavesAsRow) map[string]interface{} {
	if settings == nil {
		settings = map[string]interface{}{}
	}
	settings["modelPicker"] = map[string]interface{}{"options": rows}
	return settings
}

// behavesAsReport describes what was written, so the mapping Claude Code will
// apply is visible at the prompt rather than buried in the settings file.
func behavesAsReport(rows []behavesAsRow) []string {
	if len(rows) == 0 {
		return nil
	}
	parts := make([]string, len(rows))
	for i, r := range rows {
		parts[i] = fmt.Sprintf("%s behaves as %s", r.Model, r.BehavesAs)
	}
	return []string{
		"behavesAs: " + strings.Join(parts, ", "),
		"(Claude Code will no longer call those ids unknown; the 200k window it assumes",
		" is unchanged — set the profile's contextWindow if the upstream offers more)",
	}
}

// previewMapping asks the gateway what it advertises and prints the slot
// mapping the flags produce. A failure is a note, not an error: a profile
// with a pinned model launches fine with the gateway down. It is only used on
// the flag-driven path; the interactive one shows the same thing by asking.
func previewMapping(ctx context.Context, p *config.Profile) {
	available, note := discover(ctx, p)
	if note != "" {
		fmt.Printf("  note: %s\n", note)
	}
	models, source, notices := launch.MapModels(p, available)
	for _, n := range notices {
		fmt.Printf("  note: %s\n", n)
	}
	if len(models) == 0 {
		return
	}
	fmt.Printf("  gateway advertises %d model(s); slots would resolve as (%s):\n", len(available), source)
	for _, slot := range config.Slots {
		if m := models[slot]; m != "" {
			fmt.Printf("    %-7s -> %s\n", slot, m)
		}
	}
}

const (
	// leaveUnset is the first option of every slot row: pin nothing, and let
	// the launcher resolve that slot on its own — the gateway's Claude-shaped
	// model, then the profile's catch-all one.
	leaveUnset = "(leave unset — resolve automatically)"
)

// aborted turns the prompter's cancel key into the message the CLI reports.
// Nothing has been written when it fires.
func aborted(err error) error {
	if errors.Is(err, prompt.ErrInterrupted) || errors.Is(err, prompt.ErrEOF) {
		return fmt.Errorf("aborted at the prompt; nothing written")
	}
	return err
}

// notBlank builds the usual "this field is required" validator.
func notBlank(msg string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return errors.New(msg)
		}
		return nil
	}
}

// validBaseURL rejects an address that could not be dialled, at the prompt
// rather than at launch.
func validBaseURL(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("a profile needs a base URL")
	}
	if !strings.Contains(s, "://") {
		return fmt.Errorf("%q does not look like a URL; expected something like http://127.0.0.1:8317", s)
	}
	return nil
}

// matching applies the same substring rule the launcher uses, so the options
// shown at the prompt are exactly the models that will be used.
func matching(models []proxy.Model, family string) []proxy.Model {
	if family == "" {
		return models
	}
	family = strings.ToLower(family)
	var out []proxy.Model
	for _, m := range models {
		if strings.Contains(strings.ToLower(m.ID), family) {
			out = append(out, m)
		}
	}
	return out
}

// existingProfile reports whether the settings file at path already defines
// name, returning the profile so a caller can describe what it is replacing.
// An unreadable or malformed file reads as "nothing there", leaving the
// error to the write path where it can be reported properly.
func existingProfile(path, name string) (*config.Profile, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	cfg, err := config.Parse(data)
	if err != nil {
		return nil, false
	}
	p, ok := cfg.Profiles[name]
	if !ok {
		return nil, false
	}
	return p, true
}

// describeProfile summarises a profile in one line for prompts and reports.
func describeProfile(p *config.Profile) string {
	switch {
	case p == nil:
		return "nothing"
	case p.Model != "":
		return p.Model
	case len(p.Models) > 0:
		return fmt.Sprintf("%d pinned slot(s)", len(p.Models))
	case p.Family != "":
		return "family " + p.Family
	case p.BaseURL != "":
		return p.BaseURL
	default:
		return "an empty profile"
	}
}
