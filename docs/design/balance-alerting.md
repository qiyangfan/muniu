# 渠道余额观测与告警(balance-alerting)

> 行为契约的规范源是 `openspec/specs/provider-balance-alerting`(归档后);本文是设计叙事与运维口径摘要。定位裁决见 ADR-0018。

## 能力一句话

按渠道声明余额源 → 定时查询上游真实余额 → 叠加网关自身计量做实时衰减 → 外推剩余可用天数 → 穿越档位/阈值经 webhook 主动告警。

## 余额源 kind 对照表

| kind | 凭证 | 端点(base_url 同源) | 余额字段 | 币种 |
|---|---|---|---|---|
| `deepseek` | 渠道 key Bearer | GET `/user/balance` | `balance_infos[].total_balance` | 响应携带(CNY/USD) |
| `moonshot` | 渠道 key Bearer | GET `/v1/users/me/balance` | `data.available_balance`(含代金券) | CNY |
| `openrouter` | 渠道 key Bearer | GET `/api/v1/key` | `data.limit − data.usage`;`limit=null` 不限额 → 余额不可知 | USD |
| `billing-compat` | 渠道 key Bearer | GET `/v1/dashboard/billing/{subscription,usage}` | `hard_limit_usd − total_usage/100`(one-api/new-api 家族) | 缺省 USD;可经 `balance.currency` 显式声明(CNY\|USD) |
| `aliyun-bss` | AK/SK(RPC HMAC-SHA1 签名) | `business.aliyuncs.com` `QueryAccountBalance`(旧 bssopenapi 域名 2026-10-30 实测 NXDOMAIN 退役) | `Data.AvailableAmount`(成功信封带 `Code:"200"`+`Success:true`,已兼容) | CNY |
| `volc-bss` | AK/SK(Signature V4,密钥链终止符 `request`——2026-10-30 真机对照官方 SDK 修正) | `open.volcengineapi.com` `QueryBalanceAcct` | `Result` 直挂 `AvailableBalance`(字符串)+`Currency`(真机实证形态;按字段名容错保留) | CNY |
| `manual` | — | 无外呼,管理 API 录入快照 | 录入值 | 录入声明(必填) |
| `none` | — | 缺省,零行为 | — | — |

智谱 GLM、MiniMax、OpenAI、Anthropic、Gemini 官方无「Bearer 即查余额」的公开接口,归 `manual` 档。GCP 不纳入(ADR-0018)。
**siliconflow 不在值域**(2026-10-30 owner 裁决移除):官方余额 API `/v1/user/info` 已于 2026-08-14 退役(实测 HTTP 410),官方公告承诺的「账户层面替代 API」截至该日未发布;旧解析代码对替代 API(账户体系重建)零复用价值,故整档移除、不保留过渡形态——release-notes 出现新 API 公告后按 Bearer 族配方重新接入(deepseek 同款配方,约数十行)。

## 核心算法与纪律

- **有效余额** = 快照余额 − 快照时刻后该渠道在 `usage_logs` 的已结算消耗(`estimated=0`,与日配额同口径);币种 ≠ 结算币种时不衰减、禁天数估算、明示原因,绝不跨币种运算。
- **燃速** = 回看窗口(缺省 7 天)日均;窗口内有消耗天数 < `burn_min_days`(缺省 2)→「数据不足」;零消耗 →「剩余不限」,不发档位告警。
- **剩余小时** = 池内最小有效余额 × 24 ÷ 日均燃速(多 key 渠道取最小,哪个账户先耗尽先告警);全整数 credit 刻度,零 float。
- **档位**:缺省 30/14/7/3/1 天,配置 `tier_hours` 任意降序正小时数组;子天档位技术上允许、缺省不给(日粒度燃速推出的小时级估算误差主导信号)。
- **状态机**:armed → firing(跌破触发线,单次发射)→ armed(回升超 触发线×`rearm_ratio_milli`);判据不可得时状态保持(不误触发也不误解除);状态持久于 `balance_alert_states`,重启不重复告警。
- **失败容忍**:单次失败保持旧快照;渠道级连续失败(全部 key 失败,取各 key 行失败计数最小值)达 `failure_threshold` 发一次 `balance_source_error`,成功即清零解除。
- **燃速突变(burn_spike)**:当前回看窗口日均 ÷ 上一等长窗口日均 ≥ `burn_spike_ratio_milli`(缺省 5000,0=禁用)触发 warning;两窗口消耗天数均须达 `burn_min_days`(零基线不臆测);回落到触发线 ÷ 迟滞系数以下解除。燃速取自网关自身计量(usage_logs),key 被外部盗刷不经网关计量、此规则不可见——外部盗刷由骤降规则捕捉,两条规则互补不可互替。
- **余额骤降(cliff:<key_id>)**:仅 Bearer 源逐 key 判定(manual 人工录入天然跳变、云账务月账单周期大跌,均排除);判据为相邻两次成功快照(同币种)跌幅 ≥ `drop_ratio_milli`(缺省 500 即 50%,0=禁用),前值由 `provider_balances` 平移列(成功写入时平移,失败不动)承载;一次骤降只发一次,须一次平稳观测(跌幅低于阈值,含充值上升)才重新武装;币种不一致/前值非正不判定。
- **解耦铁律**:余额查询不触 key 池 Pick/Report(不扰动 WRR 与冷却),异步于数据面,单渠道 panic 隔离。

