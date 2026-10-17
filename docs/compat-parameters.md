# Claude Code 参数与上下文兼容性实测

> 历史基线：下文保留修复前的实测记录。CPA-16～18 修复后的入口校验与能力提示见 [修复说明](compatibility-fixes.md)。
日期：2026-10-17。对应 Linear CPA-4、CPA-5、CPA-6。

本轮使用真实 Claude Code 2.1.293、CPA 合并版本 `4639e2eaf52b5212f41cdd47a8ba4ccfb379e67b`、CLIProxyAPI v8.0.10。Claude 使用临时 HOME、临时配置及专用 profile，经 CPA 公开 `/v1/messages` 调用 loopback Responses mock。没有读取实际连接凭证，没有付费推理或账户登录，也没有修改真实 `~/.claude`。真实 CLI 的目标模型是 `gpt-6.1-sol`，三个槽位绑定到同一上游模型。

结论：effort 和 thinking 的协议映射可以工作，但存在明确限制。关闭 thinking 会转换为 `low`，SDK 不认识的上游模型会丢弃 reasoning。Claude 展示的 200K 或 1M 上下文来自客户端模型配置，不能作为 Codex 实际容量的证明。全部 mock 成功仅证明请求转换、路由、客户端响应解析。

## CPA-4：effort

真实 Claude Code 的 low、medium、high、max 四档均收到 `CPA_COMPAT_OK`，请求在两个边界记录如下。

| CLI 设置 | Anthropic 入站 | Responses 出站 | 结论 |
| --- | --- | --- | --- |
| `--effort low` | adaptive/display omitted，effort low | reasoning.effort low | 兼容映射 |
| `--effort medium` | adaptive/display omitted，effort medium | reasoning.effort medium | 兼容映射 |
| `--effort high` | adaptive/display omitted，effort high | reasoning.effort high | 兼容映射 |
| `--effort max` | adaptive/display omitted，effort max | reasoning.effort max | 兼容映射 |

公开 HTTP 测试另确认：adaptive xhigh 保留为 xhigh；minimal 钳制为 low；非法 ultra 返回 400 且不触达上游。仅传 `output_config.effort=high` 而不传 thinking，出站仍为默认 medium。因此自写客户端必须同时表达 thinking 意图。

发现：未知 SDK 模型 `compat-unknown-model` 使用 adaptive/high 仍得到 mock 200，但出站没有 reasoning。环境基线中历史名称 `gpt-5.4` 也出现同样现象。是否“参数生效”必须检查出站，不能用状态码推断。

证据目录：`/private/tmp/cpa-compat-parameters-{low,medium,high,max}/`。每目录包含 `summary.json`、`requests.jsonl` 和 `claude-result.json`。summary 记录二进制 hash、构建 revision、SDK 版本、隔离及清理结果。真实 Codex 是否接受这些档位尚未验证。

### effort 设置来源优先级

补测通过独立 wrapper `scripts/compat/parameter_matrix.py` 给真实 Claude 进程注入白名单环境变量，不改动底层隔离脚本。实际优先级是 `CLAUDE_CODE_EFFORT_LEVEL` 环境变量高于 `--effort`，`--effort` 高于 settings 的 `effortLevel`。

| settings effortLevel | 环境变量 | CLI effort | 实际入站及出站 effort |
| --- | --- | --- | --- |
| low | 无 | 无 | low |
| low | medium | 无 | medium |
| low | 无 | high | high |
| low | medium | high | medium |

这些证据描述 Claude Code 2.1.293 的客户端行为。CPA 收到的是最终选定的参数，无法识别或改变客户端设置来源。证据目录为 `/private/tmp/cpa-compat-priority-{settings,env-over-settings,cli-over-settings,env-over-cli}/`，其中 `parameter-result.json` 记录入站和出站的断言结果。

## CPA-5：thinking 与 budget

| 入站配置 | 实测 reasoning.effort | 说明 |
| --- | --- | --- |
| 无 thinking | medium | 默认档位 |
| adaptive，无 effort | xhigh | 固定默认映射，不能当作上游自动决策 |
| adaptive/display omitted + high | high | 隐藏展示不影响 effort |
| enabled，无 budget/effort | medium | 默认档位 |
| enabled + effort high，无 budget | high | effort 保留 |
| enabled + budget 1024 | low | budget 转成离散档位 |
| enabled + budget 8192 | medium | 同上 |
| enabled + budget 24576 | high | 同上 |
| enabled + budget 32768 | xhigh | 同上 |
| enabled + budget 1024 + effort high | low | budget 优先 |
| disabled | low | 目标模型不支持 none，降至最低档 |
| disabled + budget 24576 + effort high | low | disabled 优先，但仍不能真正关闭推理 |

