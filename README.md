# cpa

**Profile-based launcher for coding agents behind a [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) gateway.**

```console
$ cpa claude --profile deepseek     # Claude Code, upstream all-DeepSeek
$ cpa claude --profile gpt          # Claude Code, upstream all-GPT
$ cpa claude -p deepseek            # -p is an alias for --profile
$ cpa claude --profile deepseek -- -p "explain this repo"
```

One gateway, many upstreams. `cpa` picks the upstream per invocation from
named profiles — and **never rewrites your Claude Code configuration**, so the
native `claude` command keeps working exactly as before.

[中文说明](README.zh-CN.md)

---

## Why this exists

A CPA gateway typically fronts several upstreams at once, and which one you
want depends on the task. The usual ways to switch have a cost:

| Approach | Cost |
|---|---|
| Edit `~/.claude/settings.json` by hand | Global, sticky, easy to forget which profile is live |
| GUI switchers that rewrite `settings.json` | Same, plus they touch a file other tools own |
| One shell alias per profile | Every `ANTHROPIC_*` variable, duplicated per alias |

`cpa` computes the launch environment on the fly and hands it to the agent as
a child process. Nothing global changes; when the session ends, nothing needs
cleaning up.

## The non-obvious part

Injecting environment variables — the obvious implementation — **does not
work** when your `~/.claude/settings.json` has an `env` block.

Claude Code applies `settings.json`'s `env` *on top of* the inherited process
environment. So this silently goes to the wrong upstream:

```console
$ ANTHROPIC_BASE_URL=http://127.0.0.1:9999 claude -p ok
ok                      # ← the settings.json endpoint was used, not yours
```

`cpa` therefore supplies the profile through `claude --settings <file>`,
which outranks `settings.json`. Verified against Claude Code 2.1.280:

| How the profile reaches Claude Code | Wins over `settings.json`? |
|---|---|
| Process environment variables | **No** — silently overridden |
| `--settings '{"env":{…}}'` (inline JSON) | Yes |
| `--settings <path>` (file) | Yes |

`cpa` uses the file form, so the API key stays out of `ps` output. This is the
single design decision the rest of the tool is shaped around, and
`TestBuildCarriesEnvThroughSettingsDocument` pins it down so it cannot regress
back into plain env injection.

## Non-invasiveness, precisely

`cpa` guarantees:

- **Never writes** `~/.claude/settings.json`, `~/.claude.json`, or anything
  else Claude Code owns. It only ever reads them.
