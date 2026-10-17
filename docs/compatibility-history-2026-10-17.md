# Claude Code 使用 Codex profile 的兼容性评估

## CPA-12 真实交互验收完成（2026-10-17）

修复文件扫描竞争后的真实PTY交互整轮通过。客户端为Claude Code 2.1.293，公开模型claude-sonnet-4-6，经隔离CPA profile路由到官方Codex OAuth连接的gpt-6.1-sol，effort为low。临时settings显示200K窗口，不将该显示值当作已测最大模型容量。

本轮授权最多12次请求、30万输入和1.2万输出token，实际10次，全部HTTP200且usage完整：输入213,809、输出1,581（按上游报告计数，输入包含缓存读取）。预算账本reserved_input为0，没有熔断；不继续消耗剩余2次。旧23/30熔断账本和其中未知消耗保留，不与本轮重新结算或抵扣。

| 验收项 | 实际结果 |
| --- | --- |
| 真实交互/compact | 会话文件持久化manual compact_boundary，preTokens49,096→postTokens1,664 |
| 压缩后召回 | 首、中、尾三个随机事实全部准确；实际上游输入2,472 |
| 继续未完成任务 | Read历史及待办保留，真实Edit/Bash完成fixture.txt修改与验证，protected.txt未变 |
| 正常退出和独立恢复 | 退出码为0，新进程resume后事实召回准确；上游输入2,887，没有重带原49K文档 |
| 路由与输出 | 全部10个上游请求均为gpt-6.1-sol、官方Codex Responses入口；没有循环压缩 |

证据：`/private/tmp/cpa12-interactive-final-real.json`、对应`.gateway.json`和`/private/tmp/cpa12-interactive-final-real-ledger.json`。其前置完整mock和新预算准入回归也通过，证据`/private/tmp/cpa12-interactive-final-mock.json`。

CPA-12可标记Done：已有192K真实自动压缩通过、两轮真实同进程手动压缩通过、本轮真实交互与恢复通过；压缩失败恢复、开关与阈值使用本地/mock覆盖。范围限当前版本及上述用例，不表示所有长会话或模型容量均已验证。CPA-19中的外部客户端独立短print/resume路径仍存在已记录限制，没有修补外部Claude客户端。

本轮go test ./...、go test -race ./...、go vet ./...、go build、Python文件扫描回归均通过。真实CPA配置、凭证和Claude settings的校验值保持不变，生产服务未重启。CPA-11的最大容量/上游超限边界及CPA-15最终报告验收仍未完成，整个项目不标记Done。以下为历史记录，状态以本节为准。


## CPA-12 手动压缩真实补验（2026-10-17）

本轮授权为30次真实请求、80万输入和2万输出token。已执行23次，其中22次取得HTTP200及完整usage，1次在测试驱动异常退出后被取消，没有取得usage。已结算输入524,106、输出4,617；未结算请求保留5,238输入预留，不能按零消耗计入。账本 `/private/tmp/cpa12-supplement-real-ledger.json` 已熔断，旧20/20账本未修改。本轮未完成全部验收。

| 流程 | 结果 | 实际请求与用量 |
| --- | --- | --- |
| 同进程手动压缩第一轮 | 通过。持久化边界48,729→1,630，事实、工具待办、保护文件和独立恢复均通过 | 9次，输入162,637、输出1,567 |
| 同进程手动压缩第二轮 | 通过。持久化边界48,735→1,879，同样完成任务及恢复 | 9次，输入163,822、输出1,780 |
| 真实交互终端 | 部分验证。种子及Read完成，手动压缩持久化49,093→1,601；后续任务和独立恢复未验收 | 5次，其中4次已结算输入197,647、输出1,270，1次消耗未知 |

