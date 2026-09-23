// Command cpa launches coding agents against a CLIProxyAPI (CPA) gateway,
// selected by named profiles.
//
//	cpa claude --profile deepseek      # Claude Code, upstream all-DeepSeek
//	cpa claude --profile gpt           # Claude Code, upstream all-GPT
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
  cpa <agent> [flags] [-- <agent args>]    launch an agent with a profile
  cpa models  [--profile <name>]           list models the gateway advertises
  cpa profile list                         list configured profiles
  cpa profile create                       create one interactively
  cpa doctor                               check every profile's endpoint
  cpa init [--force]                       write a starter settings file
  cpa import-claude [--name <name>]        turn ~/.claude/settings.json into a profile
  cpa upgrade [--check]                    update cpa from its GitHub releases
  cpa version                              print the version

EXAMPLES
  cpa claude --profile deepseek
  cpa claude --profile gpt -p "explain this repo"
  cpa claude --profile deepseek --dry-run
  cpa models --profile deepseek
  cpa profile create

FLAGS
  --profile <name>   profile to launch (default: "defaultProfile")
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

Unrecognized arguments are passed straight through to the agent, so
` + "`cpa claude --profile deepseek --resume`" + ` works as you would expect.

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
		if len(args) < 2 {
			fatal("run needs an agent name, e.g. `cpa run claude --profile deepseek`")
		}
		err = cmdLaunch(ctx, args[1], args[2:])
	default:
		err = cmdLaunch(ctx, args[0], args[1:])
	}
	if err != nil {
		fatal(err.Error())
	}
}

func fatal(msg string) {
	fmt.Fprintf(os.Stderr, "cpa: %s\n", msg)
	os.Exit(1)
}
