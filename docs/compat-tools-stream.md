# Claude Code 工具、流式输出和错误兼容性

> 历史基线：下文保留修复前的实测记录。CPA-16～18 修复后的入口校验与能力提示见 [修复说明](compatibility-fixes.md)。
范围：CPA-7、CPA-8、CPA-9。此报告只包含临时状态和 loopback mock，未使用真实连接、凭证、OAuth 登录或付费调用。

## 复现

```sh
GOCACHE=/private/tmp/cpa-go-build go test ./internal/gateway -run '^TestCompat(ParallelToolsAndResults|FragmentedStreamCancellation|UpstreamErrorBounded|DeadlineCancelsUpstream|SmallOutputLimitObservation|TruncatedStreamObservation)$' -count=1 -v
```

入口为公开 `/v1/messages`，使用临时 profile 的公开 haiku 槽位，映射至 Responses API_KEY 连接。CPA 将该连接编译为锁定 SDK 的 Codex executor，使用 HTTP Responses mock。此路径验证转换和 HTTP 生命周期，不能证明 OAuth 执行器额外请求头、凭证刷新、WebSocket 或真实服务行为。

## 测试内容

- `TestCompatParallelToolsAndResults`：同一响应返回 Read、Grep、Edit、Bash 四个工具，验证名称、ID、JSON 参数不变；下一轮传回四个工具结果，验证 Responses `function_call_output` 的 call_id 和中文 output 配对正确。
- `TestCompatFragmentedStreamCancellation`：mock 每字节写入并 flush SSE，跨 UTF-8 和事件边界传输思考摘要及中文和 emoji；下游未等待上游结束即读到内容；取消后上游 context 结束，紧接着新请求成功。
- `TestCompatUpstreamErrorBounded`：400、401、429、500、503 返回失败 JSON，记录实际下游状态、上游尝试次数和耗时，检查本次调用没有无限重试。
- `TestCompatDeadlineCancelsUpstream`：150ms 客户端期限到达，验证 mock 上游 context 结束。

## 证据边界

Go测试中的四个工具名称是协议测试数据，Go测试没有运行本机工具。另一个隔离CLI脚本实际运行Read、Edit和Bash，结果见下节。客户端已验证同响应多工具结果配对，但尚未测量本机工具的执行时间是否重叠。

测试不会将 mock usage 当作计费证据，不测 SDK 默认长时间退避、真实服务限流窗口、Claude Code 自身重试策略或真实断网恢复；429 和 5xx 仅能证明本测试条件下单请求的有界失败行为。

## 2026-10-17 执行结果

Go 六组测试及错误状态的五个子测试通过 `-race`，本轮耗时 3.919s。此耗时仅用于辨认执行记录，不代表真实服务性能。证据保存在 `/private/tmp/cpa-compat-tools-all-final.log`。

| 测试项 | 观察结果 | 兼容性结论 |
| --- | --- | --- |
| 四个并行工具及结果配对 | ID、名称、JSON参数和第二轮call_id均保留 | 协议转换支持 |
| SSE分片 | 每字节flush后仍可正确接收中文、emoji、思考摘要和工具JSON参数 | HTTP流转换支持 |
| 请求取消 | 下游主动取消后，mock上游context结束，下一请求成功 | 支持 |
| 客户端超时 | 150ms期限传播到上游，网关请求日志为499 | 支持客户端取消传播 |
| 上游错误 | 400、401、429、500、503分别保留相同下游状态，均尝试一次 | 当前默认配置有界返回失败 |
| 小输出预算 | 入站max_tokens=1，出站没有max_tokens和max_output_tokens | 预算参数没有传到Responses上游 |
| 缺失完成事件 | 正常SSE帧后HTTP结束，网关输出error事件，不输出message_stop | 明确报告断流 |

小输出预算是已发现的参数映射限制。测试中 mock 仍返回 output_tokens=9，这个数字不能证明真实服务会生成多少token。需要在推荐配置中注明预算未被传递，并单独评估是否修改公开SDK支持范围。此次只记录问题，没有修改生产代码或SDK。

## 真实 Claude Code 工具闭环

可复现命令如下，脚本复用同目录的基础隔离脚本。

```sh
python3 scripts/compat/claude_tools_mock.py --output /private/tmp/cpa-compat-cli-tools-parallel
```

本轮使用 Claude Code 2.1.293、Go 1.27.1、CLIProxyAPI v8.0.10，CPA binary的Git revision为4639e2eaf52b5212f41cdd47a8ba4ccfb379e67b。模型响应全部来自loopback mock，累计六次Responses请求。脚本将每个SSE字节分别写入并flush，工具参数也经过此路径。

- 首次响应同时返回Read和独立Bash printf调用，客户端下一轮把两个call_id及结果都传回上游。该项验证同响应的多工具结果配对，没有测量两个本机工具的执行时间是否重叠。
- Read读取临时demo.txt，返回compat_before。
- Edit将文件改为compat_after，脚本直接读取磁盘文件核验。
- Bash执行临时目录内的cat demo.txt，返回compat_after。
- 独立Claude进程使用`--resume`恢复该临时会话，继续收到CPA_COMPAT_OK。两个进程均退出成功，无权限拒绝，测试结束后临时状态目录删除，profile apply后restore回读一致。

