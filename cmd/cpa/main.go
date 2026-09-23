// Command cpa launches coding agents against a CLIProxyAPI (CPA) gateway,
// selected by named profiles.
//
//	cpa --profile deepseek claude      # Claude Code, upstream all-DeepSeek
//	cpa -p gpt claude                  # Claude Code, upstream all-GPT
//
// Profiles live in cpa's own settings file, $XDG_CONFIG_HOME/cpa/settings.json
// (~/.config/cpa/settings.json by default). Launching hands the profile to the
// agent through its own child process -- environment plus a per-launch
// `--settings` document -- and never rewrites Claude Code's configuration, so
// the native `claude` command keeps working exactly as before.
package main

import (
	"context"
	"fmt"
	"os"
)

// version is overridable at build time:
//
//	go build -ldflags "-X main.version=$(git describe --tags)"
var version = "0.1.0"

const usage = `cpa - launch coding agents against a CLIProxyAPI (CPA) gateway

USAGE
  cpa [flags] <agent> [agent args]         launch an agent with a profile
  cpa models  [--profile <name>]           list models the gateway advertises
  cpa profile list                         list configured profiles
  cpa profile create                       create one interactively
  cpa doctor                               check every profile's endpoint
  cpa init [--force]                       write a starter settings file
  cpa import-claude [--name <name>]        turn ~/.claude/settings.json into a profile
  cpa upgrade [--check]                    update cpa from its GitHub releases
  cpa version                              print the version

EXAMPLES
  cpa --profile deepseek claude
  cpa -p gpt claude -p "explain this repo"
  cpa --profile deepseek --dry-run claude
  cpa models --profile deepseek
  cpa profile create

FLAGS
  --profile <name>, -p <name>  profile to launch (default: "defaultProfile")
  --dry-run          print the command and environment changes, launch nothing
  --no-discover      skip querying the gateway for its model catalogue
  --json             machine-readable output for "models" and "profile list"
  --force            overwrite an existing settings file ("init")
  --allow-settings-conflict
                     proceed even if you passed your own --settings
  --name <name>      profile name to create ("import-claude", "profile create")
  --agent <name>     agent the profile is for ("profile create", default: claude)
  --file <path>      settings file to write ("profile create")
  --check, --tag, --force
                     "cpa upgrade" only; see "cpa upgrade --help"

Launch flags belong before the agent name. Everything after it is passed to
that agent unchanged, including -p and --profile. For example:
  cpa -p deepseek claude -p "explain this repo"

CONFIGURATION
  Settings are read from $XDG_CONFIG_HOME/cpa/settings.json
  (~/.config/cpa/settings.json), merged with ./.cpa/settings.json and
  ./cpa.settings.json, nearest winning per profile. cpa writes only its own
  files; it never modifies Claude Code's settings.
`

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		fmt.Print(usage)
		os.Exit(2)
	}

	switch args[0] {
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	case "version", "-v", "--version":
		fmt.Printf("cpa %s\n", version)
		return
	}

	ctx := context.Background()
	var err error
	switch args[0] {
	case "models":
		err = cmdModels(ctx, args[1:])
	case "profile":
		err = cmdProfile(ctx, args[1:])
	case "profiles":
		// Renamed in v0.3.0. Without this the old spelling would be read as
		// an agent name and fail with a confusing "unknown agent" error.
		err = fmt.Errorf("`cpa profiles` is now `cpa profile list`")
	case "doctor":
		err = cmdDoctor(ctx, args[1:])
	case "init":
		err = cmdInit(args[1:])
	case "import-claude":
		err = cmdImportClaude(args[1:])
	case "upgrade":
		err = cmdUpgrade(ctx, args[1:])
	case "run":
		err = launchFromArgs(ctx, args[1:])
	default:
		err = launchFromArgs(ctx, args)
	}
	if err != nil {
		fatal(err.Error())
	}
}

func launchFromArgs(ctx context.Context, args []string) error {
	agent, f, err := parseLaunchArgs(args)
	if err != nil {
		return err
	}
	return cmdLaunch(ctx, agent, f)
}

func fatal(msg string) {
	fmt.Fprintf(os.Stderr, "cpa: %s\n", msg)
	os.Exit(1)
}
