# cpa

**Profile-based launcher for coding agents behind a [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) gateway.**

```console
$ cpa claude --profile deepseek     # Claude Code, upstream all-DeepSeek
$ cpa claude --profile gpt          # Claude Code, upstream all-GPT
$ cpa claude --profile deepseek -p "explain this repo"
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
| `CPA_VERSION` | install a specific tag instead of the latest release, e.g. `CPA_VERSION=v0.1.0` |
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

## Quick start

```console
$ cpa init                     # writes ~/.config/cpa/settings.json
$ export CPA_API_KEY=sk-...    # whatever your gateway expects
$ cpa doctor                   # confirm each profile's endpoint answers
$ cpa claude --profile deepseek
```

`cpa profile create` walks you through a new profile: name, description, the
agent it is for, gateway URL and key, then — having asked the gateway what it
serves — the upstream family and the model behind each Claude Code slot. The
family and the slots are picked from an arrow-key list of the models the
gateway actually advertises, so a slot cannot be typo'd into a model that does
not exist. A profile for another agent is asked for a single model instead:
only Claude Code has slots.

```console
$ cpa profile create
? Profile name devbox
? Description (optional) devbox gateway
? Agent this profile is for (claude, codex, or a name from "agents") claude
? Gateway base URL http://127.0.0.1:18317
? API key (optional; env:NAME and cmd:... also work) env:CPA_KEY
? Upstream family (which models fill Claude Code's slots)
❯ (every advertised model — 4)
  deepseek — deepseek-chat, deepseek-flash[1m] +1 more
  (type a family…)
# choosing one collapses the list onto the answer, and the four slots follow:
? opus
❯ (follow the family — choose automatically)
? sonnet (follow the family — choose automatically)
? haiku (follow the family — choose automatically)
? fable (follow the family — choose automatically)

wrote profile "devbox" to ~/.config/cpa/settings.json
```

The prompts are line edited: left/right move the cursor, home/end and
ctrl-a/ctrl-e jump to the ends, ctrl-w and ctrl-u erase, ctrl-c abandons the
profile without writing anything. A value too long for one line scrolls
sideways rather than wrapping. A list with more options than fit scrolls too,
and says how many are off screen (`↑ 8 more`, `↓ 3 more`), so a long
catalogue never looks like a short one. The key prompt accepts `env:NAME` and
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
| `modelNames` | Override the label shown in Claude Code's model picker, per slot. |
| `subagentModel` | Model for subagents (`CLAUDE_CODE_SUBAGENT_MODEL`). |
| `contextWindow` | `CLAUDE_CODE_MAX_CONTEXT_TOKENS`. A `[1m]` model suffix implies `1000000`. |
| `customModelOption` | Surface one model as a hand-picked entry in the picker. |
| `env` | Extra environment variables; these win over generated ones. |
| `claudeSettings` | Extra Claude Code settings, merged into the `--settings` document. |
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
| 1 | `models` pins | Explicit always wins. |
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
| `cpa version` | Print the version. |

Flags: `--profile`, `--dry-run`, `--no-discover`, `--json`, `--name`,
`--agent`, `--file`, `--allow-settings-conflict`. Anything unrecognized is
forwarded to the agent, so `cpa claude --profile deepseek --resume` does what
you expect. `cpa profile create` additionally takes `--description`,
`--base-url`, `--api-key`, `--family`, `--model` and `--force`, which answer
its prompts without a terminal.

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
