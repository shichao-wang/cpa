# Claude专属工具、缓存与结构化输出的兼容边界

> 历史基线：下文保留修复前的实测记录。CPA-16～18 修复后的入口校验与能力提示见 [修复说明](compatibility-fixes.md)。
本页记录CPA-14的协议转换测试。新增测试只使用临时状态和loopback Responses mock，没有真实模型调用、真实OAuth登录或真实凭证读取。测试入口是公开`/v1/messages`，连接使用API_KEY Responses协议，经过当前锁定SDK的Codex executor。

## 官方名称核对

编写测试前于2026-10-17核对以下Anthropic官方文档，选择仍有文档示例的具体版本，没有根据工具名称猜测能力。

- [工具参考](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-reference)将web search、web fetch和code execution列为server tools。这类工具在服务端执行，不能用同名客户端function替代后宣称原生支持。
- [Web search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool)给出`web_search_20250305`、`name=web_search`及`max_uses`、`allowed_domains`字段。
- [Web fetch](https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-fetch-tool)给出`web_fetch_20250910`、`name=web_fetch`及域名与引用选项。
- [Code execution](https://platform.claude.com/docs/en/agents-and-tools/tool-use/code-execution-tool)给出`code_execution_20250825`、`name=code_execution`。当前文档说明该版本无需beta header，旧版`code-execution-2025-05-22`仍为有效历史opt-in。
- [细粒度工具流](https://platform.claude.com/docs/en/agents-and-tools/tool-use/fine-grained-tool-streaming)说明当前每工具`eager_input_streaming`字段及历史`fine-grained-tool-streaming-2025-05-14`header。

本测试为观测header转换而同时发送两个历史beta token，并发送`anthropic-version: 2023-06-01`，没有声称这三个server tools现在必须设置beta header。

## 复现与结果

```sh
GOCACHE=/private/tmp/cpa-go-build go test -race ./internal/gateway -run '^TestCompat(AnthropicServerToolBoundaries|CachedUsageMapping)$' -count=1 -timeout=60s -v
```

本轮两组、十个子测试全部通过，耗时3.563s。日志为`/private/tmp/cpa-compat-extras.log`，测试文件为`internal/gateway/compat_extras_test.go`。测试既检查最终Responses工具形状和header，也检查实际下游usage；mock接受HTTP200不作为原生工具执行成功的证据。

| 入站请求 | 最终Responses请求观察 | 结论 |
| --- | --- | --- |
| web_search_20250305，max_uses=2，allowed_domains=[example.com] | type=web_search，filters.allowed_domains保留，max_uses不在出站工具中 | 部分兼容映射；调用上限未映射，未执行真实搜索 |
| web_fetch_20250910，max_uses=2，citations.enabled=true | 改为type=function、name=web_fetch，schema变为空object；原有额外字段仍在function上 | 不具备Anthropic server fetch语义，不能视为原生支持；真实上游是否接受这些扩展字段尚未验证 |
| code_execution_20250825 | 改为type=function、name=code_execution，schema变为空object | 没有Anthropic服务端沙箱执行能力；只是同名function声明 |
| 用户工具make_file，eager_input_streaming=true | 仍为function，并原样保留eager_input_streaming额外字段 | 没有证据证明该Anthropic开关在Responses端生效；也不能仅因字段仍在就标记为兼容映射成功 |

四个请求的出站`Anthropic-Beta`及`Anthropic-Version`均为空。这些header没有传到Responses mock，也没有因发送它们而被CPA拒绝。它们不能用于向Codex启用对应Anthropic能力。

此外，本轮每个出站tools列表都额外出现SDK附加的`image_generation`工具及`output_format=png`。这是请求形状观察，测试没有触发图片生成，也不证明目标真实模型接受或执行该工具。

## 缓存usage映射

非流JSON响应与流式message_delta分别验证三组usage，共六个子测试。

| Responses mock usage | Messages最终usage |
| --- | --- |
| input_tokens=100，cached_tokens=0 | input_tokens=100，output_tokens=7 |
| input_tokens=100，input_tokens_details.cached_tokens=40 | input_tokens=60，cache_read_input_tokens=40，output_tokens=7 |
| input_tokens=100，cached_tokens=40，cache_write_tokens=20 | input_tokens=40，cache_read_input_tokens=40，cache_creation_input_tokens=20，output_tokens=7 |

结果说明SDK将Responses包含缓存的input_tokens转换为Anthropic分开计算缓存的usage。`cache_write_tokens`是本测试注入、当前SDK识别的扩展计数，不据此声称真实Codex必然返回该字段。

这些测试证明非零缓存字段可以映射，不证明真实服务命中了缓存。环境验证中两个相同短请求的cached_tokens均为0，因此真实命中率、cache_control或TTL等能力尚无正面证据。

## CLI结构化输出的已有证据

环境测试的`/private/tmp/cpa-compat-real-round2.json`中，`structured-json`场景只有一次实际请求，CLI的`structured_output.marker=CPA_REAL_OK`，上游工具调用为`StructuredOutput`。该场景证明当前Claude Code的`--json-schema`工具流程能够完成。它没有证明原生Responses `text.format`或JSONSchema字段被直接透传。此处引用已有执行证据，没有重复真实调用。

## 验收范围

已区分部分兼容映射、同名function转换及未证明生效的开关，没有将mock HTTP200写为Anthropic server tools可用。未测试真实server search、fetch、沙箱代码执行或beta服务，也未做服务端工具结果块、引用与container生命周期的端到端验证。推荐使用Claude Code自身已验证的客户端工具流程；需要Anthropic托管工具的调用方应按本表检查能力，不能仅按工具名称判断支持情况。
