# cpa

通过 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的 Go SDK 嵌入网关，产物为单个 `cpa` binary，使用 CLI 管理连接、profile 和网关进程。本仓库独立实现网关。

## 配置模型

```text
connection：独立上游连接（稳定 ID、协议、认证方式、API 根地址、专属凭证、模型清单）
profile：下游 Agent 的专属 key、四档 connection/实际模型绑定与展示
```

接入只需创建一个 connection，不需要先创建 provider。每个连接独立选择协议和凭证，名称不决定协议。每个 profile 的 Opus、Sonnet、Haiku、Fable 四个必填档位直接选择 connection 和实际模型 ID；多个档位可以使用同一连接。

**这是不兼容旧配置的结构调整。** 当前只支持 schema 3，不保留 `provider` 命令、旧 profile 的 `provider` 字段或旧状态迁移。schema 1、旧 provider 版和独立 API-key 版 schema 2，以及缺少 Fable 的三档 schema 3 状态，均明确拒绝且不会被覆盖。已有配置的用户应先用原版本执行 restore、停止旧服务，再将旧状态目录移至备份位置后重新接入；OAuth 连接需要重新登录。不要让旧、新版本共享状态目录。

下游公开模型 ID 保持为 Claude ID。不同 profile 可以使用相同公开 ID，但展示不同名称并路由到不同上游。

## 安装

```sh
curl -fsSL https://raw.githubusercontent.com/shichao-wang/cpa/main/scripts/install.sh | bash
```

