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
- Settings are read from its own file: `~/.cpa/settings.json` (see
  [Configuration](#configuration)).
- The only thing it writes is a per-launch settings document at
  `~/.cpa/run/settings-<pid>.json`, mode `0600` inside a `0700` directory.
  Documents older than a day are swept on the next launch.
- The native `claude` command is untouched, and a plain `claude` started
  outside `cpa` behaves exactly as it did before.
- `--dry-run` prints the full command, the complete environment, and the exact
  settings document, without launching anything.

## Install

```console
# with a Go toolchain
$ go install github.com/shichao-wang/cpa/cmd/cpa@latest

# or from a clone
$ git clone https://github.com/shichao-wang/cpa && cd cpa
$ make install
```

## Quick start

```console
$ cpa init                     # writes ~/.cpa/settings.json
$ export CPA_API_KEY=sk-...    # whatever your gateway expects
$ cpa doctor                   # confirm each profile's endpoint answers
$ cpa claude --profile deepseek
```

Point a profile at an existing setup instead of starting from scratch — this
reads your Claude Code settings and turns its `ANTHROPIC_*` environment into a
profile (it does not modify that file):

```console
$ cpa import-claude --name mygateway
```

## Configuration

`cpa` reads, in increasing order of precedence:

1. `~/.cpa/settings.json`
2. `$XDG_CONFIG_HOME/cpa/settings.json`
3. `./.cpa/settings.json` and `./cpa.settings.json` (project-local)

`$CPA_SETTINGS=/path/to/file.json` pins an exact file. Files are JSONC —
comments and trailing commas are allowed.

```jsonc
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

| Field | Meaning |
|---|---|
| `baseUrl` | The gateway endpoint. Claude Code gets it as `ANTHROPIC_BASE_URL`. |
| `apiKey` | Client key. Also accepts `"env:VAR"` and `"cmd:shell command"`. |
| `apiKeyEnv` | Read the key from this environment variable. |
| `apiKeyCmd` | Read the key from this command's stdout. |
| `family` | Substring match against the gateway's `/v1/models` listing. |
| `model` | Catch-all model for every slot not otherwise set. |
| `models` | Pin slots by hand: `{"opus": …, "sonnet": …, "haiku": …, "fable": …}`. |
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

```jsonc
{
  "agents": {
    "claude": { "bin": "claude", "kind": "claude" },
    "codex":  { "bin": "codex",  "kind": "openai" }
  }
}
```

`kind` decides the variables injected: `claude` → `ANTHROPIC_*`, `openai` →
`OPENAI_*`, `generic` → only the profile's own `env`.

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
| `cpa profiles` | List configured profiles. |
| `cpa doctor` | Check every profile's endpoint and key. |
| `cpa init [--force]` | Write a starter settings file. |
| `cpa import-claude` | Turn `~/.claude/settings.json`'s env into a profile. |
| `cpa version` | Print the version. |

Flags: `--profile`, `--dry-run`, `--no-discover`, `--json`,
`--allow-settings-conflict`. Anything unrecognized is forwarded to the agent,
so `cpa claude --profile deepseek --resume` does what you expect.

## Troubleshooting

**`unrecognized_model` from the gateway.** The model Claude Code asked for is
not one the gateway offers. Run `cpa models --profile X` and pin `models` to
names the gateway actually advertises.

**Discovery fails but the launch works.** That is by design: a profile with
hand-pinned `models` needs no discovery. `--no-discover` skips the query
entirely for offline use.

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
