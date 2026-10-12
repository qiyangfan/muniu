# Proposal

## Why

kongming e2e 实证(2026-10-11,经 muniu 桥打通 responses 全链的同一轮排查):muniu 的 Responses 入站翻译误拒**两种官方规范内合法形态**——①message 条目 `type` 省略(官方缺省语义即 message,官方 SDK 手工构造条目的缺省产物)报 `input_item_unsupported`;②message 条目 `content` 为纯字符串快捷形态(官方规范与部件数组等价)报 `input_invalid`。百炼 responses 原生端点多轮工具回放存在服务端缺口(同轮四路径实证),muniu 桥是官方 SDK 客户端唯一可用链路——该缺陷直接挡住官方 SDK 缺省形态的接入,属网关兼容面的最高频现实形态缺陷,按既有裁决必须修复、不得登记为已知边界。

## What Changes

- `internal/protocol` Responses 条目解析接受官方快捷形态:message 条目 `content` 接受纯字符串(折叠为单 text 部件,与部件数组等价);条目 `type` 省略且携带 `role` 时缺省判定为 `message`。
- 拒绝清单与信封形态零改动:`previous_response_id`/`background`/未知 item 类型(`input_item_unsupported`)/非法 input(含 content 为数字、null 等)维持现行 400 机器码;省略 `type` 且无 `role` 的条目维持 `input_invalid`。
- 同语义快捷形态与显式形态的 canonical 逐字节一致(缓存指纹/计量同构),以对拍测试钉死。
- 文档:proxy-and-streaming.md「Responses 入站翻译」映射表补两种快捷形态,并留百炼多轮回放服务端缺口的知识注记(防未来误判 muniu 入站)。

## Capabilities

### New Capabilities

(无)

### Modified Capabilities

- `responses-inbound`:「请求翻译契约」需求增补官方快捷形态的接受义务(字符串 content、省略 type 缺省 message),新增等价性与缺省形态放行场景;拒绝语义保持。

## Impact

- `internal/protocol/responses.go`(respItem 解析:content 二态 + type 缺省判定)、`internal/protocol` 测试、`internal/proxy` 回归(既有 responses e2e/管道等价测试零改动)。
- `docs/design/proxy-and-streaming.md` 映射表增补。
- 不触碰 `internal/proxy` 翻译层与任何出站路径;与在途提案 `protocol-native-passthrough`、`gemini-native-protocol` 无文件交集,apply 顺序无关。无新依赖。
