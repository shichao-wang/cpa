package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
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
  --base-url <url>       ditto for the gateway address
  --api-key <key>        ditto for the key ("env:NAME" / "cmd:..." also work)
  --family <family>      ditto for the upstream family
  --model <model>        pin one model onto every slot
  --file <path>          write somewhere other than the user config
  --force                overwrite an existing profile without asking
  --no-discover          do not query the gateway

A created profile lands in $XDG_CONFIG_HOME/cpa/settings.json
(~/.config/cpa/settings.json); --file writes somewhere else instead.

With a terminal attached the fields are asked for interactively: first the
name, description, gateway address and key, then — once the gateway has been
queried — the upstream family and the model behind each Claude Code slot,
both chosen from the models the gateway actually advertises. Without a
terminal (a pipe, a script, CI) there are no prompts at all: every field
comes from the flags above, and a missing required one is an error.

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
		fmt.Printf("%s%-14s %-34s %s\n", marker, n, p.BaseURL, desc)
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
		Description: f.description,
		BaseURL:     f.baseURL,
		APIKey:      f.apiKey,
		Family:      f.family,
		Model:       f.model,
	}

	pr, err := prompt.New(os.Stdin, os.Stdout)
	if errors.Is(err, prompt.ErrNotATerminal) {
		if !f.noDiscover {
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
	if !f.noDiscover {
		if err := promptModels(ctx, pr, p); err != nil {
			return err
		}
	}
	return commitProfile(ctx, path, name, p, f, pr)
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
	if p.BaseURL, err = pr.Input("Gateway base URL", p.BaseURL, validBaseURL); err != nil {
		return aborted(err)
	}
	if p.APIKey, err = pr.Input("API key (optional; env:NAME and cmd:... also work)", p.APIKey, nil); err != nil {
		return aborted(err)
	}
	return nil
}

// promptModels asks which upstream the profile talks to, then which of the
// gateway's advertised models fills each Claude Code slot. Everything offered
// is something the server actually serves, so a slot cannot be typo'd into a
// model that does not exist.
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

	labels, values := familyChoices(available, p.Family)
	picked, err := pr.Choose("Upstream family", labels)
	if err != nil {
		return aborted(err)
	}
	switch family := values[picked]; family {
	case familyAll:
		p.Family = ""
	case familyCustom:
		custom, err := pr.Input("Family (matched against model ids)", "", nil)
		if err != nil {
			return aborted(err)
		}
		p.Family = custom
	default:
		p.Family = family
	}

	candidates := matching(available, p.Family)
	if len(candidates) == 0 {
		fmt.Fprintf(os.Stderr, "note: no advertised model matches family %q; leaving the slots unset\n", p.Family)
		return nil
	}
	return promptSlots(pr, p, candidates)
}

// promptSlots fills the four Claude Code slots from the models the chosen
// family matched. Following the family is the default for every slot: the
// gateway classifies the matched models onto the slots itself, and pinning is
// only worth doing when that guess is wrong.
func promptSlots(pr *prompt.Prompter, p *config.Profile, candidates []proxy.Model) error {
	pinned := map[string]string{}
	for _, slot := range config.Slots {
		labels := []string{followFamily}
		values := []string{""}
		for _, m := range candidates {
			labels = append(labels, m.Label())
			values = append(values, m.ID)
		}
		// An existing pin stays selected, so re-running create on a profile
		// does not silently reset choices that were made deliberately.
		def := 0
		for i, v := range values {
			if v != "" && v == p.Models[slot] {
				def = i
				break
			}
		}

		picked, err := pr.ChooseDefault(slot, labels, def)
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

	replacing := ""
	if exists {
		replacing = fmt.Sprintf(" (was: %s)", describeProfile(old))
	}
	if err := upsertProfileAt(path, name, p); err != nil {
		return err
	}
	fmt.Printf("\nwrote profile %q to %s%s\n", name, path, replacing)
	fmt.Printf("  cpa profile list\n  cpa claude --profile %s\n", name)
	return nil
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
	// familyAll and familyCustom are the two entries in the family picker
	// that are not families. Real families are read out of model ids, so
	// neither sentinel can collide with one.
	familyAll    = "\x00all"
	familyCustom = "\x00custom"
	// followFamily is the first option of every slot, and what most profiles
	// should keep.
	followFamily = "(follow the family — choose automatically)"
)

// familyChoices builds the family picker. Each family is labelled with the
// models it would select, since a family is a substring match and the name
// alone does not say what it catches.
func familyChoices(available []proxy.Model, current string) (labels, values []string) {
	labels = append(labels, fmt.Sprintf("(every advertised model — %d)", len(available)))
	values = append(values, familyAll)
	for _, fam := range familiesOf(available) {
		labels = append(labels, fmt.Sprintf("%s — %s", fam, summarise(matching(available, fam))))
		values = append(values, fam)
	}
	labels = append(labels, "(type a family…)")
	values = append(values, familyCustom)
	return labels, values
}

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

// familiesOf derives the family handles worth offering. A family is a
// substring match, so the useful handle for "deepseek-chat" is "deepseek";
// an id with no separator stands for itself.
func familiesOf(models []proxy.Model) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range models {
		fam := m.ID
		if i := strings.IndexAny(fam, "-_."); i > 0 {
			fam = fam[:i]
		}
		if fam == "" || seen[fam] {
			continue
		}
		seen[fam] = true
		out = append(out, fam)
	}
	sort.Strings(out)
	return out
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

// summarise names a few of the models a family matches, for an option label.
func summarise(models []proxy.Model) string {
	if len(models) == 0 {
		return "no models"
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	const shown = 2
	if len(ids) > shown {
		return fmt.Sprintf("%s +%d more", strings.Join(ids[:shown], ", "), len(ids)-shown)
	}
	return strings.Join(ids, ", ")
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
