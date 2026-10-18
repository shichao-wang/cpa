# Claude Code 隔离兼容性测试

`claude_mock.py` 使用安装在本机的真实 Claude Code 和指定 CPA binary，所有请求都发往临时 loopback mock，不读取已有连接、凭证或 Claude 登录。

```sh
go build -o bin/cpa ./cmd/cpa
python3 scripts/compat/claude_mock.py --output /private/tmp/cpa-compat-default
python3 scripts/compat/claude_mock.py --output /private/tmp/cpa-compat-low --effort low
python3 scripts/compat/claude_mock.py --output /private/tmp/cpa-compat-disabled --settings-json '{"alwaysThinkingEnabled":false}'
```

可使用 `--cpa /绝对路径/cpa`、`--model claude-sonnet-4-6`、`--target-model gpt-6.1-sol`、`--effort high` 或 `--settings-json` 组合构造实验。每次使用独立输出目录。运行环境需允许 Python、CPA、Claude Code 创建本机 loopback 监听及其子进程；受限沙箱可能需要本机执行权限。

脚本创建临时 HOME、CLAUDE_CONFIG_DIR、settings、工作区及 CPA 状态。通过 CLI 创建 Responses API_KEY mock 连接和 profile，四个槽位默认绑定 SDK 已识别的 gpt-6.1-sol，应用后回读 settings。Claude 使用 `--bare --setting-sources '' --strict-mcp-config --tools '' --no-session-persistence`，禁用非必要网络流量；bare 模式不读取 keychain/OAuth。为适配 bare 的鉴权契约，脚本将 apply 生成的临时 profile token 转为子进程的 ANTHROPIC_API_KEY，并将 BASE_URL 指向观测代理。该差异写在脚本中，不改变 apply 产物。没有真实 OAuth 登录、账号能力或上游额度验证。

链路：真实 Claude → loopback 观测代理 → 真实 CPA → Responses mock。mock 始终回复 `CPA_COMPAT_OK`，因此这条基线证明客户端/协议/路由可以完成一轮对话，不证明模型真实推理或工具调用能力。脚本结束时 restore 并回读原设置、停止临时 CPA、删除临时状态及临时凭证。

输出目录只保留：

- `summary.json`：Claude 版本、CPA 构建版本与二进制摘要、隔离参数、apply/restore 回读、清理结果。
- `requests.jsonl`：同一 run_id 下两侧请求正文、白名单 header、请求时刻，以及返回状态与耗时。没有 Authorization 或 API key；基线为串行单轮，可按记录顺序关联。请求正文含测试 prompt 和客户端系统提示，不应将此记录器指向真实会话。
- `claude-result.json`：真实客户端的 JSON 结果与模拟 usage。模拟 usage 及费用字段不能用来推断真实计费。

脚本失败会返回非零，清理放在 finally 中；只有完成双侧观测、预期回复与 settings 回读才应将此基线记为通过。运行前未触碰真实 `~/.claude`，也不使用当前 `127.0.0.1:8317` 服务。

`agent_mock.py` 另行验证非 bare 的 Agent 调度。它沿用临时目录和 mock，子进程外部 HTTP(S) 代理拒绝连接，仅允许 loopback。运行 `python3 scripts/compat/agent_mock.py --output /private/tmp/cpa-compat-agent --target-model gpt-6.1-sol`，会验证 sonnet 父模型调用 checker、haiku 子模型执行 Read、父模型汇总的四轮流程。该脚本不调用真实上游。

## 长上下文与压缩

`claude_long.py` 由 Go harness 启动，不直接读取真实凭证。默认 `go test ./...` 会跳过该集成测试；预算单测仍会执行。先使用 mock 验证客户端流程：

```sh
CPA_LONG_MODE=mock CPA_LONG_CASE=manual \
CPA_LONG_LEDGER=/private/tmp/cpa-long-mock-budget.json \
CPA_LONG_OUTPUT=/private/tmp/cpa-long-mock-manual.json \
go test ./internal/gateway -run '^TestCompatLongContext$' -count=1 -v
```

