# cpa

通过 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的 Go SDK 嵌入网关，主要产物为单个 `cpa` binary。本仓库独立实现网关。

## 配置模型

```text
connection：独立上游连接（稳定 ID、协议、认证方式、API 根地址、专属凭证、模型清单）
profile：下游 Agent 的专属 key、三档 connection/实际模型绑定与展示
```

接入只需创建一个 connection，不需要先创建 provider。每个连接独立选择协议和凭证，名称不决定协议。每个 profile 的 Opus、Sonnet、Haiku 档位直接选择 connection 和实际模型 ID；多个档位可以使用同一连接。

**这是不兼容旧配置的结构调整。** 当前只支持 schema 3，不保留 `provider` 命令、旧 profile 的 `provider` 字段或旧状态迁移。schema 1，以及旧 provider 版和独立 API-key 版 schema 2，均明确拒绝且不会被覆盖。已有配置的用户应先用原版本执行 restore、停止旧服务，再用新的 `--state-dir` 重新接入；OAuth 连接需要重新登录。不要让旧、新版本共享状态目录。

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

前端使用 React、TypeScript、Vite、Tailwind CSS 和 shadcn/ui，源码位于 `frontend/`。仓库保留构建后的内嵌资源，直接 `go build` 无需 Node.js。修改前端时需要 Node.js 24：

```sh
npm --prefix frontend ci
npm --prefix frontend run build
go build -o bin/cpa ./cmd/cpa
```

