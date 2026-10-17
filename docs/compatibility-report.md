# Claude Code 使用 Codex profile 的兼容性评估

更新日期：2026-10-17。项目：[Claude Code × Codex 兼容性验证](https://linear.app/shichao-wang/project/claude-code-codex-兼容性验证-db72f60a0b23)。

Claude Code 的基本会话、模型绑定、工具调用、结构化输出、图片 Read 和压缩后恢复已有实际证据。effort、thinking 和专属工具经过协议转换，不能视为 Anthropic 原生语义。CPA-11现按用户要求以OpenAI官方模型规格结合CPA兼容实测验收；CPA-12压缩与恢复已完成；CPA-15报告已收尾。这里的完成表示兼容性评估完成，不表示所有Anthropic专属功能都受到支持，也不声称实测过账户的精确拒绝边界。

当前结论集中在本页。[历史记录](compatibility-history-2026-10-17.md)保留失败、观测修正和旧状态，不用于覆盖本页结论。[脱敏证据摘要](compatibility-evidence.json)随报告保存，原始临时产物路径仅用于本机复核。

## 版本与隔离范围

| 项目 | 测试值 |
| --- | --- |
| Claude Code | 2.1.293 |
| CLIProxyAPI | 锁定公开 SDK v8.0.10，无 fork 或 internal 导入 |
| CPA | 首轮评估基线4639e2e；CPA-16～18已随PR #11合并至92c6446；长上下文驱动和本报告为该基线之后尚未提交的测试改动 |
| 下游公开模型 | claude-sonnet-4-6、claude-opus-4-6、claude-haiku-4-5-20251001 |
| 上游 | 已有官方Codex OAuth连接、Responses协议、gpt-6.1-sol |
| 长上下文参数 | low effort；普通公开模型显示200K窗口；显示值不等于上游容量 |
| 隔离 | 临时HOME、settings、工作区、状态、profile；只读复制目标凭证，不登录或刷新 |

不同版本的证据分别记录，不声称所有历史用例都在新binary上重新发起过真实调用。修复后公开入口、路由、参数校验和能力说明有本地回归，见[修复说明](compatibility-fixes.md)。

## 当前兼容性矩阵

“原生支持”要求原生能力的直接证据；本报告没有把跨协议HTTP200归入该类别。“兼容映射”表示转换后在列明范围内可用。“被忽略”表示该参数未执行；“不支持”表示所需语义不可用；“证据不足”表示尚不能下结论。

| 能力 | 分类及当前行为 | 已有证据与限制 |
| --- | --- | --- |
| 模型与profile绑定 | 兼容映射 | 三槽位、模型切换、重启选择、下游授权和同协议连接隔离；[参数报告](compat-parameters.md)及路由回归 |
| effort | 兼容映射 | CLI low/medium/high/max的出站转换通过；真实上游接受low/high。默认来源优先级为环境变量、CLI、settings；未评测各档推理质量 |
| adaptive thinking | 兼容映射 | 显式effort映射到对应档位；省略时映射xhigh，不能宣称上游自主选择推理预算 |
| 完全关闭thinking | 不支持该语义 | disabled降为low，仍可能推理；修复后有能力说明及响应头提示 |
| thinking budget | 兼容映射 | enabled预算转换为离散effort；不是推理token硬上限；非法类型及预算关系现已入口拒绝 |
| 未知模型的推理参数 | 不支持 | 已注册未知模型的普通对话保留；显式effort或thinking入口拒绝，避免静默丢弃 |
| max_tokens输出硬上限 | 被忽略，上游未执行 | 当前Codex路径移除输出预算；X-CPA-Output-Limit: not-enforced。要求硬上限时X-CPA-Require-Output-Limit: true使请求400且不触达上游 |
| 上下文与[1m] | 官方规格与链路实测分开记录 | OpenAI公布gpt-6.1-sol上下文1,050,000；CPA实测896,456输入召回及完整性通过。CLI显示不是规格来源，OAuth链路没有实测到官方总窗口极限 |
| Read/Edit/Bash及恢复 | 兼容映射 | 真实工具及文件核验通过，包括压缩后的Edit/Bash和独立恢复。Grep普通模式可见，bare模式曾不暴露 |
| 同响应多工具配对 | 兼容映射 | mock验证工具名、ID、参数与结果call_id；没有测量本机工具是否同时执行 |
| SSE及取消 | 兼容映射 | mock逐字节Unicode、工具JSON、思考摘要、取消传播和缺失完成事件；CLI SIGINT后恢复通过；不等于真实网络故障覆盖 |
| 重试 | 证据不足以承诺恢复策略 | mock观察429/500/断流重试，预算内主动停止，没有验证默认重试耗尽或真实限流恢复 |
| 自动压缩 | 兼容映射 | 真实192K长会话触发压缩并恢复事实；失败恢复与开关/阈值另由mock覆盖 |
| 手动/compact | 兼容映射，有客户端路径限制 | 两轮同进程stream-json和一轮PTY交互真实通过；独立短print /compact退出路径仍有间歇性持久化限制 |
| Plan到实施 | 部分证据 | 真实上游只读Read；完整计划、独立恢复及实施来自mock，未评测真实Codex自主规划质量 |
| 子Agent | 兼容映射 | 真实父Agent→haiku槽位Read→父会话汇总四轮通过；bare模式不提供Agent |
| JSON Schema | 兼容映射 | 真实CLI的StructuredOutput工具流程通过，不代表Responses原生text.format透传 |
| 图片 | 兼容映射 | 真实API合成PNG和CLI Read图片闭环通过；未测粘贴附件交互 |
| 缓存usage | 兼容映射 | 真实上游出现非零cached_tokens；mock验证流式/非流式字段拆分；TTL、cache_control和稳定命中率证据不足 |
| web_search | 部分兼容映射 | mock保留域过滤，未映射max_uses；未验收真实搜索 |
| Anthropic web_fetch/code_execution | 不支持原生托管语义 | 转为同名function，不提供原生fetch或服务端沙箱生命周期 |
| Anthropic beta及专属流选项 | 被忽略或证据不足 | beta/version头未传到Responses；eager_input_streaming字段仍在不证明开关生效 |

详细证据：[参数](compat-parameters.md)、[工具/流/错误](compat-tools-stream.md)、[Plan](compat-plan.md)、[专属能力](compat-extras.md)、[真实短请求](compat-real.md)。这些详细页中的修复前缺口以[CPA-16～18修复说明](compatibility-fixes.md)和本表为准。

## CPA-11：官方容量规格与兼容范围，已完成

根据用户对方法的纠正，停止付费寻找模型已公布的上限，采用官方规格加实际兼容范围验收。原“必须测出真实拒绝点”的探测要求不再作为关闭阻塞；保留未实测的边界，不将文档值伪装为账户实测结果。

| 规格 | 官方值 | 来源 |
| --- | ---: | --- |
| gpt-6.1-sol上下文窗口 | 1,050,000 token | [OpenAI模型规格](https://developers.openai.com/api/docs/models/gpt-6.1-sol) |
| 最大输出 | 128,000 token | 同上；不表示CPA的输出预算字段已获得执行 |

核对日期2026-10-17。[OpenAI推理说明](https://developers.openai.com/api/docs/guides/reasoning)要求为推理和输出留出上下文空间，因此1,050,000是总窗口，不是可全部填满的纯输入额度。官方API模型规格与本工具使用的Codex OAuth后端分开记录：不由API文档推断账户/客户端必定开放全部窗口。本次不新增真实调用、不再申请1152K范围。

| 档位 | 独立样本 | 结果 |
| --- | ---: | --- |
| 16K/32K/64K/128K | 每档2组 | 首、中、尾随机事实精确召回通过 |
| 192K | 1组 | 实际输入192,452，三个随机事实全部准确 |
| 自动压缩摘要 | 1次 | 193,761输入被上游接受，但它是摘要任务，不能冒充该容量的独立长文召回样本 |
| 最初128K长字符填充 | 1次本地拒绝 | CLI返回Prompt is too long，上游请求0；换短字符/token填充后同档成功 |
| 扩展256K/288K/512K/768K/896K | 每档1组完整验收；256K另有1次观测器误判 | 依次实际输入256,450 / 288,456 / 512,456 / 768,455 / 896,456，随机事实正确且完整用户文档SHA一致 |
| OAuth后端真实拒绝点 | 未实测到 | 官方模型总窗口为1,050,000；没有真实context_length_exceeded证据，不伪造该账户的精确拒绝点 |

成功下界现提高为896,456输入token的召回样本。扩展探测使用[1m]客户端显示，显式关闭自动压缩和客户端重试；每用例最多一次上游请求。首轮256K实际HTTP200且召回正确，但观测器把额外身份、日期和临时CWD混入哈希而误判失败；原证据保留，mock修正后在总预算内独立重复256K完成补证，没有免费重置计数。合成重复填充及少量事实不代表复杂长文质量，也不能证明任意信息均不会被遗漏。客户端预估、模型目录元数据和测试驱动的220K单次保护阈值均不是实际Codex上界。

新增本地容量拒绝回归使用24K mock阈值，检查长文正文完整到达上游、流式/非流式请求都明确返回400、错误原因保留、后续小请求恢复。Responses的error.code在Anthropic响应中规范化为invalid_request_error，不能仅靠下游code字段判定具体拒绝来源。mock阈值不进入真实容量结论。

CPA-11按“官方规格+链路实测+超限错误mock”范围完成。容量轮次实际6/10次、输入2,978,724、输出264，全部HTTP200；剩余4次不继续消耗。896K不是模型上限，1152K扩展未获批准且没有执行。原始探测和观测修正记录保留。

计划内400/context_length_exceeded如果没有usage，会将完整输入估算及8,192输出作为未知消耗保守预留，计入后续准入，不按零处理；其他错误或HTTP200缺少usage仍熔断。保守预留可能使测试在10次前停止。当前尚未出现这类容量拒绝。

## CPA-12：压缩和长会话恢复，已完成

| 流程 | 压缩记录 | 后续验证 |
| --- | --- | --- |
| 自动压缩 | 192,502→1,555；下一请求输入1,741 | 200K显示窗口下真实触发，三个事实准确 |
| 手动同进程第一轮 | 48,729→1,630 | 真实召回、Edit/Bash、保护约束、独立恢复通过 |
| 手动同进程第二轮 | 48,735→1,879 | 独立重复通过 |
| 真实PTY交互 | 49,096→1,664；压缩后输入2,472；独立恢复2,887 | 事实、Read历史、未完成任务、Edit/Bash、保护文件、正常退出及独立resume均通过 |

压缩失败恢复用mock注入400后恢复重试验证，不声称复现真实账户故障。原短进程print/resume路径24次最终对照中6次未持久化，直连Anthropic mock也可复现；同进程修正流程在两种mock链路各10次通过。外部客户端限制按[CPA-19](https://linear.app/shichao-wang/issue/CPA-19)保留，没有修改Claude客户端或SDK。

真实交互补验曾因Python扫描正在删除的history.jsonl.lock目录而中断。修复后限定扫描projects/*/<session-id>.jsonl，补充锁目录变化及JSONL半行回归，再完成真实整轮；旧失败与未知消耗保留。

## 推荐配置及使用范围

使用已注册、SDK可识别的上游模型及已验证profile映射。日常先保持low effort作为本次压缩/召回基线，需要更深推理时显式选择high；本项目没有质量评测证明某档普遍更好。自写Messages调用方表达thinking意图并核对能力说明，不把effort-only、disabled或adaptive当作原生Anthropic语义。

兼容默认可保持普通公开模型及200K显示窗口，让自动压缩工作。需要更长输入时，[1m]只是客户端放宽显示/预估的选项，应同时参考官方1,050,000总窗口、为输出/推理预留空间，并以已验证链路范围配置；896K实测通过不意味着任意超长任务质量已得到保证。手动压缩使用已验证的交互终端，自动化时在同进程完成/compact及后续一轮，正常退出后再resume。需要压缩持久化证据时检查会话文件和恢复后的输入量，不只看命令成功或内存事件。

严格要求输出token硬上限的调用方应启用X-CPA-Require-Output-Limit，让不支持的路径明确拒绝。客户端max_tokens或测试中的8,192输出预留都不能保证上游实际输出受限。测试输出预算按上游usage结算及停止后续请求的警戒线理解，不能换算OAuth订阅额度百分比或美元账单。

优先使用Claude Code已验证的客户端工具。依赖Anthropic托管搜索、fetch、代码沙箱、缓存TTL或复杂Agent质量时，需要单独评估。

## 实际调用与保护记录

| 独立授权轮次 | 已计请求/上限 | 已结算输入 | 已结算输出 | 状态 |
| --- | ---: | ---: | ---: | --- |
| 短请求 | 28/30 | 34,856 | 900 | 停止，未复用剩余次数 |
| 长上下文与压缩 | 20/20 | 1,455,735 | 4,212 | 已耗尽 |
| 手动补验 | 23/30 | 524,106 | 4,617 | 22次有usage，1次取消消耗未知；输入预留5,238、熔断保留 |
| 最终交互 | 10/12 | 213,809 | 1,581 | 全部结算，剩余2次不追加 |
| 容量兼容实测 | 6/10 | 2,978,724 | 264 | 以官方规格收尾，剩余4次不继续调用 |
| 合计 | 87次已计数 | 5,207,230 | 11,574 | 86次完整usage，另1次未知，合计为已知部分 |

cached input已包含在input_tokens内，reasoning已包含在output_tokens内，不重复相加。未知消耗不按零处理，不清除历史熔断，不将各轮剩余额度合并复用。[证据摘要](compatibility-evidence.json)保存各轮账本快照、用量、压缩前后计数和通过范围，没有凭证、账户标识、原始摘要或随机事实。

本轮容量工具的全量测试、race、vet、构建及Python回归已执行，日志为/private/tmp/cpa11-final-{test,race,vet,python}.log。真实配置、凭证、管理令牌及真实Claude settings与保护基线校验值一致。临时profile、settings和测试服务由清理钩子恢复/停止/删除；生产服务未替换binary或重启。

## 复现与待验收项

不涉及真实凭证的验证入口：

```sh
CPA_LONG_MODE= CPA_COMPAT_REAL=0 go test ./...
CPA_LONG_MODE= CPA_COMPAT_REAL=0 go test -race ./...
go vet ./...
go build -o /private/tmp/cpa-compat-tested ./cmd/cpa
PYTHONDONTWRITEBYTECODE=1 python3 scripts/compat/test_claude_long.py
CPA_LONG_MODE= CPA_COMPAT_REAL=0 go test ./internal/gateway -run '^TestCompatContextRejectionAndRecovery$' -count=1 -v
```

完整CLI/mock入口见[scripts/compat/README.md](../scripts/compat/README.md)，真实测试规则见[real.md](../scripts/compat/real.md)。需要具备临时loopback监听和本机子进程身份检查权限；沙箱限制导致的启动失败不能当作协议缺陷。

CPA-11依据官方规格和当前实测范围完成评估；CPA-15报告完成。当前矩阵明确区分已验证、兼容降级、不支持和未评测内容；真实账户精确拒绝点、复杂长文质量及托管工具等仍按限制记录，不阻塞本次兼容性评估收尾，也不表述为这些功能已通过。