`CPA_LONG_MODE=mock-recovery` 会对第一次手动压缩注入400，随后恢复服务并允许重试；`CPA_LONG_CASE=auto` 检查192K合成输入在固定200K窗口中的自动压缩。mock不能证明模型真实召回质量。

真实模式需要另行授权，并显式指定 `CPA_LONG_MODE=real`、只读 `CPA_LONG_SOURCE`、独立 `CPA_LONG_LEDGER` 和 `CPA_LONG_OUTPUT`。用例白名单为 `long-{16,32,64,128,192}-{a,b}`、`manual`、`auto`、`resume`。`CPA_LONG_DIAGNOSE=1` 仅检查手动压缩与恢复，不执行后续文件任务。账本上限为20次请求、200万输入预留及5万输出警戒线；所有压缩和工具续轮统一计数。必须续接原账本，不得重置次数或通过新文件绕过已授权预算。本轮真实账本已经使用20/20，禁止继续调用。

客户端成功或HTTP200不足以判定压缩成功，必须同时检查会话 compact_boundary、压缩后输入量和事实保留。当前手动 print/resume 异常及完整范围见 [最新报告](../../docs/compatibility-report.md)。

### CPA-19 本地回放与持久化

手动压缩测试默认在同一 Claude 进程内按 stream-json 输入连续完成 seed、/compact 和后续一轮，关闭输入并等待退出后再独立恢复。默认不再为 /compact 单独启动并立即结束一个 print 进程。固定版本2.1.293的该短进程路径存在间歇性持久化问题，直连Anthropic mock也能复现；改变输出格式或添加flush环境变量不足以解决。

以下命令不需要真实凭证，所有请求都发往临时 loopback mock。运行前需安装 Claude Code，并允许临时本机监听与子进程：

```sh
CPA_LONG_MODE=mock CPA_LONG_CASE=manual CPA_LONG_MOCK_TASK=1 \
CPA_LONG_MOCK_REPLAY="$PWD/scripts/compat/fixtures/manual-summary.txt" \
CPA_LONG_LEDGER=/private/tmp/cpa19-mock-budget.json \
CPA_LONG_OUTPUT=/private/tmp/cpa19-local.json \
go test ./internal/gateway -run '^TestCompatLongContext$' -count=1 -v
```

`CPA_LONG_MOCK_TASK=1` 验证压缩后的Read/Edit/Bash、文件内容和保护约束；默认mock只验证压缩、事实与独立恢复。`CPA_LONG_DIRECT_MOCK=1` 绕过CPA与SDK的推理链路，直连Anthropic mock做对照。`CPA_LONG_PERSISTENT=0` 仅用于重现旧短进程问题，可能间歇失败；`CPA_LONG_COMPACT_FORMAT=json` 或 `stream-json` 可对比输出格式，不能当作持久化修复。`CPA_LONG_MOCK_REPLAY` 接受脱敏 .txt fixture 或前一轮 .gateway.json，不读取账户凭证。

判定必须同时包括会话文件的compact_boundary、后续请求体积下降、独立进程恢复与事实保留，不能只依赖HTTP200、local_command成功或内存中的压缩事件。该修复仅调整测试驱动，没有改用户settings或外部客户端。真实补验仍需新预算，不得重置已耗尽的账本。

### 手动压缩补验预算与交互终端

用户另行授权后，`CPA_LONG_BUDGET=manual-supplement` 选择30次请求、80万输入、2万输出预算；账本记录授权上限，拒绝使用不同授权配置续接。每次输入按token估计加20%和2,048预留；输出按客户端单轮8,192预留，额度不足就拒绝下一轮。不能从缓存或Claude显示的美元费用推断OAuth实际扣费。

`CPA_LONG_INTERACTIVE=1` 使用真实PTY交互终端，处理临时目录的主题、测试profile key确认和目录信任，随后检查会话文件的压缩、工具及恢复结果。只允许在测试临时HOME运行，不使用真实Claude设置。会话观测回归使用 `PYTHONDONTWRITEBYTECODE=1 python3 scripts/compat/test_claude_long.py`。