交互测试的失败来自Python驱动递归扫描临时HOME：Claude删除history.jsonl.lock目录时，Python3.9 pathlib.rglob抛出FileNotFoundError。驱动退出取消了正在进行的召回请求。已将观测范围限制为projects/*/<session-id>.jsonl，忽略尚未完成的JSONL尾行和读前消失的会话文件。回归测试覆盖锁目录反复创建、删除和尾行补全，不启动模型。交互mock整轮通过，但不能替代修复后真实交互验收。没有修改外部Claude客户端或生产网关逻辑。

真实证据：`/private/tmp/cpa12-supplement-real-{a,b,interactive}.json` 及对应 `.gateway.json`。mock证据：`/private/tmp/cpa12-interactive-mock8.json`、`/private/tmp/cpa12-recovery.json`。交互mock覆盖主题/API key/临时工作区信任初始化、PTY输入、压缩、Edit/Bash、保护约束、正常退出和独立resume。此前未完成初始化的mock尝试不计为通过。

本地检查：会话观测Python回归、预算准入测试、mock故障恢复、go test ./...、go test -race ./...、go vet ./...和go build均通过。首次受限沙箱运行因进程身份检查受限而失败，取得必要本机执行权限后重跑通过。真实配置、凭证、Claude settings校验值保持不变，未更新或重启生产服务。

CPA-12保持In Progress：真实交互整轮仍需重新验收。剩余请求数为7，且存在未结算消耗，不能清除熔断或重置账本继续。后续真实执行需要重新确认预算。CPA-11仍未覆盖最大窗口和上游超限边界，CPA-15报告尚未最终验收，整个项目不能标记完成。以下内容保留历史记录，状态以本节为准。


## CPA-19 本地排查与测试驱动修复

2026-10-17，在 Claude Code 2.1.293 上使用脱敏摘要和临时 mock 复现，不新增真实模型调用。原生 Anthropic mock 绕过 CPA 与 SDK 后仍能复现，确认问题属于客户端的单独 print 本地命令退出/恢复路径，而非已经证实的 CPA 转换缺陷。

分别对 CPA/Responses mock、直连 Anthropic mock，以及 json/stream-json 输出做重复对照：最终使用唯一响应ID和正确消息顺序的24次对照中，有6次没有持久化压缩边界。失败的 stream-json 仍已发出 compact_boundary 事件，但会话文件和下一次恢复未采用压缩结果，说明“压缩完成事件”也不是持久化完成证明。临时强制 eager flush 的中间对照也不能稳定解决，未采用此方案。测试不修改客户端二进制、真实settings或SDK。

仓库测试驱动改为同一 Claude 进程中按 stream-json 输入执行 seed、/compact、后续召回；随后关闭输入并等待正常退出，再开独立进程恢复。输出格式用于观测，不作为修复本身。修复流程在两种链路各重复10次，共20次均保留压缩边界，通过压缩后的Edit/Bash、保护约束检查和独立恢复召回。另验证压缩后实际请求体积降至初始的四分之一以下，完成临时文件的 Edit/Bash 与保护约束，mock的400压缩失败后恢复重试和自动压缩回归也纳入验证。

需要区分结论：CPA-19 的定位和本地测试驱动修复完成；Claude Code 原始单独短进程调用路径仍有限制，本工具没有修补外部客户端。CPA-12 尚未做修正流程的真实补验和交互式手动压缩验收，不能标记整个兼容项目完成。真实账本仍为20/20，输入1,455,735、输出4,212，禁止因本地修复自动重跑真实请求。

可复现入口和回放说明见 scripts/compat/README.md；固定回放材料 scripts/compat/fixtures/manual-summary.txt 只保留合成事实，已移除原随机值和运行时路径。最终对照证据 /private/tmp/cpa19-confirm-matrix.json，各用例 /private/tmp/cpa19-confirm-*.json 及 .gateway.json。此前响应ID和消息合并处理尚未完善的mock对照仅保留作排查记录，不用于最终统计。下面保留上一轮发现问题时的记录，阻塞项状态以本节和Linear最新状态为准。

## 2026-10-17 长上下文与压缩补测

用户另行授权最多 20 次真实请求，累计输入预留 200 万 token，输出 5 万作为停止后续请求的警戒线，不探测 256K～1M。本轮已完成并停止新增真实调用，独立账本为 `/private/tmp/cpa-long-real-ledger.json`。

实际发送 20 次官方 Codex 请求，全部 HTTP 200；累计输入 1,455,735 token、输出 4,212 token，其中 cached input 为 289,280 token，已经包含在输入总数内。输出包含 reasoning，不重复相加。这些是上游 usage，不是 OAuth 订阅额度百分比或实际美元扣费。

| 项目 | 本轮结果 | 证据边界 |
| --- | --- | --- |
| CPA-11：16K/32K/64K/128K | 各两组独立随机事实，首、中、尾召回全部准确 | 8 次实际请求，固定 low effort，合成重复填充 |
| CPA-11：192K | 单组输入 192,452 token，三个事实全部准确 | 1 次请求；只证明成功下界，不代表最大容量或复杂长文质量 |
| CPA-12：自动压缩 | 真实通过，192,502→1,555 token；下一上游请求输入 1,741 token并准确召回 | 3 次请求；显式开启 autoCompactEnabled 并固定 200K 窗口，没有降低百分比阈值；会话记录 trigger=auto |
| CPA-12：手动 /compact | 分进程 print/resume 方式两次未通过 | 各4次请求，先完成 Read 工具往返；命令返回成功且上游有摘要，但没有 compact_boundary，后续仍携带约48K原历史 |
| 压缩失败恢复 | mock注入400后恢复并重试通过 | 不消耗真实请求，不等同于真实账户故障恢复 |

手动压缩第二次捕获的真实摘要包含完整 analysis/summary 标签，尚不能归因于摘要格式或 CPA 转换。已创建 [CPA-19](https://linear.app/shichao-wang/issue/CPA-19) 调查该路径，阻塞 CPA-12 的完整验收。交互式 /compact、压缩后 Edit/Bash 完成待办和更长会话仍未验收，不能报告为全部通过。

128K 最初使用长字符填充时，Claude Code 本地返回 Prompt is too long，没有触达上游或消耗请求次数。换成每 token 字符更短的填充后，同档真实请求成功。这是客户端预估与实际上游 token 的区别，不能作为 Codex 容量上界。自动压缩过程中还有一次输入 193,761 token 的摘要请求成功，但本轮没有测出实际最大容量或上游超限边界。

真实证据按用例保留在 `/private/tmp/cpa-long-real-*.json` 及 `.gateway.json`，包括失败的 manual 和 manual-repeat，未覆盖先前失败证据。Go 测试工具在 `internal/gateway/compat_long_test.go`，客户端驱动在 `scripts/compat/claude_long.py`。真实调用默认关闭，账本在上游入口准入并逐次结算，所有续轮均计数；进程中断、未知 usage、请求数或 token 预算不满足时拒绝继续，同一账本禁止多个进程同时运行。

本轮使用临时状态、settings 和工作目录，来源连接配置、API key/OAuth 凭证、管理令牌及真实 Claude settings 的文件校验与测试前一致。当前 PID 63182 的原有服务持续运行，没有重启或替换程序。

## 前一轮短请求历史

> 历史基线：下文保留修复前的实测记录。CPA-16～18 修复后的入口校验与能力提示见 [修复说明](compatibility-fixes.md)。
日期：2026-10-17。项目：[Claude Code × Codex 兼容性验证](https://linear.app/shichao-wang/project/claude-code-codex-兼容性验证-db72f60a0b23)。

测试版本为 CPA `4639e2eaf52b5212f41cdd47a8ba4ccfb379e67b`、CLIProxyAPI v8.0.10、Claude Code 2.1.293、Go 1.27.1、macOS 27.0 arm64。真实请求使用现有 Codex OAuth 连接中的 `gpt-6.1-sol`。

## 已确认的行为

| 能力 | 测试结果 | 证据范围 |
| --- | --- | --- |
| 模型绑定 | 三公开 ID、三个槽位别名、交互切换及重启选择正确 | 真实 CLI 与 mock；HTTP 鉴权和连接隔离回归 |
| effort | low/medium/high/max 可以转换；官方上游接受 low/high | 真实 CLI、双侧参数及官方短请求 |
| 配置优先级 | 环境变量高于 CLI，CLI 高于 settings | 四组真实 CLI 与 mock |
| thinking | disabled 转 low；adaptive 省略 effort 转 xhigh | 兼容映射，不能宣称完全关闭推理或 Anthropic 原生 adaptive |
| budget | enabled 下预算优先于 effort，映射为离散档位 | mock；非法字符串会被当作 0，已记录缺口 |
| 上下文 | 默认显示 200K，[1m] 显示 1M；本地计数可用 | 数值来自客户端，不证明 Codex 实际容量 |
| 工具 | Read/Edit/Bash、多工具结果配对和恢复可用 | mock 执行临时文件操作；真实 Codex 验证 Read/Bash；普通模式可暴露 Grep |
| 流式输出 | 逐字节 SSE、Unicode、工具参数和思考摘要转换可用 | mock HTTP 行为测试 |
| 取消与恢复 | SIGINT 后自行退出，resume 保留已完成工具结果 | 真实 CLI 与 mock；取消传播另有 Go 测试 |
| 失败重试 | 429/500/断流均观察到客户端重试 | 每组在第三次响应后主动终止，未验证完整重试耗尽 |
| Plan | 只读模式 Read 往返可用；Plan 到实施保留计划且修改、回读正确 | 真实官方验证只读模式；完整客户端流程用 mock 验证 |
| 子 Agent | Agent 委派、haiku 子模型路由、Read 与主会话汇总通过 | 真实官方四轮；普通模式，bare 模式不提供 Agent |
| JSON Schema | StructuredOutput 工具及 schema 约束对象通过 | 真实 CLI 与 Codex |
| 图片 | 合成纯色 PNG 识别通过 | 公开 Messages API 和真实 CLI 的 Read 图片路径 |
| 缓存 | 短重复前缀未命中；后续子 Agent 会话有非零缓存读取 | 真实 usage；mock 另验证流式与非流式字段映射 |
| Anthropic 服务端工具 | web_search 部分映射；fetch/code_execution 变成同名 function | mock 出站观测，不具备原生 Anthropic server tool 语义 |

## 需要处理的缺口

- CPA-16：Codex 路径没有将 `max_tokens` 形成出站输出上限。短请求数和超时单独限制，不能依赖该字段控制消耗。
- CPA-17：SDK 未识别的模型会丢弃 reasoning；disabled、adaptive 和上下文容量需要准确的能力说明。
- CPA-18：非法 thinking budget 字符串被转换成 0，预算与输出上限冲突也没有明确拒绝。

这些任务的测试结果是对当前行为的评估，不代表缺口已经修复。本轮没有修改生产实现或 SDK。

## 真实调用预算与观测修正

用户授权最多 30 次真实短请求，不进行大规模长上下文测试。本轮四次分组运行共 28 次实际官方请求，全部 HTTP 200，预算账本为 `/private/tmp/cpa-compat-real-ledger.json`。已停止新增真实调用，剩余 2 次未使用。

首轮观测器漏读官方工具事件，误判了三个工具用例。修正后以 mock 单测验证观测器，再仅补跑工具用例，已确认调用与结果往返。第二轮子 Agent 的宽松断言曾把主模型直接 Read 算成成功；该判定无效，独立 assessment 已明确撤销。随后确认 bare 模式不暴露 Agent，而隔离的普通模式可以。第三轮四次实际请求完成 Agent→haiku Read→主会话汇总；第四轮两次请求验证 CLI 读取图片。所有分轮原始证据保留，未覆盖先前的错误判定。

真实测试仅复制目标连接与凭证到临时状态目录，不登录、不刷新凭证。每次发送前以文件锁和原子写入维护跨运行计数。测试仅放行官方推理入口，非 200 或传输错误触发停止。此测试保护机制不用于评价产品正常的认证错误和重试行为。

## 尚未验收的范围

- 大规模长上下文容量、长文召回及压缩，属于 CPA-11/12，本轮范围明确不执行。
- 真实 Codex 自主规划和复杂任务质量没有评测。完整 Plan 到实施的证据来自真实客户端与 mock，见 CPA-13。
- Anthropic 专属服务端工具的生命周期、实际搜索、沙箱执行，以及缓存 TTL/稳定命中率没有评测，见 CPA-14。
- 图片已验证文件 Read 路径，没有操作交互界面的粘贴附件。
- CPA-1～10、CPA-13、CPA-14 已完成评估。CPA-11/12 保留待办，完整报告 CPA-15 在 In Review，待长上下文结果补齐后验收。

## 复跑与详细证据

- [基础隔离脚本](../scripts/compat/README.md)：临时 HOME、settings、状态、模型绑定及 apply/restore。
- [参数与上下文报告](compat-parameters.md)：28 个参数用例、配置优先级、reasoning 映射及容量来源。
- [工具、流与错误报告](compat-tools-stream.md)：工具文件核验、SIGINT、resume 及重试观察。
- [Plan 到实施报告](compat-plan.md)：两进程、五轮 mock 的只读计划与实施恢复。
- [专属能力与缓存报告](compat-extras.md)：服务端工具边界及非零缓存字段映射。
- [真实短请求报告](compat-real.md)：官方请求、观测修正、预算和白名单记录。
- [真实测试入口说明](../scripts/compat/real.md)：默认跳过真实测试，必须显式启用和指定来源。

最终 `go test ./...`、`go test -race ./...`、`go vet ./...`、全部 Python 脚本语法与临时 binary 构建均通过。最后一轮显式设置 `CPA_COMPAT_REAL=0`，未新增真实调用。日志为 `/private/tmp/cpa-compat-final-{test,race,vet}.log`。真实 `state.json`、管理令牌与 Claude `settings.json` 的保护基线均保持不变。没有替换当前运行的 binary 或重启现有服务。

推荐保持已验证的模型映射，使用明确的 effort 档位。thinking 关闭按低档兼容映射理解，`[1m]` 在真实容量验证前不能作为容量承诺。调用方不能依赖当前 Codex 路径的 `max_tokens` 限制输出，相关处理见 CPA-16。缓存命中已有正面证据，但命中率与 TTL 仍须单独评估。