当前`--bare --tools Read,Grep,Edit,Bash`实际上只暴露Bash、Edit、Read。mock故意调用Grep时，Claude返回`No such tool available: Grep`，并将错误工具结果传回上游。该现象发生在Claude工具注册阶段，尚不能归因于CPA协议转换。Grep在Go协议测试中通过，在本次真实Claude裸模式中不可用。

保留的执行产物如下。

- `/private/tmp/cpa-compat-cli-tools-parallel/summary.json`记录版本、隔离、恢复和清理结果。
- `/private/tmp/cpa-compat-cli-tools-parallel/tools-summary.json`记录工具暴露清单、文件核验和resume结果。
- `/private/tmp/cpa-compat-cli-tools-parallel/requests.jsonl`记录两个请求边界的无鉴权正文。
- `claude-result.json`和`claude-resume-result.json`分别记录两个Claude进程的结果。其cost字段由客户端按模型别名与mock usage估算，本轮没有真实模型计费。

后续补充了真实CLI的SIGINT取消后resume和自动重试间隔，结果见下节。仍未覆盖交互终端按键操作、Claude客户端并发执行的时间重叠、Grep在非bare模式的注册原因，以及真实OAuth服务的断流和预算行为。

## CPA-8 真实CLI取消与恢复补证

```sh
python3 scripts/compat/claude_cancel_mock.py --output /private/tmp/cpa-compat-cli-cancel-final
```

脚本首先让Claude实际Read一个临时文件。下一个mock响应发送CANCEL_PARTIAL首块并保持流打开，脚本在Claude的stream-json输出中确认首块已到达，再通过`Popen.send_signal(SIGINT)`发送中断。该方法验证进程收到SIGINT的语义，没有操作交互终端的Ctrl+C按键。

Claude输出中同时出现`aborted:true`和`[Request interrupted by user]`。该进程自行退出，code为0，没有触发5秒强杀。随后新Claude进程使用`--resume`恢复会话，请求中保留已完成Read工具的call_id和CANCEL_TOOL_HISTORY结果，并成功接收CPA_COMPAT_OK。本轮恰好三个Responses请求，profile restore、服务停止及临时状态删除均通过。

证据位于`/private/tmp/cpa-compat-cli-cancel-final`，包括`cancel-summary.json`、`cancel-stdout.jsonl`、`requests.jsonl`和基础环境`summary.json`。mock侧心跳未可靠观察到TCP关闭，`upstream_closed`为false，因此该CLI测试只证明取消及历史恢复，不据此声称观察到了上游立即断开。上游context取消由前述Go公开接口测试单独证明。

## CPA-9 真实CLI错误重试补证

```sh
python3 scripts/compat/claude_errors_mock.py --case 429 --output /private/tmp/cpa-compat-cli-error-429-final
python3 scripts/compat/claude_errors_mock.py --case 500 --output /private/tmp/cpa-compat-cli-error-500-final
python3 scripts/compat/claude_errors_mock.py --case disconnect --output /private/tmp/cpa-compat-cli-error-disconnect-final
```

每组最多观察三个客户端请求，并设置20秒外层期限。三组均在第三个响应后由测试脚本发送SIGTERM终止，进程exit为143，未触发20秒期限。这是测试的请求预算，不是Claude自行停止重试，不能将其描述为客户端已经耗尽重试次数。

| mock场景 | 客户端请求间隔 | 客户端收到的状态 | 实际上游请求数 | Claude重试事件 |
| --- | --- | --- | --- | --- |
| 429 | 0.563s、1.013s | 429、429、429 | 2 | rate_limit，前三次声明max_retries=10 |
| 500 | 0.512s、1.230s | 500、503、503 | 1 | server_error，前三次声明max_retries=10 |
| 缺少response.completed的断流 | 0.015s、0.544s | 200、408、408 | 3 | 首次流失败后改用非流请求，其后事件声明max_retries=9 |

客户端请求数与上游请求数不同。单独Go错误测试显示每个进入执行器的请求只有一次mock尝试，真实CLI则会重发请求；此外SDK认证冷却会使后续客户端请求未必再次到达mock。500场景的后续503与这一行为一致，但本测试没有读取或修改SDK内部冷却状态。

断流的客户端stream标志依次为true、false、false，转换后的Responses上游始终stream=true。Claude在首个流失败后立即尝试非流，然后对408进行退避。这个行为由两个请求边界同时记录，不是从耗时推测。

每组的`error-summary.json`保留实际间隔、stream标志、状态及api_retry事件。`error-stdout.txt`保留Claude原始stream-json事件，`requests.jsonl`保留两个请求边界。三组的profile恢复、服务停止、临时HOME删除均通过。未继续执行到客户端默认重试耗尽，也未模拟真实服务的Retry-After窗口或恢复成功后的行为。