## 事件与通知

复用 `webhook-alerting` 管道(去重/退避/未配置零开销),事件键与载荷:

| 事件键 | severity | 上下文 |
|---|---|---|
| `balance_below:<provider>` | critical | provider/rule/balance/currency |
| `balance_tier:<provider>:<h>h` | 30d/14d→info、7d/3d→warning、≤1d→critical | +remaining_hours |
| `balance_source_error:<provider>` | warning | consecutive_failures |
| `balance_burn_spike:<provider>` | warning | prev/cur_burn_per_day_credits、ratio_milli |
| `balance_cliff:<provider>:<key_id>` | warning | key_id、prev/curr_balance、drop_ratio_milli |

**投递自监控**:投递经退避重试仍最终失败时,失败事实(事件键/severity/摘要/错误归类/时刻)经 Notifier 终局回调落库单行表 `alert_delivery_status`;失败期重复失败推进计数,任一次投递成功置解除并保留失败事实;新失败期重置计数。递归纪律:失败状态绝不经 webhook 外发(通道已断,外发即递归),唯一可见面是 `GET /admin/v1/alerts/delivery-status` 与总览页警示横幅(未解除时渲染,恢复自动消失)。事件本体不回放——通道恢复后失败期间的告警不补发,尽力通知语义不变。

## 管理 API 与控制台

- `GET /admin/v1/balances` 总览(每 key 快照/有效余额、燃速、剩余估算或禁用原因、告警态、失败计数与归类);金额一律十进制串,量数出 number。
- `GET /admin/v1/alerts/delivery-status` 投递自监控(configured/failing/最近失败事实/解除时刻);总览页在存在未解除失败时渲染警示横幅。
- `POST /admin/v1/balances/{provider}/manual` 录入快照(金额十进制串 + 币种须与配置一致);录入即生效并触发立即评估。
- `POST /admin/v1/balances/{provider}/refresh` 单渠道立即查询(异步)。
- 控制台「余额告警」页:桌面表格 ↔ 移动实体卡片(manual 源页头录入表单)。

## 运维口径

- **热更边界**:`balance` 节与渠道余额源声明属非热更域,SIGHUP/控制面拓扑热替换不生效,变更需重启(与 auth/metrics 同口径);运行期仅 key 列表随拓扑快照动态映射。
- **估算口径**:网关计价与上游真实扣费存在口径差(上游分时计价、缓存价近似),上游定期查询天然校正衰减累计误差;界面与告警文案一律「≈」。
- **安全清单**:云凭证只经 `${VAR}` 同款环境变量引用注入(字段存变量名);建议云侧 RAM/IAM 子账号最小授权账务只读;凭证不落日志、告警载荷、存储。billing-compat 需上游开启货币显示(one-api 关闭货币显示时金额字段为 quota 原始值,实现侧量级护栏会判失败)。
- **billing-compat 币种纪律**(2026-10-30 真源码实证):`*_usd` 字段族不自带可信币种语义——new-api `controller/billing.go` 按站点 `QuotaDisplayType` 换算,人民币显示站点产出 CNY 数值、token 显示站点产出 token 数,协议面不可自动判定。故币种缺省 USD(OpenAI 官方端点语义),站点显示币种非 USD 时必须显式声明 `balance.currency`,否则 below 阈值错档、天数估算被币种纪律误拦;其余 Bearer 源上游自述币种,声明 currency 仍被拒绝。
- **存储**:迁移 `0015_balance.sql` 两表 + `0017_balance_prev.sql`(骤降前值三列):`provider_balances` 每 key 最新快照(含 prev_* 平移列)、`balance_alert_states` 每规则状态;不存历史曲线,日耗由 `usage_logs` 现算。
