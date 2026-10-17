# Codex OAuth 真实短请求

仅在用户明确授权真实调用后执行，默认 `go test ./...` 会跳过。单次测试的所有实际上游 RoundTrip 共享 30 次硬上限，包括重试。不要并行运行两份真实测试，否则每份都会持有独立预算。

```sh
CPA_COMPAT_REAL=1 \
CPA_COMPAT_REAL_STATE_DIR=/绝对路径/现有/cpa/状态目录 \
CPA_COMPAT_REAL_OUTPUT=/private/tmp/cpa-compat-real.json \
go test ./internal/gateway -run '^TestCompatRealCodexClaude$' -count=1 -timeout=12m -v
```

要求已有名为 `codex` 的官方 Responses OAuth 连接，已选择 `gpt-6.1-sol`，且有效凭证剩余时间超过 26 小时。测试只读原状态文件，将这个连接及凭证复制到独立临时目录，新增独立 profile/client key。使用真实 Claude Code 的 bare 模式、临时 HOME 和 settings，不修改真实 Claude 配置；结束后恢复临时 settings，停止临时服务并删除临时目录。原状态文件在结束时校验摘要未改变。

准备的短请求包括默认、low、high、关闭 thinking、Read 工具闭环、plan 模式 Read、Bash 工具闭环、JSON Schema、子任务和短重复请求。合成图片需要通过 CPA_COMPAT_REAL_CASES 显式选择 direct-image。工具测试必须观测真实上游的 function_call 及后续 function_call_output，并收到预期测试标记，才算通过。测试文件仅含 `CPA_REAL_OK`，Bash 仅执行 `printf CPA_REAL_OK`。

真实 transport 仅允许 `https://chatgpt.com/backend-api/codex/responses`，逐个计数；每请求 45 秒、每客户端 100 秒超时。关闭后台凭证刷新及配置重试，任何非 200 响应或传输错误立刻熔断并拒绝后续网络调用。非 200 不向 SDK 返回原始认证错误，防止其触发隐式 token refresh；这个测试保护不代表产品正常错误行为，因此不能用它评价 CPA 对 401/429 等错误的兼容性。

证据仅保存 case、真实请求计数、模型、reasoning、HTTP 状态、耗时、usage、工具名称/结果数量，以及客户端是否收到测试标记。不保存 token、账户 ID、请求正文、模型正文或真实原始错误。SDK 日志在本次显式真实测试期间设为 Panic，结束后恢复。真实模型是否选择工具具有不确定性，工具 case 失败应先检查证据，不能直接将其解释为协议转换故障。

后续运行必须用 `CPA_COMPAT_REAL_CASES` 指定需要的 case，并使用 `CPA_COMPAT_REAL_USED` 及 `CPA_COMPAT_REAL_LEDGER` 续接同一轮预算。启动 seed 必须与已有 ledger 一致，不能重置本轮已消耗的请求。当前已消耗 28 次，原始分轮证据必须保留，不重复跑默认和 effort。

子任务验收必须同时看到 Agent 工具事件、指定 haiku 槽位的子模型请求，以及工具返回后的主模型续轮。仅主模型调用 Read 不算子任务通过。本机 bare 模式没有向上游暴露 Agent 工具。先用 agent_mock.py 证明非 bare 的隔离委派闭环，再执行一次真实子任务，已通过 Agent 事件、子模型入站和父模型汇总验收。子进程外部 HTTP(S) 代理指向拒绝连接的 loopback 端口，只有 CPA loopback 走 NO_PROXY。实际执行结果和测试验收修正见 `docs/compat-real.md`。
