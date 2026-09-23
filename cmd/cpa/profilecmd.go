package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/shichao-wang/cpa/internal/config"
	"github.com/shichao-wang/cpa/internal/launch"
)

const profileHelp = `cpa profile - inspect and create launch profiles

USAGE
  cpa profile list [--json]     list the profiles cpa can see
  cpa profile create [flags]    create one, prompting for each field

CREATE FLAGS
  --name <name>    skip the name prompt
  --file <path>    write somewhere other than the user config
  --force          overwrite an existing profile without asking
  --no-discover    do not query the gateway to preview the slot mapping

A created profile lands in $CPA_SETTINGS when that is set, else in
$XDG_CONFIG_HOME/cpa/settings.json (~/.config/cpa/settings.json); --file
overrides both. Every prompt accepts an empty line for its default, and
stdin may be piped.
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

// cmdProfileCreate builds one profile by asking for each field, then merges
// it into a settings file. It writes only through upsertProfileAt, so the
// blast radius is the target document and nothing else.
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
		path = config.WritePath()
	}
	if path == "" {
		return fmt.Errorf("cannot determine where to write; pass --file <path>")
	}

	in := bufio.NewReader(os.Stdin)

	name := f.name
	if name == "" {
		if name, err = ask(in, "Profile name", ""); err != nil {
			return err
		}
	}
	if name == "" {
		return fmt.Errorf("a profile needs a name")
	}
	if _, exists := existingProfile(path, name); exists && !f.force {
		answer, err := ask(in, fmt.Sprintf("Profile %q already exists in %s; overwrite it?", name, path), "no")
		if err != nil {
			return err
		}
		if !isYes(answer) {
			return fmt.Errorf("aborted; %q left untouched in %s", name, path)
		}
	}

	p := &config.Profile{}
	if p.BaseURL, err = ask(in, "Gateway base URL", "http://127.0.0.1:8317"); err != nil {
		return err
	}
	// The prompts are blind and the result is only visible afterwards, so a
	// typo would otherwise land in the file as a "baseUrl" that is not a URL
	// and only surface as a confusing launch failure.
	if p.BaseURL == "" {
		return fmt.Errorf("a profile needs a base URL")
	}
	if !strings.Contains(p.BaseURL, "://") {
		return fmt.Errorf("baseUrl %q does not look like a URL; expected something like http://127.0.0.1:8317", p.BaseURL)
	}

	// The secret itself need not be typed: the env: and cmd: shorthands are
	// resolved at launch, which keeps the key out of a file you might commit.
	p.APIKey, err = ask(in, `API key (or "env:NAME" / "cmd:..." to resolve at launch)`, "")
	if err != nil {
		return err
	}
	if p.Description, err = ask(in, "Description (optional)", ""); err != nil {
		return err
	}
	if p.Family, err = ask(in, "Upstream family, e.g. deepseek (optional)", ""); err != nil {
		return err
	}
	if p.Model, err = ask(in, "Model for every slot, e.g. deepseek-flash[1m] (optional)", ""); err != nil {
		return err
	}

	if !f.noDiscover {
		previewMapping(ctx, p)
	}

	replacing := ""
	if old, exists := existingProfile(path, name); exists {
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
// mapping this profile would produce. A failure is a note, not an error: a
// profile with a pinned model launches fine with the gateway down. The user
// has not been asked to confirm anything yet, so this only reports.
func previewMapping(ctx context.Context, p *config.Profile) {
	fmt.Println()
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
	case p.Family != "":
		return "family " + p.Family
	case p.BaseURL != "":
		return p.BaseURL
	default:
		return "an empty profile"
	}
}

// ask prompts for one value and returns what was typed. An empty line takes
// the default. A stream that ends without a newline still yields its last
// line, so the command is usable from a pipe.
func ask(in *bufio.Reader, label, def string) (string, error) {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, err := in.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	if line = strings.TrimSpace(line); line == "" {
		return def, nil
	}
	return line, nil
}

func isYes(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes":
		return true
	}
	return false
}
