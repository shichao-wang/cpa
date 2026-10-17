# Claude Code Plan 与跨进程实施测试

2026-10-17，CPA-13 补测通过。真实 Claude Code 2.1.293 经 CPA 访问本地 Responses mock，完成两个进程、五轮模型响应的 Plan→实施流程。CPA 二进制来自 `4639e2eaf52b5212f41cdd47a8ba4ccfb379e67b`，CLIProxyAPI 为 v8.0.10。上游目标模型名为 `gpt-6.1-sol`，本测试没有调用真实模型。

第一进程使用 `--permission-mode plan --tools Read --allowedTools Read`。mock 要求读取合成文件 `demo.txt`，Claude 执行 Read 并回传原始内容 `compat_before`。mock 随后返回修改计划。进程结束后，脚本按文件名和文件字节比较合成工作目录，确认 Plan 阶段没有修改或新增工作文件。Claude 的临时配置和会话历史保存在临时 HOME 中，不属于此“工作目录无写”断言。

第二进程使用第一进程的会话 ID，通过 `--resume` 恢复会话，并设置 `--permission-mode acceptEdits --tools Read,Edit,Bash --allowedTools Read,Edit,Bash`。请求明确批准实施之前的计划。脚本检查该进程首个出站 Responses 请求包含之前的计划文本，再由 mock 依次要求 Edit 和 Bash。Claude 将文件改成 `compat_after`，Bash 执行 `cat demo.txt`，工具结果为 `compat_after`，最后返回 `CPA_COMPAT_OK`。

| 检查项 | 实测结果 |
| --- | --- |
| Plan 阶段 Read | 工具结果包含 compat_before |
| Plan 阶段工作目录无写 | 所有文件字节和文件名保持相同 |
| 独立进程 resume | 首请求包含原计划历史 |
| 实施 Edit | 文件实际变为 compat_after |
| 实施 Bash | 回传实际文件内容 compat_after |
| 模型请求轮数 | 五轮 |
| 临时设置恢复 | restore_readback=true |
| 测试服务停止 | service_stopped=true |
| 临时状态目录删除 | temporary_state_removed=true |
| 清理错误 | 无 |

脚本为 `scripts/compat/claude_plan_mock.py`，复用基础隔离脚本，不读取真实 `~/.claude`、API key 或 OAuth 凭证。使用临时 HOME、专用 profile 和 loopback mock。复跑命令：

```sh
PYTHONPYCACHEPREFIX=/private/tmp/cpa-compat-pycache \
python3 scripts/compat/claude_plan_mock.py \
  --target-model gpt-6.1-sol \
  --output /private/tmp/cpa-compat-plan-mock
```

脱敏证据位于 `/private/tmp/cpa-compat-plan-mock/`：`plan-summary.json`、`claude-plan-result.json`、`claude-plan-implementation-result.json`、`requests.jsonl`、`summary.json`。

本测试证明真实 Claude 客户端、CPA 协议转换、会话恢复以及工具执行流程能够配合工作。计划内容和工具调用由 mock 预先规定，不能据此评价真实 Codex 制定计划或自主实施的能力。Plan 阶段只开放 Read，也没有尝试越权写入，所以没有证明 Plan 权限能够抵抗恶意写请求。真实上游的只读 Plan 结果与本 mock 实施结果应分别报告，不能合并成“真实模型 Plan→实施已通过”。