脚本识别 macOS/Linux 与 amd64/arm64，从 [Releases](https://github.com/shichao-wang/cpa/releases) 下载对应产物并用 `checksums.txt` 校验后安装。默认装到 `/usr/local/bin`，该目录不可写时回退到 `~/.local/bin`。

可选环境变量：

| 变量 | 说明 |
|---|---|
| `CPA_VERSION` | 指定版本（如 `v0.1.0`），默认为最新 Release |
| `CPA_INSTALL_DIR` | 指定安装目录，优先级高于默认与回退规则 |

安装包校验是强制环节，没有跳过开关：脚本比对 `checksums.txt` 中的 SHA256，不匹配即中止。该检查防的是传输损坏与只改动产物一侧的篡改，属于完整性校验，不构成对发布来源的认证。

建议先下载脚本审阅再执行：`curl -fsSL <上述地址> -o install.sh`，检查无误后 `sh install.sh`。本工具暂不支持 Windows。

## 升级

### 从三档 profile 升级

本版本要求 Opus、Sonnet、Haiku、Fable 四个档位全部配置。缺少 Fable 的旧 schema 3 状态不支持原地升级，新版本会拒绝读取和覆盖，不能通过 `profile update` 补填。升级前请保留原可执行文件，按以下步骤切换：

1. 使用原版本及原状态目录执行 `profile restore`，恢复已接管的 Claude Code 配置；没有应用过 profile 时可跳过。若使用自定义 settings，传入原 `--settings` 路径。
2. 使用原版本及原状态目录执行 `server stop`。将旧状态目录整体移至备份位置，保留其中内容。默认目录为 `$XDG_CONFIG_HOME/cpa`，未设置时为 `~/.config/cpa`。
3. 安装新版本，在自动创建的默认状态目录中重新创建连接，并用包含四个档位的 YAML 创建 profile。OAuth 连接需重新登录，下游 key 也会重新生成。
4. 在新状态目录中启动网关并应用 profile，随后新开 Claude Code 会话。

例如，在已用原版本完成恢复和停服后执行：

```sh
cpa connection add example
cpa profile create daily --agent claude-code --file bindings-with-fable.yaml
cpa server start
cpa profile apply daily
```

需要回退时，先用新版本执行 restore、停止新网关，再将当前状态目录移至另一备份位置。将旧状态目录放回原位置后，使用保留的原版本启动服务、应用原 profile。

### 从改名前的版本切换

旧版本内置的 `upgrade` 只识别旧名称的安装包，不能用于本次改名升级。请按以下步骤手动切换：

1. 使用原可执行文件执行 `profile restore`，恢复已接管的 Claude Code 配置；使用自定义状态目录或 settings 时，传入原来的路径。没有应用过 profile 时可跳过恢复。
2. 使用原可执行文件执行 `server stop`，确认旧网关停止。保留原可执行文件和状态目录，便于需要时回退。
3. 按上面的安装步骤安装 `cpa`，执行 `cpa version` 确认新命令可用。新版本使用独立的默认状态目录，不会自动读取或迁移旧目录，请勿让两个版本共享状态目录。
4. 用 `cpa connection add` 和 `cpa profile create` 重新配置连接与 profile；OAuth 连接需重新登录。随后执行 `cpa server start` 和 `cpa profile apply <名称>`，并新开 Claude Code 会话。

需要回退时，先用 `cpa profile restore` 恢复配置并执行 `cpa server stop`，再用原可执行文件和原状态目录启动服务、应用原 profile。

### 后续版本升级

```sh
cpa upgrade
```

升级支持 macOS/Linux 的 amd64 与 arm64。命令只替换当前正在运行的 `cpa` 实际可执行文件；通过符号链接启动时保留符号链接并更新其目标，不修改 cpa 配置。它不会自动提权（`sudo`）、切换安装目录或重启网关进程；如果当前可执行文件所在目录不可写，升级会失败，请自行处理目录权限。网关正在运行时，升级后需手动重启。

当前版本与最新 Release tag 相同时跳过替换；从源码构建或版本未知时，将替换为最新 Release。升级复用安装脚本的 `checksums.txt` SHA256 完整性校验边界，不代表对发布来源的认证。

## 构建

首期支持 macOS/Linux（进程管理依赖 Unix 信号及 `ps`），需要 Go 1.27.1 或更新版本：

```sh
go build -o bin/cpa ./cmd/cpa
export PATH="$PWD/bin:$PATH"
cpa --help
```

SDK 固定为 CLIProxyAPI v8.0.10。分发时请附上 `THIRD_PARTY_NOTICES`，并遵守传递依赖的许可证。

## 发布

合并 PR 到 `main` 后自动打 tag 并发布 Release：

```text
合并 PR → ci 通过 → 打 tag（v2026.10.5-abc123）→ GoReleaser 发布 Release
```

tag 由日期与 commit 短 SHA 组成，不含补零（`2026.10.5` 而非 `2026.10.05`），否则不是合法 semver，GoReleaser 会拒绝。**直接 push 到 `main` 不会发布**，只有经 PR 合并的提交才触发。

Release 不是预发布版，因此 `/releases/latest` 始终指向最新一次发布，安装脚本无需指定版本即可取用。手动发布可在目标 commit 上推一个 `v*` tag：

```sh
git tag v2026.10.5-abc123 && git push origin refs/tags/v2026.10.5-abc123
```

## 接入上游

交互接入只需一条命令：

```sh
cpa connection add
# 也可以先指定名称：cpa connection add example
```

向导依次收集连接名称、认证方式、协议、API 根地址、模型清单和凭证信息，确认后一次保存。API-key 连接使用隐藏输入；Codex OAuth 连接不询问 key，创建后单独登录。认证方式、协议、模型来源、继续添加模型和最终确认均使用 **↑/↓ 选择、Enter 确认**，不需要输入数字编号或 `y/n`；保存和删除确认默认选中“取消”。直接录入模型时，每条使用 `ID` 或 `ID=展示名`，之后选择“继续添加模型”或“完成录入”；也可以选择 YAML 文件，不必先创建文件。

在文本、选择菜单、密码和确认阶段都可按 **Control+C** 取消；命令提示“已取消”，以退出码 130 返回 shell 并恢复终端。保存前（包括等待状态锁时）取消不会保存连接、凭证或增加 revision；原子提交开始后不保证撤回，保存成功后的重载失败仍会明确报告已保存。添加默认不联网、不发模型请求。

脚本接入先准备模型清单（必须填写上游真实支持的模型 ID）：

```yaml
models:
  - id: upstream-large
    name: 主力模型
  - id: upstream-balanced
    name: 均衡模型
  - id: upstream-fast
    name: 快速模型
```

将 key 从标准输入传入，不要将其放进命令参数或 shell 历史：

```sh
# 从安全的凭证来源提供 stdin；不要在命令中硬编码 key。
cpa connection add example \
  --protocol anthropic-messages \
  --base-url https://api.example.com \
  --models models.yaml \
  --api-key-stdin
cpa connection show example
cpa connection models example
cpa connection check example
```

API-key 非交互接入必须提供完整参数和 `--api-key-stdin`；OAuth 非交互接入显式指定 `--auth-type codex-oauth`、`--protocol responses` 和模型清单，不提供 API key。缺参会报错而不会等待向导。每个连接持有自己的凭证，不存在默认 key 继承或隐藏的 provider。show/list 仅显示认证方式与凭证就绪状态，不回显 key、token、凭证引用或账户标识。

### Codex OAuth

Codex OAuth 目前只支持 `responses` 协议和 OpenAI 官方 Codex endpoint `https://chatgpt.com/backend-api/codex`。可省略 `--base-url`，也可显式指定这个官方地址；不支持自定义 OAuth endpoint。认证通过 Codex 的设备码流程完成：命令输出官方验证页和设备码，用户在浏览器中完成授权，CLI 等待授权结果。可用 `--no-browser` 禁止 CLI 尝试打开浏览器。

为 Codex OAuth 准备独立的 `codex-models.yaml`，不要复用上面 API key 示例的模型清单：

```yaml
models:
  - id: gpt-5.5
    name: Codex 示例模型
```

`gpt-5.5` 仅为示例，使用前请确认自己的账号实际支持该模型，并据此填写模型清单与 profile 绑定。

```sh
cpa connection add codex \
  --auth-type codex-oauth \
  --protocol responses \
  --models codex-models.yaml
cpa connection login codex --no-browser
# bindings.yaml 的 opus/sonnet/haiku/fable 均设置 connection: codex、target_model: gpt-5.5
cpa profile create codex --agent claude-code --file bindings.yaml
cpa server start
cpa profile apply codex
cpa server stop
cpa connection logout codex --yes
```

认证方式切换必须在 `connection update` 中显式传 `--auth-type`；切换为 API key 时还需在同一命令显式提供 `--api-key-stdin`，不能继承或回退。新增连接、OAuth `login` 与重登录支持热更新，已有请求继续完成。`logout`、认证类型切换及删除 OAuth 连接仍要求先停止网关。连接保留，logout 只清除本地保存的 OAuth 凭证，不代表向上游撤销授权。OAuth 凭证属于本地敏感状态，可能以明文保存但文件权限限制为 0600；请勿提交或分享状态文件。OAuth 账号可用模型取决于账号实际授权及 SDK 注册目录。CLI 提供的模型清单是本地声明，不保证账号支持。OAuth token 不会发送到自定义地址或通用 OpenAI `/v1/models` 接口。已声明的模型也不保证账号当前有可用额度。

上游协议：

| protocol | 上游入口 | SDK 适配 |
|---|---|---|
| `anthropic-messages` | API 根地址 + `/v1/messages` | Claude |
| `chat-completions` | API 根地址 + `/chat/completions` | OpenAI compatibility |
| `responses` | API 根地址 + `/responses` | Codex HTTP |

地址应与 SDK 的路径拼接契约匹配。例如 OpenAI 常规 Chat Completions 根地址包含 `/v1`，DeepSeek 可使用 `https://api.deepseek.com`。Anthropic 根地址不要再以 `/v1` 结尾。Responses 不自动追加 `/v1`。

**Responses 连接已关闭 SDK 的 Codex 身份强制设置，但仍有请求规范化，不是任意 Responses API 的透明透传。** 支持指定协议不等于任何服务或模型都完全兼容 Claude Code 的 thinking、工具调用和上下文能力。三种协议不做自动故障切换。

add/create 默认不联网。check 仅尝试模型目录，不发送推理；目录缺失、404/405 表示无法通过这种方式验证，不能据此判断推理不可用。检查结果不会自动替换本地清单。

## Profile 与 Claude Code

### Responses 兼容性说明

Responses 连接经当前锁定 SDK 的 Codex 转换路径执行。`GET /v1/models` 的对应条目通过 `cpa_capabilities` 返回每个模型的能力说明。这是派生能力信息，不写入状态，也不根据连接名称或公开 Claude 别名推断上游能力。

- **输出预算不执行。** 此路径不把 `max_tokens` 变成上游输出上限。推理响应会带 `X-CPA-Output-Limit: not-enforced`；要求执行输出上限的调用方可发送 `X-CPA-Require-Output-Limit: true`，网关会在调用上游前返回 400。普通 Claude Code 调用仍保留，但不能依赖该参数控制消耗。标准 OpenAI Responses API 的预算说明不能直接当作此转换路径的能力承诺。
- **推理是兼容映射。** effort 档位来自公开 SDK 的模型目录；未知推理能力的模型会拒绝显式启用 thinking 或 effort，普通对话仍可用。已知模型的 adaptive、预算和 thinking 关闭按 SDK 转换；没有 `none` 档位时，关闭会降到最低支持档。仅传 effort 而没有 thinking 时可能被 SDK 忽略。
- **上下文容量未经实测。** 客户端显示的 200K / 1M 不是上游容量声明，`[1m]` 不会为连接扩容。
- **自动关闭不支持的服务端工具。** Messages → Responses / Chat 路径会移除 `web_fetch` / `code_execution` 声明；`web_search` 仅在 Responses 路径且当前连接的模型能力明确支持时保留，未知能力也会关闭。普通客户端工具（包括 Bash、WebFetch、MCP）保留。强制选择被移除工具时自动改为 auto，所有工具被移除时改为 none。响应头 `X-CPA-Disabled-Server-Tools` 列出关闭的工具类型；原生 Anthropic 连接不做此过滤。搜索的调用次数上限仍不映射。

`X-CPA-Compatibility-Warnings` 返回逗号分隔的机器可读代码，例如 `output-limit-not-enforced`、`thinking-disabled-mapped`、`adaptive-effort-mapping`、`thinking-budget-mapped`、`thinking-budget-overrides-effort`、`effort-without-thinking-ignored`、`context-window-unverified`。未知模型的关闭请求会标记 `thinking-off-unverified`，不承诺推理已关闭。

此 Responses 入口会拒绝预算字符串、null、布尔、小数、溢出和重复参数。`max_tokens` 若提供，须为 1～2147483647 的 JSON 整数；`budget_tokens` 仅可用于 `thinking.type=enabled`，须为 1024～2147483647 的 JSON 整数且小于提供的 `max_tokens`。不提供 budget 时沿用 SDK 默认映射。adaptive / disabled 请移除预算字段。该检查只定义 Responses 兼容请求，不改变原生 Anthropic 连接的 interleaved thinking 语义；预算合规也不代表此路径执行 token 上限。

### 应用与恢复

绑定文件示例见 `examples/`。公开 model ID 需明确填写你希望 Claude Code 使用的官方 ID，不能填实际的非 Claude 上游 ID。四个档位 ID 必须唯一。

Fable 是必填档位，profile YAML 必须包含 `models.fable`，字段与其他档位相同，示例见 `examples/profile.yaml`。创建、更新、加载及应用 profile 时，缺少任一档位都会报错；旧三档状态须按升级步骤备份旧状态目录后重新配置，不能在旧目录直接补填。应用 profile 后会写入 `ANTHROPIC_DEFAULT_FABLE_MODEL`，展示名和说明统一写入 `modelPicker.options`，配置方式见 [Claude Code 模型配置](https://code.claude.com/docs/en/model-config#environment-variables)。在 Claude Code 中使用 `/model fable` 选择该映射，restore 会恢复应用前的配置。

```sh
cpa profile create daily --agent claude-code --file bindings.yaml
cpa server start
cpa server status
cpa profile apply daily
# 新开 Claude Code 会话使用已应用的配置。
cpa profile restore
cpa server stop
```

模型选择器展示需要支持 `modelPicker` 的 Claude Code 版本（v2.1.242 或更新版本）。

apply 默认合并 `~/.claude/settings.json`，也可通过 `--settings` 指定文件。env 设置网关地址、profile 认证、四档 `ANTHROPIC_DEFAULT_{OPUS,SONNET,HAIKU,FABLE}_MODEL` 公开模型 ID，以及指向 haiku 档位的 `CLAUDE_CODE_SUBAGENT_MODEL`。apply 不修改订阅登录凭证。

模型名称与描述统一写入 `modelPicker.options`，并设置 `modelPicker.replaceBuiltInOptions: true` 来替换内置列表。模型选择器左侧自动显示上游模型名称，GPT 模型 ID 会整理为 `GPT 6.1 Sol`、`GPT 6 Luna` 这样的名称。右侧固定显示对应的 `Opus`、`Sonnet`、`Haiku` 或 `Fable` 档位。名称与描述不可配置，profile YAML 不接受 `label`、`description` 字段。apply 不再写入四档 MODEL 的 `_NAME`、`_DESCRIPTION` 环境变量，也不再启用 `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY`。

已有旧版 apply 记录时，再次 apply 或切换 profile 会将上述九个旧受管变量恢复为首次接管前的值，原先不存在的变量会删除，然后解除对这些变量的管理。没有管理记录的同名变量保持原样。清理完成后，后续 apply 和 restore 会保留用户对这些变量的新修改。直接对旧版记录执行 restore 仍会恢复全部受管字段；旧受管字段在清理前已被手动修改时，apply 和 restore 都会拒绝覆盖。

restore **仅对有有效 cpa 接管记录的配置执行**。它恢复首次接管前的受管字段，保留无关修改；受管字段发生冲突时拒绝整体恢复。没有记录、只有备份、路径不匹配或重复 restore 均不能恢复。当前 shell 环境变量及项目级设置可能覆盖用户配置，需自行检查这些优先级。

### Claude Desktop（macOS）

Claude Desktop 使用独立的 `claude-desktop` profile 和下游 key，可以与 Claude Code 同时接入同一网关。已验证 Desktop 2.19675.0 的 Chat 和本地 Code，内嵌 Code 版本为 2.1.286。Cowork 首期关闭。模型选择器的上下文和能力显示来自客户端，不代表上游已验证这些能力。兼容性证据见 [报告](docs/claude-desktop-compatibility.md)。

```sh
cpa profile create desktop --agent claude-desktop --file bindings.yaml
cpa server start
# 完整退出 Claude Desktop 后执行。
cpa profile apply desktop
# 重新启动 Claude Desktop，选择 Chat 或本地 Code。
# 恢复前也必须完整退出 App。
cpa profile restore --agent claude-desktop
```

Desktop 默认管理 `~/Library/Application Support/Claude/` 与 `Claude-3p/` 的模式文件，以及第三方配置库中的 cpa 独立项、活动指针和混合配置指针。`--config-dir` 指向同时包含这两个目录的 Application Support 父目录，不能传入 configLibrary 自身。Claude Code 的 `--settings` 与 Desktop 的 `--config-dir` 不可混用，显式空值也按已指定处理。默认 `profile restore` 仍恢复 Claude Code。

apply 不启动或结束 App，不修改真实 `~/.claude`，也不管理会话历史。组织策略、运行中的 App、损坏配置、符号链接或受管字段冲突会阻止操作。重复 apply 和切换 Desktop profile 保留首次接管基线；写入中断后，在排除冲突并退出 App 后对同一目录重试 apply 或 restore。不要删除 cpa 的 Desktop 管理记录来绕过冲突。管理记录读取失败时，删除 profile 和切换 Agent 会被拒绝；当前受管及中断切换的前后双方均受保护。

schema 3 的现有 Claude Code 状态继续有效。旧 binary 可能拒绝包含 Desktop profile 的状态；回退前先用支持 Desktop 的版本分别 restore Desktop 和 Claude Code，停止网关，再使用与旧 binary 匹配的独立状态目录或原有备份。不要让旧 binary 覆盖不兼容状态，也不要把未恢复的配置留给旧版本管理。

## 更新与删除

```sh
cpa connection update example --api-key-stdin
cpa connection update example --models models.yaml
cpa connection update example --base-url https://api.example.com
cpa connection remove example
cpa profile delete daily
```

更新未指定 key 时保留连接的原凭证；地址、模型与 key 同时修改时按同一事务保存。轮换只影响目标连接。删除连接或移除模型前检查 profile 引用，存在引用时拒绝。被活动 apply 记录引用的 profile 需先 restore 或切换到其他 profile；解除 profile 对连接的引用后才能删除连接。非交互删除需提供 `--yes`。

## 远端配置

通过 SSH 登录服务器后，执行同样的 CLI 命令管理远端连接、profile 和网关。命令使用远端用户的状态目录，`profile apply` 修改执行命令所在机器的客户端配置。

## 本地状态与安全

- 状态目录使用 `$XDG_CONFIG_HOME/cpa`，未设置时使用 `~/.config/cpa`。命令行无需指定状态目录。
- 状态目录 0700、敏感文件 0600；配置、secret 引用及 secret 数据以同一原子事务保存。上游 key、Codex OAuth 凭证和下游 profile key 分开管理，SDK 配置是私有派生数据。
- 当前状态格式为 schema 3，仅接受显式认证方式、稳定连接 ID 与独立 OAuth 表的完整新格式；不存在 `state migrate` 或自动默认化。旧 schema 1/2 及 provider 结构会拒绝读取/覆盖，使用新状态目录重新配置，OAuth 连接重新登录。
- 本地状态、Claude Code 应用后的 settings 和 Claude Desktop 的受管配置及管理记录都可能含明文 key；文件权限保护不是加密保险箱。不要提交或分享这些文件。
- 默认仅监听 loopback；后台和前台启动都会预检查监听地址，端口冲突时明确报告占用地址，不启动网关，也不停止原占用服务。预检查分两步：先探测同端口的 `127.0.0.1` 与 `::1` 是否已有服务监听，再验证地址可绑定。**只做绑定检查并不足够**——macOS 允许通配监听（如 `*:8317`）与具体 loopback 地址同时绑成功，绑定成功不能证明端口空闲。预检查会立即释放临时监听器，正式监听和实例就绪校验仍负责处理之后的端口竞争。
- 下游只开放模型列表、Messages 及支持的 count_tokens，其他执行协议不向 profile key 开放。
- 模型目录隐藏不是唯一的访问控制：每次推理也严格校验 key 和公开模型绑定。
- 本地状态是配置与 OAuth 凭证的唯一事实源，SDK 配置和认证记录均为派生运行时数据。网关进程使用单个 SDK Service；结构变更在该 Service 上串行热更新。新增连接与 OAuth 登录只注册目标认证和模型并发布新路由，未变更的 profile 在更新期间继续服务。OAuth token 刷新仅做凭证条件保存，不触发结构重载；退出、删除 OAuth 连接和认证方式切换仍要求模型网关停止。
- stop 不承诺等待全部在途 SSE 完整结束。

## 验证

```sh
go test ./...
go test -race ./...
go vet ./...
go build -o bin/cpa ./cmd/cpa
```

测试只使用临时状态/settings 和 mock 上游，不改真实 `~/.claude`，不调用付费模型。真实模型的兼容性和费用需要另外授权验收。
