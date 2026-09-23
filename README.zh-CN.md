# cpa

**基于 Profile 的 Agent 启动器，面向 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI)（CPA）网关。**

```console
$ cpa claude --profile deepseek     # 上游全 DeepSeek 的 Claude Code
$ cpa claude --profile gpt          # 上游全 GPT 的 Claude Code
$ cpa claude --profile deepseek -p "解释一下这个仓库"
```

一个网关，多个上游。`cpa` 按命名 profile 逐次选择上游，并且**绝不改写你的
Claude Code 配置**——原生的 `claude` 命令照旧可用。

[English](README.md)

---

## 为什么需要它

一个 CPA 网关通常同时接着多个上游，用哪个取决于当前任务。而常见的切换方式都有代价：

| 做法 | 代价 |
|---|---|
| 手工编辑 `~/.claude/settings.json` | 全局、粘性、容易忘记当前生效的是哪个 |
| 会改写 `settings.json` 的 GUI 切换器 | 同上，而且动了别的工具拥有的文件 |
| 每个 profile 一个 shell alias | 每个 alias 都要重复一遍 `ANTHROPIC_*` 变量 |

`cpa` 在启动时即时算出环境，作为子进程交给 agent。没有任何全局状态被改动，
会话结束也不需要清理。

## 一个不那么显然的地方

只注入环境变量——最直觉的实现——在 `~/.claude/settings.json` 带 `env` 块时**行不通**。

Claude Code 会把 `settings.json` 的 `env` **叠加在**继承来的进程环境之上。所以下面这条会静默地走错上游：

```console
$ ANTHROPIC_BASE_URL=http://127.0.0.1:9999 claude -p ok
ok                      # ← 用的是 settings.json 里的端点，不是你指定的
```

因此 `cpa` 通过 `claude --settings <文件>` 传递 profile，它的优先级高于
`settings.json`。已在 Claude Code 2.1.280 上实测：

| profile 传给 Claude Code 的方式 | 能压过 `settings.json` 吗 |
|---|---|
| 进程环境变量 | **不能**，会被静默覆盖 |
| `--settings '{"env":{…}}'`（内联 JSON） | 能 |
| `--settings <路径>`（文件） | 能 |

`cpa` 用文件形式，这样 API key 不会出现在 `ps` 输出里。这是整个工具其余部分
围绕展开的唯一设计决策，并由 `TestBuildCarriesEnvThroughSettingsDocument`
固化下来，防止它退回成单纯的环境变量注入。

## 「非侵入」的确切含义

`cpa` 的保证：