预算边界另有以下结果，所有请求使用 `max_tokens=100`。

| budget_tokens | HTTP 状态 | 出站行为 |
| --- | --- | --- |
| -2 | 400 | 拒绝请求，不调用上游 |
| 字符串 "invalid" | 200 | 被按零预算处理，再映射成 low，类型未严格拒绝 |
| 0 | 200 | 映射成 low |
| 32768 | 200 | 映射成 xhigh，即使 budget 大于 max_tokens |

上述成功请求出站没有 `max_output_tokens`。不能把 Anthropic 的 max_tokens/budget 组合限制等同于 Codex 的限制，也不能从 mock 回复长度证明输出上限被保留。非法 budget 类型未拒绝，以及出站没有输出 token 上限，均应作为后续兼容性问题记录。

真实 Claude `--settings-json '{"alwaysThinkingEnabled":false}'` 发出 `thinking.type=disabled`，即便仍带 output_config.effort high，最终出站 low。这属于兼容降级。不要向用户宣称 thinking 已关闭。adaptive 在这里是兼容映射，没有证据证明与 Anthropic 原生 adaptive 有相同语义，也没有评估推理质量、时间或 reasoning token 消耗。

证据：`/private/tmp/cpa-compat-parameters-thinking-off/`，以及 Go 参数测试日志。

## CPA-6：上下文与 token 计数

- 真实 Claude 以普通 `claude-sonnet-4-6` 运行时，结果的 `modelUsage.contextWindow=200000`。
- 指定 `claude-sonnet-4-6[1m]` 后，Claude **移除实际请求 model 的后缀**，向 CPA 发送 `claude-sonnet-4-6`，并添加 `context-1m-2025-08-07` beta header。请求正常 200，Claude 本地报告 `contextWindow=1000000`。
- CPA `/v1/models` 返回公开 ID、名称、说明、创建时间，没有 context_length/context_window/max_input_tokens/max_output_tokens/capabilities。上面的 1M 数值不是 CPA 对实际 Codex 容量的声明。
- 手工对公开 HTTP 接口发送未配置的 `claude-opus-test[1m]` 模型字符串会 400，不触达上游；这与真实 Claude 去后缀的行为不同。
- `/v1/messages/count_tokens` 对 hello 返回 1，对 1000 次 `hello world ` 返回 2000。两次均未调用推理 mock 上游。这证明本地计数可用及随输入增长，不证明计数和 Codex 计费完全相同，更不证明模型接受 200K/1M 输入。

证据：`/private/tmp/cpa-compat-parameters-1m/` 记录实际 model、beta header、Claude contextWindow。上下文真实边界、长文召回、自动压缩和 `/compact` 不在此结果内，应由 CPA-11/12 验证。

## 可复跑的验证

```sh
GOCACHE=/private/tmp/cpa-go-build go test -race ./internal/gateway \
  -run '^TestCompatibilityParametersThroughMessages$' -count=1 -v

python3 scripts/compat/claude_mock.py \
  --target-model gpt-6.1-sol --effort medium \
  --output /private/tmp/cpa-compat-parameters-medium

python3 scripts/compat/claude_mock.py \
  --target-model gpt-6.1-sol --model 'claude-sonnet-4-6[1m]' \
  --output /private/tmp/cpa-compat-parameters-1m
```

Go 测试包含 28 个行为子测试，使用临时 schema 3 store 和 mock 上游，驱动公开 HTTP 界面；没有导入 SDK internal，也没有改动 SDK 或生产实现。部分用例明确记录当前限制，生产行为改进后应同步更新此评估。日志：`/private/tmp/cpa-compat-parameters-go.log`。

建议将 effort 仅搭配 SDK 已识别的模型使用；将 disabled→low 作为明确能力降级提示；在上下文容量得到上游真实验证前，不把 `[1m]` 当作容量承诺。真实模型接受度、实际容量和推理质量仍标记为“证据不足”，不能由本轮 mock 通过推定。

优先级复跑示例：

```sh
PYTHONPYCACHEPREFIX=/private/tmp/cpa-compat-pycache \
python3 scripts/compat/parameter_matrix.py --target-model gpt-6.1-sol \
  --settings-json '{"effortLevel":"low"}' \
  --env-json '{"CLAUDE_CODE_EFFORT_LEVEL":"medium"}' \
  --effort high --expect-effort medium \
  --output /private/tmp/cpa-compat-priority-env-over-cli
```
