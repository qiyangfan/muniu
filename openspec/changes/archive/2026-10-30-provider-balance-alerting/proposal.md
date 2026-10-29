# Proposal: provider-balance-alerting

## Why

渠道余额耗尽目前只能事后感知:上游 402 → key 冷却 → 池降级 → 候选全失败,业务已经中断才被告警。网关侧已有完整的按渠道计量(`usage_logs.cost_credits` 每上游尝试一行)与 webhook 告警基座(`internal/alerting` 去重+退避),缺的只是「上游余额观测 + 剩余可用天数外推 + 阈值状态机」——把「事后救火」提前为「按天/按小时预算的主动运维」。个人多渠道形态下,各渠道余额分散在 8+ 家控制台,人工巡检不现实。

## What Changes

- **新增余额源体系**(`internal/balance`,零新增依赖,纯 stdlib):按渠道可配置余额源,七种内置 kind——`deepseek` / `moonshot` / `siliconflow` / `openrouter` / `billing-compat`(one-api/new-api 家族)用渠道现有 Bearer key 直查;`aliyun-bss`(百炼→阿里云账务 RPC HMAC-SHA1 签名)与 `volc-bss`(火山方舟→Signature V4)用配置的云厂商 AK/SK 查账户余额;`manual` 控制台手动录入快照(留给智谱/MiniMax/OpenAI/Anthropic/Gemini 等无公开接口的家);缺省 `none` 零开销。
- **有效余额与剩余天数估算**:余额快照 − 快照后网关计量消耗 = 实时有效余额;按回看窗口(缺省 7 天)从 `usage_logs` 推导日耗速率,外推剩余可用时间,落入告警档位(缺省 30 天/14 天/7 天/3 天/1 天,配置可调;粒度下限 1 天,子天档位技术上允许但缺省不给——日粒度聚合推出的小时级估算误差主导信号)。
- **告警状态机**:每 (provider, 规则) 一份持久状态,穿越档位/阈值时经既有 Notifier 发 webhook(新增 `balance_below` / `balance_tier` / `balance_source_error` 事件),带迟滞回差与单次触发语义,防抖动刷屏;余额币种 ≠ 结算币种时天数估算显式禁用并明示,绝不跨币种硬除。
- **定时评估循环**:模仿 `revocationLoop` 模式(启动即评、可 Close、失败保持旧态),缺省每小时评估+逐渠道抖动;余额查询失败连续超阈值才发一次运维告警。
- **管理 API 与控制台页**:余额总览(每 key 余额/有效余额/日耗/剩余估算/告警态/最近检查时间)、手动录入快照、立即刷新三个端点;控制台新增「余额」页(标准件,移动端卡片范式)。
- **配置面**:新增 `balance` 顶层节(开关/评估间隔/回看窗口/档位表/迟滞系数)+ `providers[].balance` 渠道级字段(来源 kind/绝对阈值/AK·SK 环境变量引用),`${VAR}` 展开沿用,example.yaml 同步。
- **ADR-0018**:裁决「上游渠道余额的运营观测属网关自保能力」,与 ADR-0005 控制面边界(租户钱包/计费门户)划清关系。

不做(failover 已覆盖运行时自愈):余额耗尽自动摘除渠道(402→冷却既有语义)、租户钱包、跨币种折算汇率;GCP 不纳入——其账务体系无「账户余额」API(仅预算+已消耗,小时级滞后),与本能力「余额」语义不同构,未来以独立 change 做「预算消耗观测」并复用本能力的状态机与通知管道。

## Capabilities

### New Capabilities

- `provider-balance-alerting`:渠道余额观测全链——余额源查询契约(七种 kind 的行为语义)、有效余额合成、消耗速率与剩余时间估算、阈值与档位告警状态机(含迟滞与失败容忍)、定时评估循环、管理 API 与配置契约。

### Modified Capabilities

- `webhook-alerting`:「去重与事件覆盖」需求的覆盖事件集合从三类运行时事件扩展为「三类运行时事件 + 余额观测事件」;发射管道(异步/超时/退避/未配置零开销)语义不变,由既有 Requirement 继续承载。

## Impact

- **代码**:`internal/balance`(新包:kind 注册表 + 两种云签名器 + 评估状态机)、`internal/store`(迁移 `0015_balance.sql` 两张表:余额快照、告警状态;只追加纪律)、`internal/config`(新节+校验)、`internal/admin`(三端点+路由表注册)、`cmd/muniu`(装配+cleanup+Close)、`web`(新页+api-types 再生+`App.tsx` TABS)。
- **契约**:`contracts/openapi.yaml` 增三端点与 schema;`openspec/specs/webhook-alerting` 增量同步。
- **依赖**:Go 零新增(HMAC 签名 stdlib `crypto/*`);web 零新增。
- **安全**:AK/SK 经 `${VAR}` 环境变量注入,建议云侧只授账务只读 RAM/子账号;密钥不入库、不落日志,日志字段沿用 snake_case 白名单。
- **运营**:已配置渠道缺省 `none`,升级零行为变化;评估循环每渠道每小时一次 Bearer GET,量级可忽略。