- Settings are read from its own file, `$XDG_CONFIG_HOME/cpa/settings.json`
  (`~/.config/cpa/settings.json` by default — see
  [Configuration](#configuration)).
- The only thing it writes is a per-launch settings document, mode `0600`
  inside a `0700` directory, under `$XDG_RUNTIME_DIR/cpa/run/` when that is
  set and `~/.local/state/cpa/run/` otherwise. Documents older than a day are
  swept on the next launch.
- The native `claude` command is untouched, and a plain `claude` started
  outside `cpa` behaves exactly as it did before.
- `--dry-run` prints the full command, the complete environment, and the exact
  settings document, without launching anything.

## Install

```console
$ curl -fsSL https://raw.githubusercontent.com/shichao-wang/cpa/main/install.sh | bash
```

The script detects your OS and CPU, downloads the matching release archive,
verifies its SHA-256 against the release's `checksums.txt`, and installs the
binary to `~/.local/bin`. No root required, and nothing under `~/.claude/` is
read or written. It can be steered with environment variables:

| Variable | Effect |
|---|---|
| `CPA_VERSION` | install a specific tag instead of the latest release, e.g. `CPA_VERSION=v2026.09.23-a1b2c3` |
| `CPA_INSTALL_DIR` | install somewhere other than `~/.local/bin` |
| `CPA_SKIP_VERIFY=1` | skip checksum verification (not advised) |

Read it before piping — it is short and lives in the repo root as
[`install.sh`](install.sh). To pin the installer itself to a release rather
than tracking `main`, fetch the copy attached to that release:

```console
$ curl -fsSL https://github.com/shichao-wang/cpa/releases/latest/download/install.sh | bash
```

Or install from source:

```console
# with a Go toolchain
$ go install github.com/shichao-wang/cpa/cmd/cpa@latest

# or from a clone
$ git clone https://github.com/shichao-wang/cpa && cd cpa
$ make install
```

### Updating

`cpa upgrade` replaces the running binary with the newest release:

```console
$ cpa upgrade
downloading cpa_2026.09.23-a1b2c3_darwin_arm64.tar.gz
checksum ok
installed /Users/you/.local/bin/cpa (v2026.09.22-ff00aa -> v2026.09.23-a1b2c3)

$ cpa upgrade --check        # report only; exits 1 when an update is waiting
$ cpa upgrade --tag v2026.09.22-ff00aa   # a specific release, like CPA_VERSION in install.sh
```

The newest tag comes from `/releases/latest` (a redirect, not an API call, so no
rate limit), the archive is checked against that release's `checksums.txt`, and
the new binary is run once before it is renamed over the file `cpa` is running
from — so a download that does not work never replaces one that does. The file
replaced is whatever path this `cpa` was installed at.

A build from source reports a `git describe` version (`v2026.09.23-a1b2c3-11-gb7fc3aa`),
which says nothing about whether a release is newer, so `cpa upgrade` refuses to
guess; `--force` installs the release anyway. Only the first upgrade needs it —
after that the binary is a release build and compares properly.

Installing for the first time is still the installer's job:

```console
# release install
$ curl -fsSL https://raw.githubusercontent.com/shichao-wang/cpa/main/install.sh | bash

# source install: fast-forward your clone, then rebuild and reinstall
$ git pull --ff-only && make install
```

Every merge to `main` runs the tests in
[`.github/workflows/ci.yml`](.github/workflows/ci.yml) and, when they pass,
tags a release — so `cpa upgrade` gets you the newest code. A merge is tagged
`v<date>-<commit>`, the UTC date plus the first six characters of the merge
commit (`v2026.09.23-a1b2c3`), so a tag names exactly the tree it was built
from, and two merges in one day still get distinct tags. Label a PR
`skip-release` to publish nothing for it.

Both routes write the same file, `~/.local/bin/cpa`: `make install` defaults
to `PREFIX=$(HOME)/.local`, which is the path `install.sh` calls
`CPA_INSTALL_DIR`:

```console
$ make install PREFIX=/usr/local
```

## Quick start

```console
$ cpa init                     # writes ~/.config/cpa/settings.json
$ export CPA_API_KEY=sk-...    # whatever your gateway expects
$ cpa doctor                   # confirm each profile's endpoint answers
$ cpa claude --profile deepseek
```

`cpa profile create` walks you through a new profile: name, description, the
agent it is for, gateway URL and key, then — having asked the gateway what it
serves — the mapping from what the agent itself asks for onto what the gateway
has. For Claude Code that is one row per slot, read from the agent's side: the
row is named by the model Claude Code resolves for that slot, and the answer is
the model on your gateway that should serve it. Every answer is picked from an
arrow-key list of the models the gateway actually advertises, so it cannot be
typo'd into a model that does not exist, and each row starts on the gateway's
model for that same slot — mapping everything onto itself is four enters. In
model lists, type to filter by model ID, name, or provider (case-insensitively),
use backspace to edit the search and arrow keys to choose; Esc returns to the
previous question, and enter does nothing if no models match. A
candidate the gateway would file under another slot says so (`→ haiku`), which
is what keeps a two-dozen-model catalogue readable. Each row also names the
provider the gateway files the model under (`[commandcode]`), which is what a
model resold under a name of its own cannot say for itself. A profile for
another agent is asked for a single model instead: only Claude Code has slots.

```console
$ cpa profile create
✓ Profile name devbox
✓ Description (optional) devbox gateway
✓ Agent this profile is for (claude, codex, or a name from "agents") claude
✓ Gateway base URL http://127.0.0.1:18317
✓ API key (optional; env:NAME and cmd:... also work) env:CPA_KEY
Model configuration
    ? opus (Claude Code default: claude-opus-5-5)
      (leave unset — resolve automatically)
      claude-haiku-4-5 (Haiku 4.5)  [anthropic] → haiku
    ❯ claude-opus-5 (Opus 5)  [anthropic]
      claude-sonnet-5 (Sonnet 5)  [anthropic] → sonnet
      deepseek-v4-flash  [commandcode] → haiku
      deepseek-v4-pro  [commandcode]
      gpt-6-sol  [openai]
# after answering the four slot questions:
    ✓ opus (Claude Code default: claude-opus-5-5) claude-opus-5 (Opus 5)  [anthropic]
    ✓ sonnet (Claude Code default: claude-sonnet-5) gpt-6-sol  [openai]
    ✓ haiku (Claude Code default: claude-haiku-4-5) claude-haiku-4-5 (Haiku 4.5)  [anthropic]
    ✓ fable (Claude Code default: claude-fable-5-1) (leave unset — resolve automatically)

wrote profile "devbox" to ~/.config/cpa/settings.json
  behavesAs: gpt-6-sol behaves as claude-sonnet-5
  (Claude Code will no longer call those ids unknown; the 200k window it assumes
   is unchanged — set the profile's contextWindow if the upstream offers more)
  cpa profile list
  cpa claude --profile devbox
```

The ids the rows are named by are Claude Code's own alias table, built into cpa
(2.1.280: `opus` → `claude-opus-5-5`, `sonnet` → `claude-sonnet-5`, `haiku` →
`claude-haiku-4-5`, `fable` → `claude-fable-5-1`). The gateway need not serve
them: the row is labelled by what Claude Code *means*, so the choice reads as
"Claude Code's opus becomes this". A row left unset pins nothing and lets the
launcher resolve that slot on its own.

Those answers also decide what Claude Code's model picker offers, and that is
one decision rather than two. `create` writes a `claudeSettings.modelPicker`
carrying `replaceBuiltInOptions` and one row per model the profile can route
to, so `/model` lists Default and those rows instead of the built-in lineup
and every model advertised by the gateway through `/v1/models`. The picker
only offers the models mapped by this profile. A row also carries `behavesAs` for an id Claude Code
has never heard of: without it, Claude Code calls the id unknown at every
launch and assumes 200k tokens for it, while a row names the Claude model the
id fills in for, which the slot mapping already said. A slot pinned to Claude
Code's own id gets its row but no claim — that namespace belongs to the agent,
and a row saying `claude-opus-5` behaves as `claude-opus-5-5` would be cpa
talking over it. A model serving two slots is listed once, as the stronger
one, because an id carries one `behavesAs` and opus is the larger claim. Since
the rows are the whole list, a `label` is written only where `modelNames`
asked for one: the picker otherwise names each row from Claude Code's own
catalogue, which is the better name when there is one.

One trade-off comes with it. `behavesAs` makes Claude Code read the window off
the model a row names, and that reading outranks
`CLAUDE_CODE_MAX_CONTEXT_TOKENS`: measured, a profile asking for 1M through
`contextWindow` believes 200k once the claim is added. Each knob silences the
warning on its own, so the one that also widens the window is the one that gets
to stay — a profile with a `contextWindow` gets its rows without any
`behavesAs`, and one carrying `behavesAs` rows by hand next to a
`contextWindow` is told at launch which of the two it will get. A `[1m]`
suffix sidesteps the choice: Claude Code reads it off the id.

Existing profiles without a `modelPicker` are covered too: at launch, cpa
builds the same shortlist from the resolved slot mapping (including `family`
matches), a catch-all `model`, and `customModelOption`. A manually configured
`modelPicker` in either default or profile `claudeSettings` is left untouched;
cpa does not overwrite the user's choice.

The prompts are line edited: left/right move the cursor, home/end and
ctrl-a/ctrl-e jump to the ends, ctrl-w and ctrl-u erase, Esc returns to the
previous question, and Ctrl-C cancels without writing anything. A value too
long for one line scrolls sideways rather than wrapping. A list with more
options than fit scrolls too, and says how many are off screen
(`↑ 8 more`, `↓ 3 more`), so a long catalogue never looks like a short one.
The key prompt accepts `env:NAME` and
`cmd:...`, which resolve at launch and keep the secret out of the file.

Without a terminal — a pipe, a script, CI — there are no prompts at all:
every field comes from a flag (`--name`, `--agent`, `--base-url`, `--api-key`,
`--family`, `--model`, …) and a missing required one is an error rather than
a hang. Passing `--name` and `--base-url` is enough to create a profile
non-interactively; without `--agent` it is a Claude Code profile.

It writes to `$XDG_CONFIG_HOME/cpa/settings.json`; `--file` writes somewhere
else instead.

Point a profile at an existing setup instead of starting from scratch — this
reads your Claude Code settings and turns its `ANTHROPIC_*` environment into a
profile (it does not modify that file):

```console
$ cpa import-claude --name mygateway
```

## Configuration

`cpa` reads, in increasing order of precedence:

1. `$XDG_CONFIG_HOME/cpa/settings.json`, or `~/.config/cpa/settings.json`
   when `XDG_CONFIG_HOME` is unset
2. `./.cpa/settings.json` and `./cpa.settings.json` (project-local)

Earlier versions used `~/.cpa/settings.json`. That path is **no longer read**;
move the file to the location above.

Files are **plain JSON** — no comments, no trailing commas. What each field
means lives in [`examples/settings.schema.json`](examples/settings.schema.json),
which every generated file points at through `$schema`, so editors validate
and autocomplete as you type. (Earlier versions accepted JSONC; a file that
still has comments is rejected with an error saying so.)

```json
{
  "defaultProfile": "deepseek",
  "defaults": {
    "env": { "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "90" }
  },
  "profiles": {
    "deepseek": {
      "description": "all traffic to DeepSeek",
      "baseUrl": "http://127.0.0.1:8317",
      "apiKeyEnv": "CPA_API_KEY",
      "family": "deepseek",
      "subagentModel": "deepseek-flash"
    }
  }
}
```

### Profile fields

The table below is a summary. The authoritative description of every field —
including the ones not listed here — is
[`examples/settings.schema.json`](examples/settings.schema.json), which is what
your editor reads.

| Field | Meaning |
|---|---|
| `agent` | The one agent this profile is for. See [one profile, one agent](#one-profile-one-agent). |
| `baseUrl` | The gateway endpoint. Claude Code gets it as `ANTHROPIC_BASE_URL`. |
| `apiKey` | Client key. Also accepts `"env:VAR"` and `"cmd:shell command"`. |
| `apiKeyEnv` | Read the key from this environment variable. |
| `apiKeyCmd` | Read the key from this command's stdout. |
| `family` | Substring match against the gateway's `/v1/models` listing. It fills Claude Code's slots, so it also makes the profile a Claude Code one. |
| `model` | Catch-all model for every slot not otherwise set. |
| `models` | Pin slots by hand: `{"opus": …, "sonnet": …, "haiku": …, "fable": …}`. Pinning skips discovery entirely. |
| `modelNames` | Override the label shown in Claude Code's model picker, per slot; `create` writes it into the row's `label`. |
| `subagentModel` | Model for subagents (`CLAUDE_CODE_SUBAGENT_MODEL`). |
| `contextWindow` | `CLAUDE_CODE_MAX_CONTEXT_TOKENS`. A `[1m]` model suffix implies `1000000`. `behavesAs` outranks it — see above. |
| `customModelOption` | Surface one model as a hand-picked entry in the picker. |
| `env` | Extra environment variables; these win over generated ones. |
| `claudeSettings` | Extra Claude Code settings, merged into the `--settings` document. `cpa profile create` writes the derived `modelPicker` here: one row per mapped model, with `replaceBuiltInOptions`. |
| `args` | Extra arguments for the agent. |

Keep keys out of the file where you can — `apiKeyEnv` and `apiKeyCmd` exist so
a settings file stays safe to sync or commit.

You can also define other agents:

```json
{
  "agents": {
    "claude": { "bin": "claude", "kind": "claude" },
    "codex":  { "bin": "codex",  "kind": "openai" }
  }
}
```

`kind` decides the variables injected: `claude` → `ANTHROPIC_*`, `openai` →
`OPENAI_*`, `generic` → only the profile's own `env`.

### One profile, one agent

A profile says how one downstream application is pointed at one upstream. Its
model slots and its settings belong to that application and mean nothing to
another, so a profile fits a single agent — and launching it with an agent of a
different kind is an error rather than a silent misapplication:

```console
$ cpa codex --profile deepseek
cpa: profile "deepseek" is for agent "claude" (kind "claude"); "codex" is kind "openai"
a profile is written for one downstream application — its model slots and its settings mean nothing to another — so cpa will not apply it here.
launch it with an agent of kind "claude", or move the profile over with "agent": "codex"
```

Two agents of the same `kind` may share a profile: they read the same
variables, so nothing is lost. An application that needs its own upstream gets
its own profile — `examples/settings.json` has a `codex` profile next to the
Claude Code ones.

| What the profile says | Which agent it fits |
|---|---|
| `"agent": "codex"` | `codex`. Declaring always wins. |
| no `agent`, but any of `family`, `models`, `modelNames`, `subagentModel`, `customModelOption`, `contextWindow`, `claudeSettings` | `claude`: only Claude Code understands those fields, so a profile using them is a Claude Code profile — including one written before this rule existed. |
| no `agent`, none of those fields | Any agent. `cpa profile list` prints `-` for it, so an unbound profile is visible rather than assumed. |

## How a profile becomes a model mapping

Claude Code addresses upstream models through slots (opus / sonnet / haiku /
fable). `cpa` fills them in this order, stopping at the first rule that
produces something:

| # | Rule | Notes |
|---|---|---|
| 1 | `models` pins | Explicit always wins. A pin fills its own slot and the rules below fill the rest; a profile that pins only some slots still reports `explicit`, because nothing but the pins decided its mapping. |
| 2 | `family` matches exactly one advertised model | The "*all* traffic goes to DeepSeek" case — one model fills every slot. |
| 3 | `family` matches several | Bucketed by name: `flash`/`mini`/`nano` → haiku, `pro`/`max`/`ultra` → opus, `sonnet`/`medium` → sonnet. Unmatched slots get the strongest match. |
| 4 | Four `claude-<slot>-*` entries exist | Gateways that alias the upstream onto Claude-shaped names. |
| 5 | `model` | Catch-all. |
| 6 | Exactly one model advertised | Used for everything. |
| 7 | Nothing | Left unset, with a warning on stderr. |

Rules 3 and beyond are heuristics on purpose. `cpa models --profile X` always
prints what was chosen and which rule decided it, and an explicit `models` map
overrides all of it:

```console
$ cpa models --profile deepseek
profile deepseek -> http://127.0.0.1:8317  (4 models)

  claude-haiku-4-5   [anthropic]  -> haiku
  claude-opus-5      [anthropic]  -> opus
  claude-sonnet-5    [anthropic]  -> sonnet

mapping source: claude aliases
```

A row is named the way the picker names it — the id, the display name when the
gateway provides one, and the provider it files the model under — followed by
the slots that model serves.

## Commands

| Command | Purpose |
|---|---|
| `cpa <agent> [flags] [-- args]` | Launch an agent with a profile. |
| `cpa models --profile X` | List the gateway's models and the slot mapping. |
| `cpa profile list [--json]` | List configured profiles. |
| `cpa profile create` | Add one: prompts on a terminal, flags otherwise. |
| `cpa doctor` | Check every profile's endpoint and key. |
| `cpa init [--force]` | Write a starter settings file. |
| `cpa import-claude` | Turn `~/.claude/settings.json`'s env into a profile. |
| `cpa upgrade [--check]` | Update cpa from its GitHub releases. |
| `cpa version` | Print the version. |

Flags: `--profile` (or `-p`), `--dry-run`, `--no-discover`, `--json`, `--name`,
`--agent`, `--file`, `--allow-settings-conflict`. Anything unrecognized is
forwarded to the agent, so `cpa claude --profile deepseek --resume` does what
you expect. Use `--` before agent arguments when needed; Claude Code's own `-p`
prompt flag must follow it. For example:
`cpa claude --profile deepseek -- -p "explain this repo"`.
`cpa profile create` additionally takes `--description`, `--base-url`,
`--api-key`, `--family`, `--model` and `--force`, which answer its prompts
without a terminal. `--family` remains available as a fallback for any slot
left unpinned; the interactive flow configures Claude Code's slots directly.

## Troubleshooting

**`unrecognized_model` from the gateway.** The model Claude Code asked for is
not one the gateway offers. Run `cpa models --profile X` and pin `models` to
names the gateway actually advertises.

**Discovery fails but the launch works.** That is by design: a profile with
hand-pinned `models` needs no discovery. `--no-discover` skips the query
entirely for offline use.

**`profile "X" is for agent "Y"`, and I asked for another.** A profile belongs
to one agent — see [one profile, one agent](#one-profile-one-agent). Launch it
with that agent, or set the profile's `agent` field to the one you meant. `cpa
profile list` shows what each profile is bound to, and `-` for a profile that
fits any agent.

**My global `ANTHROPIC_CUSTOM_MODEL_OPTION` still shows up.** `cpa` overrides
the variables it manages; anything else in your global `env` block — including
picker entries — is left alone. Set `customModelOption` on the profile to take
that one over too.

**Two different `--settings`.** `cpa` supplies `--settings` itself, so passing
your own is an error rather than a coin flip. Move those settings into the
profile's `claudeSettings`, or pass `--allow-settings-conflict` to let cpa's
document come last.

## Status

Early. The `claude` agent is the one that is exercised; `openai` and `generic`
kinds inject the corresponding variables but have not been driven against a
real Codex/Gemini CLI yet.

## License

MIT
