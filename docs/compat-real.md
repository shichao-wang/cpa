# Claude Code 与 Codex 真实短请求验证

本轮经用户授权，在隔离环境中使用 Claude Code 2.1.293、CPA 合并版本 `4639e2e` 和 CLIProxyAPI v8.0.10 访问已有 Codex OAuth 连接的 `gpt-6.1-sol`。共发送 28 次官方推理请求，全部返回 HTTP 200。授权上限为 30 次，剩余 2 次没有使用。

测试只读原状态，将目标连接及凭证复制到临时状态目录。Claude 的 HOME、settings 和工作区均为临时目录。执行后恢复临时 settings、停止测试服务并删除临时目录。各轮结束时原状态文件的摘要均未变。没有执行账号登录或 token refresh。

## 已确认结果

| 能力 | 实际证据 | 结论 |
| --- | --- | --- |
| 默认参数 | Claude 完成单轮，出站 effort 为 high | 默认短对话通过 |
| low / high | 出站 reasoning.effort 分别为 low / high，客户端收到预期标记 | effort 映射通过 |
| 关闭 thinking | Claude 设置 alwaysThinkingEnabled=false 后，上游 effort 为 low | 兼容映射，不能理解为完全禁止推理 |
| Read | 上游返回 Read 调用，下一轮收到 function_call_output，客户端返回文件标记 | 工具闭环通过 |
| Bash | 模型调用 Bash 执行 printf，下一轮收到工具结果并返回标记 | 工具闭环通过 |
| plan 模式 | Claude 在 plan 权限模式下调用 Read，并返回文件标记 | 只读工具访问通过，未证明 plan 转实施及持久会话 |
| JSON Schema | Claude --json-schema 调用 StructuredOutput，客户端输出匹配约束的 marker 对象 | CLI 结构化输出通过，路径使用工具调用 |
| API 图片输入 | 直接调用 CPA /v1/messages，发送合成的 64×64 纯红 PNG，模型返回颜色验证标记 | API 图片输入通过 |
| CLI 图片读取 | Claude 调用 Read 读取同样的合成 PNG，完成工具闭环并返回颜色验证标记 | CLI Read 图片路径通过，未验证粘贴或附件交互 |
| 缓存 | 子任务续轮的 cached_tokens 分别为 1408 和 8960，Claude 返回 cache_read_input_tokens=8960 | 上游缓存 usage 可映射到 CLI |
| 子 Agent | 父模型调用 Agent，haiku 槽位子模型调用 Read 后回传结果，父模型汇总标记 | 非 bare 隔离模式下委派闭环通过 |

## 观测与验收修正

首轮使用了 10 次请求。默认、low、high 和关闭 thinking 的短对话通过。Read、plan Read、Bash 均完成两轮且收到预期标记，但测试观测器只检查 `response.completed.output`，没有检查官方返回的 `response.output_item.done` 工具事件，所以把三个工具测试误判为失败。原证据保留在 `/private/tmp/cpa-compat-real.json`。修复后的观测器已用模拟官方事件的单元测试验证。

第二轮只运行指定补证项，新增 12 次请求。Read、plan Read、Bash 的工具名称和下一轮工具结果均已记录。JSON Schema 和直接 API 图片测试也通过。原证据保留在 `/private/tmp/cpa-compat-real-round2.json`。两次相同短请求的 cached_tokens 均为 0，不能据此判定缓存不支持。

第二轮的 subagent 项曾因验收条件过宽而记录 `passed=true`。回读请求后发现，模型直接调用 Read，未发生 Agent 调度，因此该项判定无效。原证据保留，单独评估记录在 `/private/tmp/cpa-compat-real-assessment.json`。

独立 mock 探查表明，bare 测试模式没有暴露 Agent 工具。切换为非 bare 后，使用临时 HOME、空 settings 来源、严格 MCP 隔离和拒绝外部网络的子进程代理，Agent 和 Grep 可见。`scripts/compat/agent_mock.py` 随后完成 Agent、子模型 Read 和父模型汇总的 mock 闭环。模型入站顺序为 sonnet、haiku、haiku、sonnet。该限制属于测试模式，不能解释为产品不支持子 Agent。

第三轮在同等非 bare 隔离环境下只执行一次真实子任务，新增 4 次请求。入站模型顺序与 mock 一致，出站均为 `gpt-6.1-sol`，工具事件包含 Agent 和 Read，父模型收到预期标记。该轮还观测到上游缓存命中。证据位于 `/private/tmp/cpa-compat-real-round3-agent.json`。

第四轮只补充真实 CLI Read 合成图片，新增 2 次请求。客户端成功读取图片并返回颜色验证标记。证据位于 `/private/tmp/cpa-compat-real-round4-cli-image.json`。本轮未再执行真实请求。

## 预算与记录

第二轮从首轮已消耗的 10 次开始计数。每次真实发送前，测试获取文件锁并原子更新预算文件 `/private/tmp/cpa-compat-real-ledger.json`。文件最终记录为 `used=28`。达到 30 后测试拒绝继续发送，失败请求也占用预算。启动时给定的已用数量必须与预算文件一致。

| 分轮 | 实际请求 | input_tokens | output_tokens | reasoning_tokens | cached_tokens |
| --- | ---: | ---: | ---: | ---: | ---: |
| 第一轮 | 10 | 5413 | 202 | 0 | 0 |
| 第二轮 | 12 | 6659 | 387 | 99 | 0 |
| 第三轮 | 4 | 21546 | 242 | 38 | 10368 |
| 第四轮 | 2 | 1238 | 69 | 0 | 0 |
| 合计 | 28 | 34856 | 900 | 137 | 10368 |

表格按每次上游返回的 usage 数值求和，没有再次加入 CLI 聚合值。reasoning_tokens 包含在 output_tokens 内，cached_tokens 包含在 input_tokens 内。上述数据不能直接用于推断账户扣费。

测试只放行官方 Codex Responses 地址，关闭后台刷新和配置重试。任何非 200 或传输错误都会终止后续网络请求。测试主动阻止 SDK 对认证错误进行隐式刷新，所以本轮结果不能用于评价正常服务处理 401 或 429 的行为。

证据只保存模型、effort、工具名称及数量、状态、耗时、数值 usage 和预期标记是否出现。原始请求正文、回复正文、凭证和账户 ID 不进入测试报告。运行入口与隔离约束见 `scripts/compat/real.md`。
