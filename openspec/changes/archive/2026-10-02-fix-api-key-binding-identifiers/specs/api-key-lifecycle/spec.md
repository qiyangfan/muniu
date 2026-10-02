## MODIFIED Requirements

### Requirement: 管理面可见性

key 列表（`GET /admin/v1/keys` 与控制台密钥页）MUST 展示 `tpm_limit`、`daily_quota_credits`（抽象额度单位 credit，1 credit = 本位币十亿分之一）、`expires_at`；控制台签发表单 MUST 提供对应输入，列表可见当前值。管理面 MUST 明确区分 Key ID、secret 与 prefix，并在列表和签发结果中提供真实 Key ID 的展示与复制。创建响应契约 MUST 如实声明运行时已经返回的 `key`、`id`、`prefix`、`tenant`，不得因本变更改名既有响应字段。

#### Scenario: 列表回显

- **WHEN** 签发携带全部新字段的 key 后查询列表
- **THEN** 该条目回显三个字段的设定值，空值如实显示为“不限/永不过期”

#### Scenario: 复制真实 Key ID

- **GIVEN** 列表或签发结果包含真实 Key ID
- **WHEN** 用户执行“复制 Key ID”
- **THEN** 剪贴板获得完整原始 `id`，与用量 `key_id` 一致，不是 secret 或 prefix，不转换大小写

#### Scenario: 创建响应声明与实际输出一致

- **WHEN** 用户成功签发新 Key
- **THEN** 响应及契约包含 `key`、`id`、`prefix`、`tenant`，客户端从该响应提供 ID 复制入口，secret 仍遵守既有一次性展示纪律

#### Scenario: 自定义 ID 可复制

- **GIVEN** 平台提供的 Key 使用合法自定义字符串 ID
- **WHEN** 用户查看并复制 Key ID
- **THEN** 系统按真实值显示和复制，不因 ID 不是 UUID 或具有某种前缀而替换、截短或拒绝它

#### Scenario: 缺失 ID 不伪造值

- **GIVEN** 兼容列表条目没有可用的 `id`
- **WHEN** 用户查看条目
- **THEN** 界面明确显示 ID 不可用并禁用复制，不以 prefix、secret 或序号充当 ID