构建会更新 `internal/webui/assets/` 和前端依赖许可，两者应随源码一起提交。CI 会重新构建并检查资源是否一致。开发预览可运行 `npm --prefix frontend run dev`，管理接口转发到 `127.0.0.1:18317`；完整验收请使用构建后的 binary，通过 `/ui/` 访问。

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
# bindings.yaml 的 opus/sonnet/haiku 均设置 connection: codex、target_model: gpt-5.5
cpa profile create codex --agent claude-code --file bindings.yaml
cpa server start
cpa profile apply codex
cpa server stop
cpa connection logout codex --yes
```

认证方式切换必须在 `connection update` 中显式传 `--auth-type`；切换为 API key 时还需在同一命令显式提供 `--api-key-stdin`，不能继承或回退。新增连接、OAuth `login` 与重登录支持热更新，已有请求继续完成。`logout`、认证类型切换及删除 OAuth 连接仍要求先停止网关；网页退出与删除会自动暂停模型服务并在结束后恢复。连接保留，logout 只清除本地保存的 OAuth 凭证，不代表向上游撤销授权。OAuth 凭证属于本地敏感状态，可能以明文保存但文件权限限制为 0600；请勿提交或分享状态文件。OAuth 账号可用模型取决于账号实际授权及 SDK 注册目录。CLI 提供的模型清单是本地声明，不保证账号支持。网页在授权后请求官方 Codex 账号目录，仅展示当前锁定 SDK 可以注册的模型；无兼容模型时明确报错。OAuth token 不会发送到自定义地址或通用 OpenAI `/v1/models` 接口。目录列出模型也不保证账号当前有可用额度。

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

绑定文件示例见 `examples/`。公开 model ID 需明确填写你希望 Claude Code 使用的官方 ID，不能填实际的非 Claude 上游 ID。三个档位 ID 必须唯一。

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

apply 默认合并 `~/.claude/settings.json`，也可通过 `--settings` 指定文件。通过 env 和 modelPicker 设置网关认证、模型 ID、名称与描述，不修改订阅登录凭证。模型发现不会可靠地覆盖所有内置档位，因此展示配置也由 profile 应用。

restore **仅对有有效 cpa 接管记录的配置执行**。它恢复首次接管前的受管字段，保留无关修改；受管字段发生冲突时拒绝整体恢复。没有记录、只有备份、路径不匹配或重复 restore 均不能恢复。当前 shell 环境变量及项目级设置可能覆盖用户配置，需自行检查这些优先级。

## 更新与删除

```sh
cpa connection update example --api-key-stdin
cpa connection update example --models models.yaml
cpa connection update example --base-url https://api.example.com
cpa connection remove example
cpa profile delete daily
```

更新未指定 key 时保留连接的原凭证；地址、模型与 key 同时修改时按同一事务保存。轮换只影响目标连接。删除连接或移除模型前检查 profile 引用，存在引用时拒绝。被活动 apply 记录引用的 profile 需先 restore 或切换到其他 profile；解除 profile 对连接的引用后才能删除连接。非交互删除需提供 `--yes`。

## WebUI 与远端配置

WebUI 随 `server start` 启动，与模型 API 共用监听端口，并随 `server stop` 关闭。页面和脚本内嵌在单个 binary 中，不需要安装前端依赖或访问 CDN。

在服务器执行：

```sh
cpa server start
```

在本地机器建立 SSH 隧道：

```sh
ssh -N -o ExitOnForwardFailure=yes -L 127.0.0.1:18317:127.0.0.1:8317 user@remote-host
```

在浏览器打开 [管理页面](http://127.0.0.1:18317/ui/)。服务监听 `127.0.0.1` 时自动进入管理页面，无需填写管理令牌；通过 SSH 隧道访问同样适用。远端网关仍只监听 loopback。本地端口可以不同于远端端口；如果远端使用其他监听端口，修改转发目标即可。使用自定义状态目录时，所有远端命令都需要指定相同的 `--state-dir`。

管理页面支持 API key 连接的创建、地址与模型清单更新、凭证轮换和删除，以及 profile 的创建、映射更新和删除。新增连接时填写地址与 API key，点击「测试连接并获取模型」，成功后自动展示可勾选的模型目录。新发现的模型默认勾选，可按需取消后再保存连接，无需手填模型清单。连接协议创建后固定。页面显示已保存和实际生效的修订，等待加载时可以刷新确认。CLI 与 WebUI 共用状态事务、引用检查和热更新流程。

侧栏切换连接与 Profiles。连接列表可按名称搜索、按协议筛选。每个连接最多预览 3 个模型名称，摘要最多两行；点击模型数量可查看完整清单，并按名称或模型 ID 搜索。右侧抽屉可重新获取已有 API key 连接的模型目录，并提供模型搜索、全选当前结果和清空选择。保存时只写入勾选模型，至少保留一个；profile 只能绑定已保存到连接的模型，下游请求仍受 profile 模型授权限制。重新测试时保留已有选择，新发现的模型默认勾选；被 profile 引用的模型需先调整映射后再取消。OAuth 连接使用相同的模型搜索和勾选界面，无需手工录入模型 ID。profile 编辑页并列展示 opus、sonnet、haiku 三个档位，支持搜索上游模型、预览映射，以及填写可选的展示名和说明；窄屏下按档位纵向排列。

测试连接只发送模型目录 GET 请求，不发送推理请求，也不保存输入或修改 revision。Chat Completions 和 API key Responses 连接请求 API 根地址下的 `/models`；Anthropic Messages 请求 `/v1/models`，并自动读取后续分页。完整发现最多等待 8 秒、接收 4 MiB；空目录、无效响应或不完整分页会明确报错。目录访问成功不代表每个模型都已验证推理能力。测试不会跟随上游重定向或返回错误正文。测试已有连接时可留空 API key，使用此连接的保存凭证；修改地址后必须显式填写新地址对应的 key。修改地址、协议或 key 后，需重新测试再保存。

新增连接时，认证方式可选择「OAuth」或「API_KEY」。当前 OAuth 支持官方 Codex 设备码授权，固定使用 `responses` 协议和官方 endpoint。选择 OAuth 并填写连接名称后，点击「登录并获取模型」。页面会展示设备码及 OpenAI 官方授权页链接。完成官方授权后，cpa 自动读取账号模型目录，展示默认全选的模型列表，此时尚未创建 connection。模型列表保留在同一表单中。表单提供独立的「登录并获取模型」和「保存 Connection」按钮，登录前保存按钮不可用。取消不需要的模型后，点击「保存 Connection」，才会在同一事务中保存连接、凭证和所选模型。OAuth 表单不显示协议和 API 根地址。无需手工填写模型 ID。登录或获取目录失败、放弃创建时，不保存 connection 或凭证。

已有 OAuth 连接可重新登录、退出授权、重新获取模型或删除。编辑连接中的「测试连接并获取模型」只读取目录，勾选并保存后才更新模型清单。新发现模型默认勾选，同一次编辑中重新获取目录会保留已取消的选择。已有连接重新登录后获取目录失败时，会保留已完成的授权并提示重试。自动获取时会保留已保存但目录暂未返回的模型，避免破坏已有 profile 引用。退出仅清除本地凭证，被 profile 引用的连接不能删除。

未创建的授权草稿只在服务内存中暂存，最多保留 15 分钟，且不超过凭证有效期。浏览器刷新并重新连接后可继续选择模型，服务重启后草稿失效。后台登录进程通过权限为 0600 的临时文件交接授权结果，读取或取消后立即删除。

网页 OAuth 登录、获取模型及最终创建期间，模型服务持续运行，已有请求继续完成；取消、超时或失败不会改变原有连接。授权成功后只注册目标连接的凭证和模型，再原子切换路由。退出与删除 OAuth 连接仍会暂停模型服务并中断在途输出，结束后自动恢复，恢复失败时页面提供重试按钮。设备授权最多等待 15 分钟，刷新页面并重新连接后可继续查看进度。设备码仅在授权期间展示，OAuth token 不返回浏览器。模型 SDK 使用独立的临时 loopback 端口，外部仍通过原配置端口访问。`server status` 在暂停期间显示 `maintenance`；`server stop` 仍关闭整个管理及模型服务。认证方式切换仍通过 CLI 在停机时完成。WebUI 不修改 Claude Code 的 settings；远端执行 `profile apply` 修改的是远端机器的设置。

监听 `127.0.0.1` 时管理接口无需令牌，页面会自动连接；其他监听地址仍要求独立管理令牌，可执行 `cpa server admin-token` 获取。管理令牌首次使用时自动生成，独立保存在状态目录的 `admin-token` 文件中，权限为 0600，不进入 schema 3 状态、SDK 配置或服务日志。`server admin-token` 会主动输出敏感令牌，请勿分享。需要令牌时，页面仅在内存中保留，刷新后需重新输入。profile key 不能作为管理令牌使用，管理令牌也不能调用模型 API。新建 profile 的响应会返回一次专属下游 key，页面提供复制步骤，并显示当前隧道地址供本地 Agent 使用。请在关闭提示或刷新前保存 key。之后的列表和更新响应不会返回该 key；上游 key、OAuth token 和凭证引用始终不返回。

管理接口位于 `/admin/api/`，监听 `127.0.0.1` 时允许无令牌请求，其他监听地址仅接受独立 Bearer 管理认证。不使用 Cookie 或 URL 参数认证。接口校验 loopback Host 和浏览器 Origin，不开放跨来源访问。通过隧道访问页面即可配置远端，无需另设服务地址或开放公网监听。

## 本地状态与安全

- 状态目录默认为 `$XDG_CONFIG_HOME/cpa`，未设置时使用 `~/.config/cpa`；可用 `--state-dir` 隔离。
- 状态目录 0700、敏感文件 0600；配置、secret 引用及 secret 数据以同一原子事务保存。上游 key、Codex OAuth 凭证和下游 profile key 分开管理，SDK 配置是私有派生数据。
- 当前状态格式为 schema 3，仅接受显式认证方式、稳定连接 ID 与独立 OAuth 表的完整新格式；不存在 `state migrate` 或自动默认化。旧 schema 1/2 及 provider 结构会拒绝读取/覆盖，使用新状态目录重新配置，OAuth 连接重新登录。
- 本地状态和 Claude Code 应用后的 settings 都可能含明文 key；文件权限保护不是加密保险箱。不要提交或分享这些文件。
- 默认仅监听 loopback；后台和前台启动都会预检查监听地址，端口冲突时明确报告占用地址，不启动网关，也不停止原占用服务。预检查分两步：先探测同端口的 `127.0.0.1` 与 `::1` 是否已有服务监听，再验证地址可绑定。**只做绑定检查并不足够**——macOS 允许通配监听（如 `*:8317`）与具体 loopback 地址同时绑成功，绑定成功不能证明端口空闲。预检查会立即释放临时监听器，正式监听和实例就绪校验仍负责处理之后的端口竞争。
- 下游只开放模型列表、Messages 及支持的 count_tokens，其他执行协议及管理入口不向 profile key 开放。
- 模型目录隐藏不是唯一的访问控制：每次推理也严格校验 key 和公开模型绑定。
- 本地状态是配置与 OAuth 凭证的唯一事实源，SDK 配置和认证记录均为派生运行时数据。网关进程使用单个 SDK Service；结构变更在该 Service 上串行热更新。新增连接与 OAuth 登录只注册目标认证和模型并发布新路由，未变更的 profile 在更新期间继续服务。OAuth token 刷新仅做凭证条件保存，不触发结构重载；退出、删除 OAuth 连接和认证方式切换仍要求模型网关停止，网页退出与删除会自动管理暂停和恢复。
- stop 不承诺等待全部在途 SSE 完整结束。

## 验证

```sh
go test ./...
go test -race ./...
go vet ./...
go build -o bin/cpa ./cmd/cpa
```

测试只使用临时状态/settings 和 mock 上游，不改真实 `~/.claude`，不调用付费模型。真实模型的兼容性和费用需要另外授权验收。

WebUI 与隧道流程可单独复验（需要 Node.js 24 和 Playwright Chromium）：

```sh
npm --prefix frontend ci
cd frontend && npx playwright install chromium && cd ..
./scripts/check-webui.sh
```
