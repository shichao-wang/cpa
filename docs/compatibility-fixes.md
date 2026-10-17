# CPA-16～18 修复说明

本轮仅修改本工具的入口、派生能力信息与管理页面，沿用 CLIProxyAPI v8.0.10 的公开 SDK，没有导入 internal 或修改 SDK。

## CPA-16：明确输出预算不执行，并允许严格拒绝

SDK 当前 Codex 转换路径去除输出预算字段。标准 [OpenAI Responses 的 max_output_tokens](https://developers.openai.com/api/docs/guides/reasoning) 支持说明不能用来证明这条路径实际执行上限。

正常请求保留，响应头明确 `X-CPA-Output-Limit: not-enforced`，管理页面和模型目录同样说明。调用方要求执行输出上限时，发送 `X-CPA-Require-Output-Limit: true`，返回中文 400 且不触达上游。没有对 SSE 整段缓冲、根据字数估算 token 或伪造截断结果。

## CPA-17：按上游模型提供一致说明

已知模型的 effort 列表来自 SDK 的公开静态模型接口。模型目录通过 `cpa_capabilities` 返回输出预算、reasoning 映射、thinking 关闭及上下文说明；管理 API 返回每连接的模型能力，前端在完整模型清单与 profile 所选映射中展示。能力是私有配置的派生信息，不写入 schema 3 状态。

未知模型的普通对话保留，但显式 enabled/adaptive thinking 或 effort 在入口拒绝，不再成功后静默丢弃。已知模型的关闭降级、adaptive、budget 优先级以及 effort-only 被忽略均有响应头代码。上下文只标记未经验证，不返回虚构容量；工具提示区分普通函数和托管执行。

## CPA-18：入口校验预算

Responses 推理请求的参数使用原始 JSON 校验，不做字符串到零的转换。校验包括重复字段、thinking/output_config 对象、type/effort 类型、整数范围、预算与输出配置冲突。

- max_tokens：提供时为 1～2147483647 的 JSON 整数。
- budget_tokens：只用于 enabled，提供时为 1024～2147483647 的 JSON 整数，并要求 max_tokens 存在且更大。
- adaptive/disabled 携带 budget 字段会拒绝；enabled 不提供预算仍可使用 SDK 默认映射。
- 合法预算仍只是离散 effort 映射，不承诺 token 上限。

1024 起步及普通预算关系参照 [Anthropic 扩展思考规则](https://platform.claude.com/docs/en/build-with-claude/extended-thinking)。原生 Anthropic interleaved thinking 存在例外，所以新检查只作用于 Responses 兼容请求，不跨协议套用限制。

## 验证

回归涵盖严格预算不触达上游、已知/未知能力、普通未知模型对话、非法类型和边界、映射响应头、模型目录及管理状态派生信息。前端验证模型清单与 profile 选择不同模型后的说明切换。

本轮没有新增真实模型调用。历史测试报告保留修复前的观测；当前行为以本文件、README 和新增回归为准。

最终验证全部通过：

- `go test ./...`、`go test -race ./...`、`go vet ./...` 与独立 binary 构建，日志 `/private/tmp/cpa-fixes-all-{test,race}.log`、`/private/tmp/cpa-fixes-vet.log`。
- 前端构建及 20 项 Playwright 测试，包含模型清单、profile 选择变化后的能力说明与零配置写入断言。
- 真实 Claude Code 对修复后 binary 的默认、thinking 关闭、[1m]、六轮工具与 resume、四轮主子 Agent mock 流程。证据 `/private/tmp/cpa-fixed-{default,thinking-off,1m,tools,agent}/`。所有临时设置恢复、服务停止和目录清理均通过。
- 真实连接状态、管理令牌与 Claude settings 的保护基线未变；真实请求账本仍为上一轮的 28，未新增付费请求。

修复后的测试 binary 为 `/private/tmp/cpa-compat-fixed`，当前运行服务没有重启或替换。
