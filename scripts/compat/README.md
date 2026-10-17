# Claude Code 隔离兼容性测试

`claude_mock.py` 使用安装在本机的真实 Claude Code 和指定 CPA binary，所有请求都发往临时 loopback mock，不读取已有连接、凭证或 Claude 登录。

```sh
go build -o bin/cpa ./cmd/cpa
python3 scripts/compat/claude_mock.py --output /private/tmp/cpa-compat-default
python3 scripts/compat/claude_mock.py --output /private/tmp/cpa-compat-low --effort low
python3 scripts/compat/claude_mock.py --output /private/tmp/cpa-compat-disabled --settings-json '{"alwaysThinkingEnabled":false}'
```

可使用 `--cpa /绝对路径/cpa`、`--model claude-sonnet-4-6`、`--target-model gpt-6.1-sol`、`--effort high` 或 `--settings-json` 组合构造实验。每次使用独立输出目录。运行环境需允许 Python、CPA、Claude Code 创建本机 loopback 监听及其子进程；受限沙箱可能需要本机执行权限。

脚本创建临时 HOME、CLAUDE_CONFIG_DIR、settings、工作区及 CPA 状态。通过 CLI 创建 Responses API_KEY mock 连接和 profile，三个槽位默认绑定 SDK 已识别的 gpt-6.1-sol，应用后回读 settings。Claude 使用 `--bare --setting-sources '' --strict-mcp-config --tools '' --no-session-persistence`，禁用非必要网络流量；bare 模式不读取 keychain/OAuth。为适配 bare 的鉴权契约，脚本将 apply 生成的临时 profile token 转为子进程的 ANTHROPIC_API_KEY，并将 BASE_URL 指向观测代理。该差异写在脚本中，不改变 apply 产物。没有真实 OAuth 登录、账号能力或上游额度验证。

链路：真实 Claude → loopback 观测代理 → 真实 CPA → Responses mock。mock 始终回复 `CPA_COMPAT_OK`，因此这条基线证明客户端/协议/路由可以完成一轮对话，不证明模型真实推理或工具调用能力。脚本结束时 restore 并回读原设置、停止临时 CPA、删除临时状态及临时凭证。

输出目录只保留：

- `summary.json`：Claude 版本、CPA 构建版本与二进制摘要、隔离参数、apply/restore 回读、清理结果。
- `requests.jsonl`：同一 run_id 下两侧请求正文、白名单 header、请求时刻，以及返回状态与耗时。没有 Authorization 或 API key；基线为串行单轮，可按记录顺序关联。请求正文含测试 prompt 和客户端系统提示，不应将此记录器指向真实会话。
- `claude-result.json`：真实客户端的 JSON 结果与模拟 usage。模拟 usage 及费用字段不能用来推断真实计费。

脚本失败会返回非零，清理放在 finally 中；只有完成双侧观测、预期回复与 settings 回读才应将此基线记为通过。运行前未触碰真实 `~/.claude`，也不使用当前 `127.0.0.1:8317` 服务。

`agent_mock.py` 另行验证非 bare 的 Agent 调度。它沿用临时目录和 mock，子进程外部 HTTP(S) 代理拒绝连接，仅允许 loopback。运行 `python3 scripts/compat/agent_mock.py --output /private/tmp/cpa-compat-agent --target-model gpt-6.1-sol`，会验证 sonnet 父模型调用 checker、haiku 子模型执行 Read、父模型汇总的四轮流程。该脚本不调用真实上游。