本轮真实账本 `/private/tmp/cpa12-supplement-real-ledger.json` 已因取消请求缺少usage而熔断：23/30次，已结算524,106输入、4,617输出，还有1次消耗未知。不得清除停止标记、丢弃输入预留或重置账本继续。两轮stream-json真实流程通过，交互真实流程只通过压缩持久化，修复后的整轮仅mock通过。后续真实验收需新预算确认。

### 真实交互补验已完成

另经用户授权，`CPA_LONG_BUDGET=interactive-final` 使用12次请求、30万输入和1.2万输出预算，并使用新的独立账本。预算准入回归覆盖请求数、输入预留、输出预留、熔断、授权不匹配等拒绝路径。

本轮账本 `/private/tmp/cpa12-interactive-final-real-ledger.json` 实际使用10次，输入213,809、输出1,581，所有请求HTTP200并取得usage，reserved_input为0。完整PTY交互验证了手动压缩持久化、事实召回、Edit/Bash继续任务、保护约束、正常退出和独立resume。CPA-12已具备完成证据。不继续使用剩余2次。历史23/30账本的停止标记及未知消耗保持原样，不能以本轮通过覆盖历史失败。完整范围和证据见最新报告首节。

### 容量拒绝与报告验收

`TestCompatContextRejectionAndRecovery` 使用24K mock阈值覆盖流式/非流式的16K成功、32K拒绝、16K恢复；同时断言原始正文没有被静默截断。24K仅是测试阈值，不能用于Codex容量声明。真实失败边界仍须新预算。

当前综合报告见 `docs/compatibility-report.md`，旧轮记录保留在 `docs/compatibility-history-2026-10-17.md`，数值证据摘要见 `docs/compatibility-evidence.json`。输出8,192预留只是测试准入估算，上游不执行该硬上限；输出警戒线以返回usage结算，不能保证在途请求绝不超出。

### CPA-11扩展容量探测

新授权使用 `CPA_LONG_BUDGET=capacity-boundary`，仅允许 `capacity-256-a` 至 `capacity-896-b` 范围内的显式档位。上限10次请求、800万输入、2万输出警戒线；与历史轮次独立记账。每个用例最多触达一次上游，CLI使用[1m]显示、关闭自动压缩和客户端重试，不将这些客户端设置当作上游容量证明。

容量用例校验完整用户文档与召回指令的SHA256；身份、日期、临时CWD等客户端附加提示单独计入出站输入。首轮256K因观测范围包含附加提示而误判失败，原始记录保留；修正后在预算内独立重复补证。

只有HTTP400且明确`context_length_exceeded`属于计划内容量拒绝。没有usage时保守保留输入估算及8,192输出未知消耗，计入后续准入，不能按零结算；其他错误或成功响应缺少usage会熔断。`TestCapacityBudgetAccounting`验证这些分支及每用例禁止额外请求，`TestCapacityBudgetAdmission`验证各类预算拒绝，`TestCapacityInputObservation`验证客户端附加提示不影响文档完整性校验。

当前真实账本 `/private/tmp/cpa11-capacity-real-ledger.json` 已使用6/10次，输入2,978,724、输出264，全部HTTP200。成功下界为896,456输入token，完整文档与事实召回通过；授权范围内没有测出拒绝上界。任何超过896K的单次探测须先取得新的范围确认，不能因为还有总预算就自动扩大单次上限。

### 2026-10-17按官方规格收尾

用户要求先查OpenAI模型资料后，已核对官方gpt-6.1-sol页面：上下文总窗口1,050,000、最大输出128,000 token。推理与输出均需要上下文空间。规格来源为 https://developers.openai.com/api/docs/models/gpt-6.1-sol 和 https://developers.openai.com/api/docs/guides/reasoning 。

CPA-11按官方规格、896,456输入的兼容实测及mock超限处理完成评估，不再付费寻找已公布的模型上限。账户实际拒绝点未实测，不伪装成已测。容量轮次停在6/10，1152K扩展未批准、未执行，剩余4次不继续使用。CPA-15报告据此收尾；历史“待上限确认”的文字仅为过程记录，以综合报告最新结论为准。
