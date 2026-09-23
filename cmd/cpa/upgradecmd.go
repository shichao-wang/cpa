package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/shichao-wang/cpa/internal/upgrade"
)

// upgradeUsage is printed by `cpa upgrade --help` and on a bad argument.
//
// These flags are parsed here rather than by parseFlags on purpose: parseFlags
// hands anything it does not recognize to the agent, and --version is a flag
// agents claim for themselves, so a shared parser would quietly eat
// `cpa claude --version`. Upgrade's flags belong to upgrade alone.
const upgradeUsage = `cpa upgrade - replace this binary with a published release

USAGE
  cpa upgrade [--check] [--tag <tag>] [--force]

FLAGS
  --check        report what an upgrade would do and change nothing; the exit
                 status answers the question too: 0 up to date, 1 update available
  --tag <tag>    install a specific release tag, like CPA_VERSION in
                 install.sh, instead of the latest release
  --force        replace a binary that is not a release build (a source build
                 reports a version that says nothing about how it compares)

The release comes from https://github.com/` + upgrade.Repo + `/releases and is
checked against that release's checksums.txt. The new binary is staged beside
the old one and run once before the swap, so a download that does not work
never replaces a cpa that does. The file replaced is the running binary
itself, wherever it was installed.
`

func cmdUpgrade(ctx context.Context, args []string) error {
	var o upgrade.Options
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--check":
			o.Check = true
		case a == "--force":
			o.Force = true
		case a == "--tag":
			if i+1 >= len(args) {
				return errors.New("--tag needs a release tag, e.g. --tag v0.2.1")
			}
			i++
			o.Version = args[i]
		case strings.HasPrefix(a, "--tag="):
			o.Version = strings.TrimPrefix(a, "--tag=")
		case a == "help" || a == "-h" || a == "--help":
			fmt.Print(upgradeUsage)
			return nil
		default:
			return fmt.Errorf("unknown argument %q for `cpa upgrade`\n\n%s", a, upgradeUsage)
		}
	}

	o.Current = version
	o.Out = os.Stdout

	res, err := upgrade.Run(ctx, o)
	if err != nil {
		return err
	}
	// --check answers through its exit status as well as its output, so a
	// script can ask whether an update is waiting without parsing anything.
	if o.Check && res.Available {
		os.Exit(1)
	}
	return nil
}
