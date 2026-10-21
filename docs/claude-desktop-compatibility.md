# Claude Desktop 兼容性报告

调查日期为 2026 年 10 月 21 日。已核实安装包配置契约，并用当前安装的 Desktop 完成 Chat 文本、本地 Code Read 工具往返和四档模型选择的本地 mock 验证。取消与错误恢复仍待补充。

本报告属于 [Issue #17](https://github.com/shichao-wang/cpa/issues/17) 的 MR1 验证结果。需求以 Issue Desc 为准，实施 Plan 和后续进展在 Issue Discussion 中维护。

## 调查对象与证据

| 项目 | 值 |
| --- | --- |
| 平台 | macOS |
| 应用位置 | `/Applications/Claude.app` |
| 应用版本 | `2.19675.0`，来自 `Contents/Info.plist` |
| 应用包 | `Contents/Resources/app.asar` |
| 应用包 SHA-256 | `817767bfbad77ea60678e22df90baba2cbabba9dda90201c65a49f0176fc7306` |
| 预加载文件 | `.vite/build/index.pre.js` |
| 预加载文件 SHA-256 | `83126565df48e98691a3845f27bb7ee78d0632aa14b5881adad8c7ac4f0a3adf` |
| 主文件 | `.vite/build/index.chunk-BZdcw7TE.js` |
| 主文件 SHA-256 | `0e794998a1ad175f651818913cba690de0724d92d7a946efc58ae0f08c7df226` |

静态调查读取应用包，提取文件只写入 `/private/tmp`。随后用户明确授权临时接管并恢复 Desktop 配置：测试前退出 App，备份配置库元数据，新增临时配置并切换活动项。Code 使用临时 `CLAUDE_CONFIG_DIR`，所有模型请求指向 loopback mock；未修改真实 `~/.claude`，未登录或调用付费模型。验收后退出 App，检查受管字段无冲突，再恢复活动指针和列表并回读验证。仓库不包含应用源码、用户配置或凭证。

下文的文件名均指应用包内部文件，偏移是 UTF-8 解码后字符串的零起始字符位置。偏移只适用于表中摘要对应的版本，升级后必须重新检查。

## 本地配置契约

macOS 的第三方模式默认读取 `~/Library/Application Support/Claude-3p/`。配置库位于该目录的 `configLibrary/`，每个配置项按 `<id>.json` 命名。应用创建 ID 时使用 UUID，读取活动项时检查 `/^[a-f0-9-]{36}$/`。

配置项 JSON 直接保存配置键，没有 `config`、`settings` 或其他外层。主文件偏移 `3371498` 附近的 `LI` 直接序列化传入对象，`iNn` 直接解析并校验该对象。

`configLibrary/_meta.json` 保存活动项和列表，其基础结构如下。示例 ID 为合成 UUID。

```json
{
  "appliedId": "11111111-1111-4111-8111-111111111111",
  "entries": [
    {
      "id": "11111111-1111-4111-8111-111111111111",
      "name": "配置名称"
    }
  ]
}
```

主文件偏移 `3371498` 至 `3374360` 的 `PI`、`FI`、`RI`、`dNn` 读取和维护该结构。首次初始化会创建名为 `Default` 的空配置项。元数据还可包含 `hybridPointer`，其结构为 `{ "bootstrapUrl": "…" }`；应用切换本地活动项时会删除该字段。适配器必须把活动指针、配置项列表和混合模式指针视为受管状态，不能假定元数据只有基础字段。

同一个第三方配置根目录中还存在 `claude_desktop_config.json`。该文件的 `deploymentMode` 接受 `"3p"` 或 `"1p"`。主文件偏移 `1161959` 的 `Ed`、`vVe` 读取模式，偏移 `3377906` 的 `UI` 在应用配置时写入 `"3p"`。显式 `"1p"` 会阻止正常的第三方模式选择，因此只修改配置库不足以完整管理模式切换。

## 网关、模型与界面开关

| 字段 | 已核实行为 | 主文件证据偏移 |
| --- | --- | --- |
| `inferenceProvider` | 网关模式使用 `gateway`。 | `799445` |
| `inferenceCredentialKind` | 静态凭证使用 `static`。 | `784854` 附近的凭证分支 |
| `inferenceGatewayBaseUrl` | 网关地址使用允许 HTTP loopback 的 URL 校验。 | `799445` |
| `inferenceGatewayApiKey` | 静态网关凭证由客户端读取并用于请求认证。cpa 应仅派生 profile 下游凭证。 | `1159000` 附近的 `dVe` 网关分支 |
| `inferenceGatewayAuthScheme` | 支持 `bearer`、`x-api-key`，默认为 `bearer`。 | `787244` |
| `modelDiscoveryEnabled` | 可以显式关闭发现，并使用给定模型列表。 | `913717` |
| `inferenceModels` | 条目的 `name` 必填，列表首项是默认模型。 | `909100` 至 `913500` |
| `labelOverride` | 可选，用于模型选择器的显示名称。 | `909609` |
| `anthropicFamilyTier` | 可选，当前枚举包括 sonnet、opus、haiku、fable、mythos。 | `670732`、`910655` |
| `chatTabEnabled` | 第三方模式默认关闭 Chat，需要显式设为 true。 | `883944`、`886770` |
| `isClaudeCodeForDesktopEnabled` | Code 默认开启。 | `872061` |
| `coworkTabEnabled` | Cowork 默认开启。 | `870100`、`886770` |

模型条目还接受 `supports1m`、`prefer1m`、`maxEffort` 等能力字段。字段存在只说明客户端接受配置，不能证明上游具备该能力。cpa 的四档绑定不需要跟随客户端枚举增加第五档。

主文件偏移 `696800` 至 `699700` 的 `FEe`、`zs`、`Bs` 明确允许 `http://127.0.0.1`、`http://localhost`、`http://[::1]`。HTTP 校验按这三个主机名精确匹配，不接受其他 HTTP 地址。HTTPS 经过另行校验，URL 中的用户名或密码会被拒绝。

主文件偏移 `913300` 附近的 `AMe` 去除网关根地址末尾的 `/` 后附加 `/v1/models`，偏移 `2722144` 的 `B$t` 对推理探测附加 `/v1/messages`。根地址应保持 API 根路径，不能预先附加 `/v1`。实际 Chat、本地 Code 的请求和流式行为仍需 mock 观测。

## 隔离启动的核查结果

`CLAUDE_USER_DATA_DIR` 不能作为生产包的普通隔离入口。预加载文件偏移 `1068789` 在没有有效 `CLAUDE_CDP_AUTH` 时删除该变量。偏移 `995944` 的 `RZ` 校验该令牌的目录、时间和签名。普通兼容性验证不应设置、伪造或绕过该令牌。

普通 Electron 参数 `--user-data-dir` 没有出现在应用的调试与网络覆盖参数拒绝列表中。列表位于预加载文件偏移 `995065`，包含 remote-debugging、host-resolver-rules、ignore-certificate-errors 等参数。静态调查没有发现对 `user-data-dir` 的其他特殊处理。

预加载文件中的 `a6` 检查用户数据目录是否变化，但检查结果只写入 `localPairingDisabledReason`。主文件在偏移 `2415435`、`4057177` 使用该值禁用 Chrome extension MCP 本地配对，没有因此拒绝启动，也没有发现 Chat 或 Code 推理依赖该值。不能把此检查写成“临时目录会被应用拒绝”。

`--user-data-dir=/private/tmp/独立目录-3p` 是待实测的隔离候选。目录以 `-3p` 结尾时，预加载文件偏移 `997216` 的 `XZ` 和主文件偏移 `1138848` 的 `Yu` 不再附加后缀。应用仍可能读取系统用户目录或系统钥匙串，因此仅指定 userData 或 HOME 不足以证明隔离。

`CLAUDE_CONFIG_DIR` 可控制 Code 的配置根目录，见主文件偏移 `3225740` 的 `Ij`；偏移 `3890450` 的 `VH` 在该变量存在时把 `.claude.json` 也放到该根目录。系统钥匙串需要另外隔离，不能由这些环境变量推定安全。

首次尝试使用外层 macOS Seatbelt 沙箱、临时 HOME 和 userData。访问限制探针通过，但 Chromium helper 初始化自身沙箱时报告 `Operation not permitted`，网络和 GPU 服务反复崩溃，未发出推理请求。该路径为 `blocked`，没有关闭 Chromium 沙箱。用户随后授权临时接管实际 Desktop 配置，正常启动当前 App 后完成验证。因此本次结果不证明独立 userData 的完整隔离能力，也不承诺多实例不冲突。

## 推理组件的安装契约

安装包主文件偏移 `1792099` 的 `$h` 固定 Claude Code 组件版本为 `2.1.286`，发布根地址为 `https://downloads.claude.ai/claude-code-releases`。本次安装的 `Contents/Resources/` 下没有 `preseed/`，没有发现可以直接使用的预置 host 组件。调查没有读取日常用户缓存，不能推断用户缓存中是否已有组件。

macOS 的内置清单提供应用 bundle，因此主文件偏移 `4114953` 的 `downloadBundleForTarget` 选择以下公开压缩包。摘要是内置发布清单声明的下载校验值，Apple silicon 压缩包已下载，实际字节数和 SHA-256 与清单一致；Intel 包未下载。

| 平台 | 组件 URL | 预期 SHA-256 | 预期字节数 |
| --- | --- | --- | --- |
| Apple silicon | `https://downloads.claude.ai/claude-code-releases/2.1.286/darwin-arm64/claude.app.tar.zst` | `f2326db618029608409be774a5f4d5fde8a032d319d6aa58c3043afda393dd96` | `75074255` |
| Intel | `https://downloads.claude.ai/claude-code-releases/2.1.286/darwin-x64/claude.app.tar.zst` | `fda00b160da41c43716c36fbd4f21729108f898911eea5ede701b10305bca5eb` | `79224277` |

组件安装位置由当前 `userData` 派生。主文件偏移 `4084736` 的 `Rdr` 选用 `<userData>/claude-code`，偏移 `4106858` 附近的 `installDirFor` 和 `getBinaryPathForTarget` 组合版本、压缩包摘要前 12 位及 bundle 内可执行文件路径。Apple silicon 的完整相对路径为 `claude-code/2.1.286/f2326db61802/claude.app/Contents/MacOS/claude`。

安装目录中的 `.verified` 保存完整压缩包摘要。主文件偏移 `4088100` 的 `ufr` 在 bundle 安装后写入该标记，偏移 `4107313` 的 `binaryExistsForTarget` 检查可执行权限、标记与 Mach-O 文件头。临时缓存必须来自摘要匹配的官方压缩包，并保留 bundle 原有结构；不能用任意本机 CLI 或虚构校验标记代替该组件。

## 真实客户端 mock 验收状态

| 验收项 | 状态 | 当前证据 |
| --- | --- | --- |
| 安装包版本与本地存储格式 | `static_verified` | 本报告所列应用包与函数位置。 |
| HTTP loopback 配置校验 | `static_verified` | URL 校验逻辑接受三个明确主机名。 |
| 受限沙箱下的独立 userData 启动 | `blocked` | Chromium helper 的嵌套沙箱初始化失败；改用用户授权的临时 Desktop 配置接管。 |
| Chat 文本与 SSE | `passed` | 真实 Chat 界面显示 `CPA_DESKTOP_OK`，观测到 Messages → cpa → Responses mock 和 HTTP 200/SSE。 |
| Chat 工具调用、取消与错误 | `not_run` | 尚无真实 Desktop 对应流程证据。 |
| 本地 Code 文本与工具往返 | `passed` | 内嵌 Code `2.1.286` 执行 Read 读取临时 fixture，匹配 call ID 的结果经过双侧并显示预期文本。 |
| 四档选择、默认模型与辅助请求 | `passed` | 默认 Sonnet；Code 选择 Opus、Haiku、Fable 后均返回预期文本。观察到四个完整公开 ID、Haiku 辅助请求和 count_tokens。 |
| 配置切换、完整重启与恢复 | `partial` | 手工临时配置切换、完整启动和原配置恢复回读通过；产品 apply/restore 生命周期待 MR2。 |
| Cowork | `not_run` | 尚未完成独立验收。 |

真实客户端证据应同时记录版本、隔离方法、请求实际路径、模型 ID、认证方式和模拟响应结果。输出中不得保留认证 header 或凭证。只有完成目标界面的实际请求、预期输出和清理回读，才能把对应项目改为通过；HTTP 200、静态样本或测试驱动自测均不能替代客户端验收。

本次实际 UI 验证的 run ID 为 `8cccfe07-9ab6-4c6e-aee1-906103f8bb75`。所有观测到的下游认证与临时 profile key 相符，所有记录的 HTTP 结果为 200。原始脱敏请求形状、UI 观察记录与恢复结果留在本机私有临时证据目录，不提交真实配置。旧版 harness 的摘要缺少请求响应关联，本次通过结论由真实 UI 与双侧记录共同确认；仓库中的新版 harness 另加请求 ID、完整响应和 Read 发出顺序检查，HTTP 自测始终不标记 Desktop UI 通过。
