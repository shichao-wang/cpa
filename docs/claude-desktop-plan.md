# Claude Desktop 支持方案

调研日期为 2026 年 10 月 18 日。本方案建议通过 Claude Desktop 官方第三方推理模式接入 cpa，首期支持 macOS，复用现有网关和 connection，新增 Desktop 专属 profile 与配置适配。本轮交付为调研和设计，Desktop 兼容性仍需按下文完成实测。

## 官方接入方式

Claude Desktop 从第三方推理配置读取网关地址，不从 `ANTHROPIC_BASE_URL` 或 Claude Code 的 `settings.json` 读取。因此，现有 `profile apply` 不能直接覆盖 Desktop。官方也明确指出，SSH 会话由远端直接访问网关，本机 localhost 地址不适用于远端会话。[Desktop 网关配置说明](https://code.claude.com/docs/en/llm-gateway-connect#desktop-app)

官方入口为 Help → Troubleshooting → Enable Developer Mode，再进入 Developer → Configure Third-Party Inference。可以在应用内填写、测试并应用配置，也可以导出 JSON。首轮验证应使用这个入口生成基准文件。[应用内配置](https://claude.com/docs/third-party/claude-desktop/in-app-configuration)

macOS 用户配置位于 `~/Library/Application Support/Claude-3p/configLibrary/`。目录中的 `<id>.json` 保存配置，`_meta.json` 记录当前应用项。管理配置可能覆盖本地配置；仅设置应用行为的管理策略存在例外。配置变化需要完整退出并重新打开应用。[配置参考](https://claude.com/docs/third-party/claude-desktop/configuration#how-keys-are-read)

网关至少需要提供支持流式输出与工具调用的 `POST /v1/messages`。模型目录可由 `GET /v1/models` 提供，也可显式填写。Desktop 支持静态 key，以及 bearer、x-api-key 两种发送方式。[Desktop 网关协议要求](https://claude.com/docs/third-party/claude-desktop/gateway)

第三方模式会改变推理入口与会话存储方式，不能将普通 Claude 登录模式下的历史自动视为第三方模式的历史。cpa 接管和恢复只处理连接配置，保留两种模式的会话数据。[第三方模式架构](https://claude.com/docs/third-party/claude-desktop/overview#architecture)

## 当前仓库可以复用的能力

| 位置 | 当前行为 | 本次处理 |
| --- | --- | --- |
| `internal/gateway/middleware.go` | 限定 Messages、count_tokens、models 入口，按 profile key 授权并改写结构化模型 ID | 先复用，只有 Desktop mock 实测发现差异才改动 |
| `internal/gateway/runtime.go` | 按连接生成独立路由与认证 | 保持连接隔离和现有 OAuth 生命周期 |
| `internal/config/config.go` | schema 3 已有 `Profile.Agent`，目前只接受 `claude-code` | 增加 `claude-desktop` 合法值 |
| `internal/profile/profile.go` | 草稿校验也只接受 `claude-code`，更新允许修改 agent | 统一 agent 校验，并限制受管 profile 切换 agent |
| `internal/agent/claudecode/claudecode.go` | 单 settings 文件的字段管理、冲突检查和中断恢复 | 保留现有行为，Desktop 使用独立实现和记录 |
| `internal/cli/runtime.go` | apply、restore、删除保护直接调用 Claude Code 适配器 | 增加明确的 agent 分派和跨适配器引用检查 |
| `internal/webui/handler.go` | 管理状态和删除保护直接查询 Claude Code 记录 | 同时识别 Desktop 的有效及待完成管理记录 |
| `frontend/src/profile-editor.tsx` | 创建 profile 不提供 agent 选择 | 新增 Agent 选择和对应的本机应用提示 |

当前代码具备 Desktop 所需的主要 HTTP 入口，不代表真实 Desktop 已通过验证。尤其需要确认 count_tokens 的实际响应、SSE 事件、取消、工具结果和错误是否被客户端正确处理。

## 支持范围

首期以 macOS 上的 Desktop 第三方模式为目标，先验证 Chat 文本和本地 Code 会话，再单独验证 Cowork。只有通过对应实测的界面才列为支持；Cowork 尚未通过时，发布说明应明确标为待验证，避免一次配置成功被解释为所有功能可用。

继续使用四个必填档位 Opus、Sonnet、Haiku、Fable。Desktop 与 CLI 分别创建 profile，各自持有 key，可以指向相同 connection。connection 的协议、凭证引用和模型清单保持当前结构。

首期不覆盖远端 SSH、Anthropic 托管云会话、Windows、企业 MDM 部署和账户历史迁移。Linux 配置适配在 macOS 流程稳定后单独验证。MCP 接入属于工具扩展，不能替代推理网关接入，本次不修改 MCP 配置。

现有 Responses 能力限制继续适用，包括输出预算不执行、thinking 兼容映射和上下文容量未经实测。Desktop 使用 Claude 公开 ID 时，也不能据此声明真实上游拥有对应 Claude 模型的能力。

## 推荐设计

### profile 与命令

保留 schema 3，新增 `agent: claude-desktop`，不新增 provider 或全局默认凭证。现有 schema 3 的 Claude Code 配置继续有效；旧版本 binary 可能拒绝读取包含 Desktop profile 的状态，回退说明需要指出这个边界。

拟提供以下命令，以下语法属于设计，尚未实现：

```sh
cpa profile create desktop-daily --agent claude-desktop --file bindings.yaml
cpa server start
cpa profile apply desktop-daily
cpa profile restore --agent claude-desktop
```

`apply` 根据存储中的 profile.agent 分派，不允许临时覆盖 agent。`restore` 增加 `--agent`，默认仍为 `claude-code`，保持已有命令行为。Desktop 增加 `--config-dir` 以支持临时配置根目录，CLI 的 `--settings` 只用于 Claude Code；不匹配的参数直接报错。

CLI 和 WebUI 应使用同一处 agent 校验。受任一适配器管理或处于待完成操作中的 profile 禁止切换 agent，完成 restore 后才允许修改。删除保护始终按 profile ID 查询全部适配器，不能仅按当前 agent 或名称决定。

### Desktop 配置生成

新增 `internal/agent/claudedesktop/`。从 cpa 当前状态派生独立的 Desktop 配置项，并将其作为本地活动配置。cpa 状态继续是唯一事实源，Desktop 配置属于派生结果。配置文件外层结构和 `_meta.json` 的完整格式须先完成下一节的验证，不能照猜测写入。

拟生成的主要配置键如下。键名来自官方配置契约，值和默认策略为本方案的设计选择。[配置键参考](https://claude.com/docs/third-party/claude-desktop/configuration)

| 配置键 | cpa 派生值 |
| --- | --- |
| `inferenceProvider` | `gateway` |
| `inferenceCredentialKind` | `static` |
| `inferenceGatewayBaseUrl` | 从当前监听地址生成 API 根地址，默认候选为 `http://127.0.0.1:8317` |
| `inferenceGatewayApiKey` | 当前 Desktop profile 的专属下游 key |
| `inferenceGatewayAuthScheme` | `bearer` |
| `modelDiscoveryEnabled` | 首期设为 false，使用明确列表 |
| `inferenceModels` | 从四档绑定派生，Sonnet 排第一作为初始默认 |

模型条目的 `name` 使用公开模型 ID，`labelOverride` 复用 `DisplayBinding` 派生名称并附档位，`anthropicFamilyTier` 对应档位。列表使用完整 ID，避免依赖裸档位别名。首期不自动填写 `supports1m`、`prefer1m` 或推理 effort 能力；后续只能基于已验证能力补充。[模型列表与默认行为](https://claude.com/docs/third-party/claude-desktop/models)

显式列表可能在 OAuth 凭证失效后仍显示模型，但请求必须沿用网关当前的健康检查并拒绝服务。不得为了界面可见性恢复失效认证。Desktop 配置只写下游 key，不包含上游 API key 或 OAuth token。

不自动启用工具权限、放宽沙箱或修改组织策略。Chat、Code、Cowork 对应开关的默认值和最低版本在验证阶段确定。新增配置项中的其他必要字段采用官方生成的最小基准，保留用户原有配置项。

### apply 与 restore

Desktop 涉及多个文件，不能直接复用单文件 settings 的事务。单独保存管理记录，记录 Desktop 配置根路径、profile ID、原活动项、受管字段原值和目标值。记录与备份使用 0700 目录和 0600 文件。

apply 前检查 Desktop 已退出，并确认本地配置没有被组织推理策略覆盖。无法判断配置来源、遇到未知文件格式或符号链接时拒绝写入。仅读取配置结构所需内容，错误与日志不输出凭证。

首次 apply 创建 cpa 专属配置项，保留用户配置。切换 profile 更新该受管项，但恢复基线仍是首次 apply 前的状态。先持久保存 pending 记录，再写配置项，最后更新活动项引用，回读全部受管值后标记 committed。

restore 先检查全部文件和受管字段，发现任一冲突即拒绝开始恢复。确认仍由 cpa 管理后，恢复原活动项并移除仍匹配管理记录的 cpa 配置。用户新增配置项、无关元数据、MCP 和会话历史全部保留。若原活动项被用户删除，报告冲突，不重新制造用户配置。

跨文件写入无法保证一次系统调用完成，因此 pending 记录需要支持在任一写入点中断后恢复。恢复前再次比较原值、目标值和现值；出现第三种值时停止，不能覆盖外部编辑。整个文件操作由适配器锁保护，并在写入前检查文件是否变化；锁顺序保持 state 锁先于适配器锁。

apply 与 restore 均不自动杀死或重启 Desktop。完成后提示用户完整重新打开应用。cpa 网关可以继续服务无关 profile，Desktop 配置写入不触发整个 SDK 重载。

### WebUI 与文档

创建 profile 时提供 Claude Code 和 Claude Desktop 选择；列表显示 agent 和受管状态。编辑受管 profile 时禁用 agent 切换，服务端执行同样校验。受管状态读取失败时按受保护处理，避免删除仍被使用的 profile。

网页只展示本机 CLI 应用命令，保持现有“在运行 Agent 的机器上配置”的边界。README 增加 Desktop 接入、完整重启、组织策略覆盖、恢复方式和上游能力限制，示例不包含 key。

## 实施前必须完成的验证

使用独立 macOS 测试用户或 VM，让 Desktop 自己生成最小配置。只有已证明能隔离 Electron 用户数据时，才可以用临时目录代替独立用户。测试不触碰日常 Desktop 配置、真实 `~/.claude` 或账户登录。

| 验证项 | 方法与通过条件 |
| --- | --- |
| 本地存储契约 | 在官方配置窗口创建、切换、应用、取消应用配置，保存脱敏样本，核对配置外层、ID、活动项及额外模式标志 |
| 本地网关可达 | 使用本地 mock，分别检查 Chat、Code、Cowork 发出的实际请求、HTTP loopback 是否接受、根路径如何拼接；逐项报告结果 |
| 模型选择 | 四档名称、默认模型、tier 别名及内部辅助请求均命中公开 ID；未知模型仍被网关拒绝 |
| 生命周期 | 完整重启后读取预期活动项，恢复后回到原模式；原有会话和配置项保留 |

官方文档足以确定接入方向，但未完整规定本地配置项的外层序列化与所有模式标志，也不能替代目标版本对 HTTP loopback 的实测。若需要其他模式文件，必须把对应受管字段一并纳入事务。若只能通过提升权限、安装证书或扩大监听范围才能接入，应重新评估范围，不能静默扩大 cpa 的默认权限。

## 实施计划与验收

具体子任务、依赖和 PR 边界见 [Claude Desktop 实施计划](claude-desktop-milestones.md)。首期依次交付隔离兼容性验证、CLI 完整接入和 WebUI 管理；Cowork 差异按需作为独立 PR，不阻塞已验证界面交付。每个 PR 同时交付自身测试和使用说明。

配置测试使用临时根目录，至少覆盖新建、重复 apply、profile 切换、两个 agent 同时受管、恢复前基线保留、无关编辑保留、受管冲突拒绝、文件损坏和符号链接拒绝。对每个跨文件写入点注入中断，确认下次操作可以恢复，且 pending 期间保护新旧 profile。

协议测试沿用 mock 上游，覆盖流式文本、工具调用往返、取消、401/429、上下文错误和 token 计数。原生 Anthropic 路径检查缓存字段与 usage 保留；转换路径按实际能力验证。OAuth 失败时仍须停止在途输出，Desktop 接入不能绕过现有保护。

实现完成后执行仓库要求的检查：

```sh
go test ./...
go test -race ./...
go vet ./...
go build -o bin/cpa ./cmd/cpa
npm --prefix frontend run build
```

还需确认前端构建后内嵌资源一致，并运行 Desktop 的独立 mock 验收。当前仅新增本方案文档，以上检查属于后续实施验收，尚未执行。
