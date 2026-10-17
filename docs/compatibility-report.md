# Claude Code 使用 Codex profile 的兼容性评估

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
