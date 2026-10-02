## Why

锦官密钥绑定当前提示用户填写 `mk-` 原始密钥，但用量归账使用木牛 Key 记录的 `id`。木牛管理 API 已提供真实 ID，控制台却没有便于复制的入口；签发响应虽然实际包含 `id`，声明和客户端类型没有完整表达该事实。

## What Changes

- 密钥列表显示并可复制实际 Key ID；签发结果提供同一 ID 的复制入口。
- 为签发响应声明当前已经返回的 `key`、`id`、`prefix`、`tenant` 字段，并从契约生成类型。
- 保留平台提供的自定义字符串 ID，不将所有 ID 强制当作 UUID，也不以原始密钥前缀判断合法 ID。
- 与锦官同名 change `fix-api-key-binding-identifiers` 配套，完成“签发 → 复制 ID → 绑定 → 归账”验收。

## Capabilities

### New Capabilities

无。

### Modified Capabilities

- `api-key-lifecycle`：管理面真实 Key ID 可见、可复制，签发响应契约如实声明。

## Impact

涉及管理 API 契约声明、生成类型、密钥列表与签发反馈；不改变密钥认证、散列存储、计价或平台拉取协议。

## Non-goals（不做清单）

- 不在木牛新增锦官用户绑定接口或共享控制台登录。
- 不持久化原始密钥、不将 `prefix` 作为 ID、不提供通过 secret 猜测 ID 的接口。
- 不重写既有平台 ID，也不禁止历史合法的 `mk-` 开头字符串 ID。
- 不引入新表单/复制控件体系；复用现有组件与浏览器能力。
- 不接真实支付渠道；收款继续 Mock。