- **绝不写入** `~/.claude/settings.json`、`~/.claude.json` 或任何属于 Claude Code 的文件，只读取。
- 配置读取自它自己的文件：`$XDG_CONFIG_HOME/cpa/settings.json`，未设置该变量时为
  `~/.config/cpa/settings.json`（见[配置](#配置)）。
- 唯一会写的东西是每次启动的 settings 文档：`0700` 目录下的 `0600` 文件，位置在
  `$XDG_RUNTIME_DIR/cpa/run/`，该变量未设置时用 `~/.local/state/cpa/run/`；
  超过一天的文档会在下次启动时清理。
- 原生 `claude` 不受影响，在 `cpa` 之外直接运行 `claude` 的行为与以前完全一致。
- `--dry-run` 会打印完整命令、完整环境变量和确切的 settings 文档，且不启动任何东西。

## 安装

```console
$ curl -fsSL https://raw.githubusercontent.com/shichao-wang/cpa/main/install.sh | bash
```

脚本会识别操作系统与 CPU 架构，下载对应的 release 包，用该 release 的
`checksums.txt` 校验 SHA-256，然后安装到 `~/.local/bin`。不需要 root，也不会读写
`~/.claude/` 下的任何东西。可用环境变量调整：

| 变量 | 作用 |
|---|---|
| `CPA_VERSION` | 安装指定 tag，而不是最新 release，如 `CPA_VERSION=v0.1.0` |
| `CPA_INSTALL_DIR` | 安装到 `~/.local/bin` 以外的目录 |
| `CPA_SKIP_VERIFY=1` | 跳过校验和验证（不建议） |

建议先读再执行——脚本很短，就在仓库根目录：[`install.sh`](install.sh)。
若想让安装脚本本身也固定在某个 release（而不是跟随 `main`），用该 release
附带的副本：

```console
$ curl -fsSL https://github.com/shichao-wang/cpa/releases/latest/download/install.sh | bash
```

或者从源码安装：

```console
# 有 Go 工具链
$ go install github.com/shichao-wang/cpa/cmd/cpa@latest

# 或从源码
$ git clone https://github.com/shichao-wang/cpa && cd cpa
$ make install
```

## 快速开始

```console
$ cpa init                     # 写入 ~/.config/cpa/settings.json
$ export CPA_API_KEY=sk-...    # 你的网关要求的 key
$ cpa doctor                   # 确认每个 profile 的端点可达
$ cpa claude --profile deepseek
```

`cpa profile create` 会逐项问你：名字、描述、这个 profile 服务哪个 agent、网关
地址与 key；随后先问网关它提供哪些模型，再让你从这些模型里选 upstream family，
以及每个 Claude Code 槽位用哪个模型。family 与槽位都是方向键选择的列表，选项来自
网关真实返回的模型，因此不可能因为手误填进一个不存在的模型。给别的 agent 建的
profile 只问一个模型：槽位是 Claude Code 独有的。

```console
$ cpa profile create
? Profile name devbox
? Description (optional) devbox gateway
? Agent this profile is for (claude, codex, or a name from "agents") claude
? Gateway base URL http://127.0.0.1:18317
? API key (optional; env:NAME and cmd:... also work) env:CPA_KEY
? Upstream family
❯ (every advertised model — 4)
  deepseek — deepseek-chat, deepseek-flash[1m] +1 more
  (type a family…)
# 选定后列表收成一行答案，随后依次问四个槽位：
? opus
❯ (follow the family — choose automatically)
? sonnet (follow the family — choose automatically)
? haiku (follow the family — choose automatically)
? fable (follow the family — choose automatically)

wrote profile "devbox" to ~/.config/cpa/settings.json
```

输入行支持编辑：左右方向键移动光标，home/end 与 ctrl-a/ctrl-e 跳到行首行尾，
ctrl-w 与 ctrl-u 删除，ctrl-c 放弃且不写任何文件。一行放不下的输入会横向滚动
而不换行。选项多到一屏放不下时列表同样会滚动，并标出屏外还有多少项
（`↑ 8 more`、`↓ 3 more`），因此再长的模型表也不会看起来像是只有这么多。key 那一项接受 `env:NAME`
与 `cmd:...` 简写，它们在启动时才解析，密钥因此不必落进文件。

没有终端时（管道、脚本、CI）完全不提问：所有字段都从命令行参数取
（`--name`、`--agent`、`--base-url`、`--api-key`、`--family`、`--model`
等），缺少必填项会直接报错而不是卡住。脚本里只给 `--name` 与 `--base-url`
就能建出一个 profile（不给 `--agent` 时它是一个 Claude Code profile）。

写入目标是 `$XDG_CONFIG_HOME/cpa/settings.json`；`--file` 可改为写到别处。

也可以不从头写，而是把现有配置直接转成 profile——它会读取你的 Claude Code
配置并提取其中的 `ANTHROPIC_*` 环境变量（不会修改那个文件）：

```console
$ cpa import-claude --name mygateway
```

## 配置

`cpa` 按优先级递增读取：

1. `$XDG_CONFIG_HOME/cpa/settings.json`，未设置 `XDG_CONFIG_HOME` 时为
   `~/.config/cpa/settings.json`
2. `./.cpa/settings.json` 与 `./cpa.settings.json`（项目内）

早期版本用的是 `~/.cpa/settings.json`，该路径**已不再读取**，把文件移至上表位置即可。

配置文件是**纯 JSON**，不允许注释与尾逗号。每个字段的含义写在
[`examples/settings.schema.json`](examples/settings.schema.json) 里，生成的文件都会通过
`$schema` 指向它，编辑器因此能实时校验与补全。（早期版本按 JSONC 解析；带注释的旧文件
现在会被拒绝，并明确告诉你原因。）

```json
{
  "defaultProfile": "deepseek",
  "defaults": {
    "env": { "CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "90" }
  },
  "profiles": {
    "deepseek": {
      "description": "全部流量走 DeepSeek",
      "baseUrl": "http://127.0.0.1:8317",
      "apiKeyEnv": "CPA_API_KEY",
      "family": "deepseek",
      "subagentModel": "deepseek-flash"
    }
  }
}
```

### Profile 字段

下表只是摘要。每个字段（含下表未列出的）的权威说明在
[`examples/settings.schema.json`](examples/settings.schema.json)——那才是编辑器读的东西。

| 字段 | 含义 |
|---|---|
| `agent` | 这个 profile 唯一服务的那个 agent。见[一个 profile 只服务一个 agent](#一个-profile-只服务一个-agent)。 |
| `baseUrl` | 网关端点，作为 `ANTHROPIC_BASE_URL` 传给 Claude Code。 |
| `apiKey` | 客户端 key，也接受 `"env:VAR"` 与 `"cmd:shell 命令"`。 |
| `apiKeyEnv` | 从该环境变量读取 key。 |
| `apiKeyCmd` | 从该命令的标准输出读取 key。 |
| `family` | 对网关 `/v1/models` 列表做子串匹配。 |
| `model` | 兜底模型，用于所有未显式指定的槽位。 |
| `models` | 手工钉死槽位：`{"opus": …, "sonnet": …, "haiku": …, "fable": …}`。钉死后完全跳过模型发现。 |
| `modelNames` | 按槽位覆盖模型选择器里显示的标签。 |
| `subagentModel` | 子 agent 使用的模型（`CLAUDE_CODE_SUBAGENT_MODEL`）。 |
| `contextWindow` | `CLAUDE_CODE_MAX_CONTEXT_TOKENS`；模型名带 `[1m]` 后缀时隐含 `1000000`。 |
| `customModelOption` | 把某个模型作为手工条目放进模型选择器。 |
| `env` | 额外环境变量，优先级高于自动生成的。 |
| `claudeSettings` | 额外 Claude Code 设置，合并进 `--settings` 文档。 |
| `args` | 传给 agent 的额外参数。 |

尽量别把 key 写进文件——`apiKeyEnv` 和 `apiKeyCmd` 就是为此存在的，让配置文件
可以安全地同步或提交。

也可以定义其他 agent：

```json
{
  "agents": {
    "claude": { "bin": "claude", "kind": "claude" },
    "codex":  { "bin": "codex",  "kind": "openai" }
  }
}
```

`kind` 决定注入哪类变量：`claude` → `ANTHROPIC_*`，`openai` → `OPENAI_*`，
`generic` → 只用 profile 自己的 `env`。

### 一个 profile 只服务一个 agent

一个 profile 说的是「某个下游应用该怎么接到某个上游」：它的槽位与设置属于那个
应用，对别的应用毫无意义。所以一个 profile 只配一个 agent，拿另一种 kind 的
agent 去启动它是报错，而不是把 Claude Code 的设置悄悄喂给读不懂它的程序：

```console
$ cpa codex --profile deepseek
cpa: profile "deepseek" is for agent "claude" (kind "claude"); "codex" is kind "openai"
a profile is written for one downstream application — its model slots and its settings mean nothing to another — so cpa will not apply it here.
launch it with an agent of kind "claude", or move the profile over with "agent": "codex"
```

kind 相同的两个 agent 可以共用一个 profile——读的变量完全一样，不会有损失。
需要各自上游的应用就各自建 profile：`examples/settings.json` 里在几个 Claude
Code profile 旁边就有一个 `codex` profile。

| profile 里写了什么 | 它服务于哪个 agent |
|---|---|
| `"agent": "codex"` | `codex`。显式声明永远优先。 |
| 没写 `agent`，但用了 `models`、`modelNames`、`subagentModel`、`customModelOption`、`contextWindow`、`claudeSettings` 中任意一个 | `claude`：这些字段只有 Claude Code 认，用了它们的 profile 就是 Claude Code profile——包括这条规矩出现之前就写好的那些。 |
| 没写 `agent`，上面那些字段一个也没用 | 任何 agent 都能用；`cpa profile list` 会给它显示 `-`，未绑定的 profile 是被看见的，而不是被默认假设的。 |

## Profile 如何变成模型映射

Claude Code 通过槽位（opus / sonnet / haiku / fable）寻址上游模型。`cpa`
按下面的顺序填充，遇到第一条能产出结果的规则就停：

| # | 规则 | 说明 |
|---|---|---|
| 1 | `models` 手工钉死 | 显式指定永远优先。 |
| 2 | `family` 恰好命中一个已公布模型 | 即「全部流量走 DeepSeek」的情形——一个模型填满所有槽位。 |
| 3 | `family` 命中多个 | 按名字分桶：`flash`/`mini`/`nano` → haiku，`pro`/`max`/`ultra` → opus，`sonnet`/`medium` → sonnet；剩余槽位取其中最强的一个。 |
| 4 | 存在四个 `claude-<槽位>-*` 条目 | 网关把上游别名成了 Claude 形状的名字。 |
| 5 | `model` | 兜底。 |
| 6 | 只公布了一个模型 | 用它填满所有槽位。 |
| 7 | 什么都没有 | 留空，并在 stderr 给出警告。 |

第 3 条起是有意为之的启发式。`cpa models --profile X` 总会打印选中了什么、
由哪条规则决定；而显式的 `models` 映射可以覆盖全部推断：

```console
$ cpa models --profile deepseek
profile deepseek -> http://127.0.0.1:8317  (4 models)

  claude-haiku-4-5   [anthropic]  -> haiku
  claude-opus-5      [anthropic]  -> opus
  claude-sonnet-5    [anthropic]  -> sonnet

mapping source: claude aliases
```

## 命令

| 命令 | 用途 |
|---|---|
| `cpa <agent> [flags] [-- args]` | 用某个 profile 启动 agent。 |
| `cpa models --profile X` | 列出网关模型与槽位映射。 |
| `cpa profile list [--json]` | 列出已配置的 profile。 |
| `cpa profile create` | 新增一个：有终端时交互，否则走参数。 |
| `cpa doctor` | 检查每个 profile 的端点与 key。 |
| `cpa init [--force]` | 写入起始配置文件。 |
| `cpa import-claude` | 把 `~/.claude/settings.json` 的环境变量转成 profile。 |
| `cpa version` | 打印版本。 |

参数：`--profile`、`--dry-run`、`--no-discover`、`--json`、`--name`、
`--agent`、`--file`、`--allow-settings-conflict`。未识别的参数一律透传给
agent，所以 `cpa claude --profile deepseek --resume` 就是你想的那样。`cpa
profile create` 另有 `--description`、`--base-url`、`--api-key`、`--family`、
`--model`、`--force`，用来在没有终端时回答它的提问。

## 排错

**网关报 `unrecognized_model`。** Claude Code 请求的模型网关没有。跑
`cpa models --profile X`，然后用 `models` 钉死成网关真正公布的模型名。

**模型发现失败但启动仍然成功。** 这是设计如此：手工钉了 `models` 的 profile
不需要发现。离线场景可以加 `--no-discover` 完全跳过查询。

**报 `profile "X" is for agent "Y"`，但我用的是另一个 agent。** 一个 profile
只服务一个 agent，见[一个 profile 只服务一个 agent](#一个-profile-只服务一个-agent)。
用它所属的 agent 启动，或把 profile 的 `agent` 改成你实际要用的那个。`cpa
profile list` 会显示每个 profile 绑在哪个 agent 上，未绑定的显示 `-`。

**我全局的 `ANTHROPIC_CUSTOM_MODEL_OPTION` 还在。** `cpa` 只覆盖它自己管理的
变量；全局 `env` 块里的其他内容（包括模型选择器条目）保持原样。想让 profile
接管这一项，就设置 `customModelOption`。

**两个 `--settings`。** `cpa` 自己要用 `--settings`，所以你自己传一个会被报错
拦下，而不是靠猜。把那些设置放进 profile 的 `claudeSettings`，或加
`--allow-settings-conflict` 让 cpa 的文档排在后面。

## 状态

早期阶段。被实际跑通的是 `claude` 这一种 agent；`openai` 与 `generic` 会注入
对应变量，但还没对着真实的 Codex/Gemini CLI 跑过。

## 许可

MIT
