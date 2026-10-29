# Design

## Context

- 告警管道已就绪:`internal/alerting` Notifier(事件键去重 + 退避重试 + 未配置零开销),规格 `webhook-alerting`;余额告警只新增「评估器」这一发射方。
- 余额数据源为零:`Provider` 配置(`internal/config/config.go:61-73`)无余额/币种字段,`keypool.Key` 仅 `{ID, Value}`;唯一账面数据是 `usage_logs.cost_credits`(每上游尝试一行,恒记结算币种,1 credit = 本位币 1e-9)。
- 后台循环范本:`internal/auth/auth.go:422-481 revocationLoop`(自有 stop channel、启动即拉、失败保快照、Close 挂 `cmd/muniu/main.go` cleanup)。
- store = SQLite(modernc,CGO_ENABLED=0),迁移只追加,下一个编号 0015;金额整数运算禁 float;配置支持 `${VAR}` 环境展开。
- key 池健康状态是被动状态机(Pick/Report):余额查询若走 Pick/Report 会扰动数据面 WRR 与冷却语义——必须完全解耦。

上游余额 API 调研结论(官方文档/源码核实,2026-10-29):

| kind | 端点(base_url 同源) | 凭证 | 余额解析 | 币种 |
|---|---|---|---|---|
| `deepseek` | GET `/user/balance` | 渠道 key Bearer | `balance_infos[].total_balance`(十进制串) | 响应携带 CNY/USD |
| `moonshot` | GET `/v1/users/me/balance` | 渠道 key Bearer | `data.available_balance`(数值,含代金券) | CNY |
| `siliconflow` | GET `/v1/user/info` | 渠道 key Bearer | `data.totalBalance`(十进制串,充值+赠送;不用 `balance` 赠送口径) | CNY |
| `openrouter` | GET `/api/v1/key` | 渠道 key Bearer | `data.limit − data.usage`;`limit=null` = 不限额,余额不可知 | USD |
| `billing-compat` | GET `/v1/dashboard/billing/subscription` + `/usage` | 渠道 key Bearer | `hard_limit_usd − total_usage/100`(one-api/new-api 家族,源码核实) | USD |
| `aliyun-bss` | POST `bssopenapi.aliyuncs.com` `Action=QueryAccountBalance` | AK/SK,RPC HMAC-SHA1 签名 | `Data.AvailableAmount` | CNY |
| `volc-bss` | POST `open.volcengineapi.com` `Action=QueryBalanceAcct` | AK/SK,Signature V4 | 响应余额字段(见 Open Questions) | CNY |
| `manual` | 无外呼 | — | 管理 API 录入快照 | 录入时声明 |
| `none` | 缺省 | — | 零行为 | — |

**GCP 不纳入的裁决**(否决「零消费者」论据后的纯技术论证):GCP 账务体系没有「账户剩余余额」查询 API——可编程获取的只有预算/已消耗语义(Billing Budget API 与 BigQuery 账单导出,且账务数据小时级滞后),凭证是项目级服务账号私钥(OAuth2 JWT 交换,凭证面大于 AK/SK)。接入它不是再加一个 querier,而是引入与「余额」不同构的第二套语义(预算声明 + 消耗校正 + 降级三态),违背「最小结构解决既定范围」。未来若立项,形态是独立的「预算消耗观测」change,复用本能力的状态机与通知管道(源与管道解耦正是为此)。

## Goals / Non-Goals

**Goals:**

- 八种余额源 kind 的查询契约与解析器,全部纯 stdlib(`net/http`、`crypto/hmac`、`crypto/sha1`、`crypto/sha256`、`crypto/rsa` 不需要——GCP 已排除)。
- 有效余额 = 快照 − 快照后计量消耗;燃速外推剩余时间;阈值/档位告警状态机(迟滞 + 持久化)。
- 管理 API 三端点 + 控制台「余额」页;OpenAPI 单源同步。
- 余额观测与数据面完全解耦:不触 Pick/Report,不阻塞请求,评估循环可优雅停止。

**Non-Goals:**

- 不做余额耗尽自动摘除渠道(402→key 冷却既有语义已覆盖运行时自愈)。
- 不做跨币种汇率折算(余额币种 ≠ 结算币种 → 禁天数估算并明示)。
- 不做余额历史曲线存储(v1 只存最新快照 + 告警态;日耗由 `usage_logs` 现算)。
- 不做 `balance` 节的 SIGHUP 热更(非拓扑域,改动需重启,与 auth/metrics 同口径,登记)。
- 不做 GCP/预算消耗观测(见 Context 裁决)。

## Decisions

### D1: 余额源注册表独立于 adapter

`internal/balance` 维护自己的 kind→Querier 注册表(形态对齐 `adapter.For`),不复用 adapter 体系:余额查询与协议适配是正交维度(qwen 渠道可配 `aliyun-bss`,deepseek 渠道也可配 `billing-compat` 指向中转站);按 adapter 类型自动推断会造成「隐式余额源」,违背「缺省关闭」契约。

Querier 统一签名:`Query(ctx, req) (Balance, error)`,`Balance{Credits int64, Currency string, Unavailable bool}`;注册表构造期校验 kind 合法性(与配置校验双保险)。

### D2: 有效余额模型——快照 + 计量衰减,而非「查询即真实余额」

每次评估tick查询上游会得到**查询时刻**的真实余额,但两次查询之间余额仍在被消耗。有效余额统一合成为:

```
有效余额 = 快照余额 − SUM(usage_logs.cost_credits WHERE provider=? AND created_at > 快照.as_of_at AND estimated=0)
```

- `estimated=0` 过滤与 `SumCostToday` 同口径:预估行会在真实 usage 回填后重复计费,只算已结算行(误差=最后一段未结算窗口,≤ 分钟级出账延迟)。
- 对 `manual`/`aliyun-bss`/`volc-bss` 同样成立(manual 快照后衰减正是其核心价值;云账务查询频率可低)。
- 快照币种参与减法的前提是 `usage_logs.currency`(结算币种)与快照币种一致;不一致时衰减禁用——有效余额退化为「快照原值」且天数估算禁用(见 D6)。
- 备选方案「直接用上游每次返回的余额,不做衰减」被否:查询间隔内余额失真,档位判定抖动;「本地衰减 + 上游定期校正」两全(校正天然纠正计量与上游扣费的口径差,如上游侧分时计价的近似)。

### D3: 存储——两张新表(迁移 `0015_balance.sql`,只追加)

```sql
CREATE TABLE provider_balances (
  provider TEXT NOT NULL,
  key_id TEXT NOT NULL,            -- Bearer 源=池内 key ID(env 名);manual/云账务='__account__'
  source TEXT NOT NULL,            -- 产生本快照的 kind
  balance_credits INTEGER,         -- 余额币种 1e-9 刻度;NULL=不可知(openrouter 不限额)
  currency TEXT NOT NULL,
  as_of_at TEXT NOT NULL,          -- 快照观测时刻 RFC3339Nano
  manual_amount TEXT,              -- manual 源原始录入串,仅展示
  last_error TEXT,                 -- 最近查询错误摘要(分类码,无敏感值)
  consecutive_failures INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  PRIMARY KEY (provider, key_id)
);
CREATE TABLE balance_alert_states (
  provider TEXT NOT NULL,
  rule_key TEXT NOT NULL,          -- 'below' | 'tier:<hours>'
  firing INTEGER NOT NULL,
  severity TEXT NOT NULL,
  last_fired_at TEXT,
  context_json TEXT,               -- 触发上下文(余额/估算),通知载荷复用
  updated_at TEXT NOT NULL,
  PRIMARY KEY (provider, rule_key)
);
```

- 余额沿用 credit 刻度整数存储(与 `cost_credits` 同纪律,禁 float);「毫元以下精度」由 1e-9 刻度天然覆盖。
- 不建历史快照表:v1 告警需要的是当前态,历史曲线属可观测性扩张(需要时另立,`usage_logs` 本身就是历史)。
- `key_id` 哨兵 `__account__` 而非拆两张表:查询路径统一按 (provider, key_id) 扫描,min-估算逻辑对账户级源天然退化为单行。

### D4: 池内逐 key 查询,与 key 池健康完全解耦

Bearer 源对池内每把 key(跳过禁用 key——401 禁用 key 查询必然失败,徒增噪音;经既有 `StateOf` 观测口或最小只读扩展枚举)独立发 GET,独立落行;**不调用 Pick/Report**,余额查询失败不进冷却、不计 EMA——数据面健康语义不被运维观测污染。需要给 `keypool.Pool` 加一个只读枚举出口(keys + disabled 标志),不动现有方法。

剩余估算取池内最小有效余额(哪个账户先耗尽先告警);同账户多 key 场景各 key 余额相同,min 无害。

### D5: 燃速与剩余估算

```
窗口日耗 burn = SUM(usage_logs.cost_credits WHERE provider=? AND created_at >= now−W天 AND estimated=0) / W
剩余时间(小时) = 最小有效余额 / (burn / 24)
```

- `W` 缺省 7 天(覆盖周内波动;过短被单日突发主导,过长稀释近期用量变化)。
- 最小样本 = 窗口内 `cost>0` 的天数 ≥ `burn_min_days`(缺省 2),否则「数据不足」;零消耗(burn=0)→「剩余无限」,不发档位告警(绝对阈值仍生效)。
- **纯整数运算**:全部以 credit(1e-9)整数计算;小时比较采用交叉相乘前先做量纲收缩(先除后乘)并设余额上界钳制(余额超过档位表最大档 × 燃速 × 安全系数时直接判「高于全部档位」,不做除法),杜绝 int64 溢出;精确式子在 estimator 纯函数内以测试钉死。
- 燃速取窗口**均值**而非中位数:低流量个人场景日耗稀疏,中位数大概率= 0,均值 + 最小样本门槛 + 迟滞是更诚实的组合;均值对突发的敏感由 7 天窗口摊薄。
- 语义措辞:估算受「网关计价 ≈ 上游真实扣费」口径差影响(上游分时计价、缓存计价近似),管理面一律以「≈」呈现,文档明示这是网关视角估算。

### D6: 币种纪律——禁估算,不折算

汇率折算需要外部数据源(汇率 API = 新外联依赖 + 新故障面),且个人场景渠道币种通常与结算币种一致(CNY 主场)。币种不一致时:余额展示与 `balance_below` 阈值告警照常(都在余额币种内闭环),天数估算禁用并明示原因。备选「配置静态汇率」被否:引入手工维护的失真参数比禁用更危险。

### D7: 档位粒度——缺省五档,下限 1 天,配置自由

缺省档位 `[720h, 336h, 168h, 72h, 24h]`(30 天/14 天/7 天/3 天/1 天)。

- **子天档位(12h/6h/1h)不进缺省**:燃速来自日粒度聚合的窗口均值,小时级剩余估算的误差(窗口均值 vs 真实未来消耗)远大于 12h 档本身,必然抖动;「最后 1 天」的紧迫性由 1d 档 + `balance_below` 绝对阈值兜底。技术上不禁止(配置允许任意正小时数),缺省克制。
- **1 年/半年/3 个月不进缺省**:低耗渠道「余额够烧半年」是常态而非异常,长档位会常驻触发态制造噪音;需要长周期规划时自行加档。
- 配置为降序正整数小时数组,校验强约束(降序、>0、非空);档位语义是「跌破即触发」,因此 30d 档已触发时 14d 档再触发不算重复——各档独立状态、逐档发射,severity 缺省映射 30d/14d→info、7d/3d→warning、1d→critical、below→critical、source_error→warning(固定映射,可配置性留给未来真实诉求)。

### D8: 告警状态机与迟滞

每 (provider, rule_key) 状态迁移:

```
armed --估算/余额跌破线--> firing(发射事件) --回升超 线×1.2--> armed
```

- 迟滞系数 1.2 防止估算在触发线附近来回穿越刷屏;`below` 规则以余额为判据(快照币种内),`tier` 规则以剩余小时为判据(币种一致时才有)。
- 评估核心做成纯函数:`evaluate(states, rules, observation, now) → (newStates, []Alert)`,表驱动测试覆盖穿越/迟滞/重启恢复/多档直落;发射经 `Notifier.Emit`(事件键 `balance_below:<provider>` / `balance_tier:<provider>:<hours>h`,携带渠道名 + 余额/估算上下文),去重与重试由既有管道承载。
- 一次评估直落多档(如 8 天→0.5 天)= 多条告警各发一次,符合「逐档」语义。

### D9: 评估循环——revocationLoop 范式

- `internal/balance/loop.go`:自有 stop channel + ctx;启动即全量评估一轮;此后按 `eval_interval_sec`(缺省 3600)tick;每渠道相对 tick 边界错峰 `hash(provider) % interval`,避免多渠道同刻并发外呼。
- `refresh` 管理端点向循环投递单渠道立即评估信号(channel + select),与定时 tick 合流。
- `Close()` 挂 `cmd/muniu` cleanup(与 auth/alerting 同法);在途查询 ctx 取消,已得快照落库。
- 查询超时缺省 10s,单渠道失败只影响该渠道;循环 panic 用 recover 包住单渠道评估,不让观测面拖垮进程。
- 失败容忍:行内 `consecutive_failures` 计数,达阈值(缺省 3)发一次 `balance_source_error`,成功即清零;错误分类码(超时/HTTP 状态/解析失败/凭证类)进 `last_error`,凭证值永不落日志/告警。

### D10: 阿里云 RPC 签名(aliyun-bss)

- 固定端点 `https://bssopenapi.aliyuncs.com/`,`Action=QueryAccountBalance, Version=2017-12-14, Format=JSON`,POST form。
- 公共参数:`AccessKeyId, SignatureMethod=HMAC-SHA1, SignatureVersion=1.0, SignatureNonce(uuid v7), Timestamp(UTC YYYY-MM-DDTHH:mm:ssZ)`。
- 签名:参数按 key 排序 → RFC3986 percentEncode(含 `+→%2B, *→%2A, %7E→~` 特例)拼 canonical query → `StringToSign = "POST&%2F&" + encode(canonical)` → `base64(HMAC-SHA1(SK+"&", StringToSign))` 追加 `Signature` 参数。
- 成功判定:HTTP 200 且响应含 `Data.AvailableAmount`(十进制串,CNY);错误响应含非空 `Code` → 分类为凭证/权限类(`InvalidAccessKeyId` 等)进入失败容忍路径。
- 安全:AK/SK 只经 `${VAR}` 环境变量注入;RAM 子账号最小权限(AliyunBSSReadOnlyAccess);SK 在内存不进日志、不进告警上下文、不落库。文档附最小权限配置说明。

### D11: 火山引擎 Signature V4(volc-bss)

- 固定端点 `https://open.volcengineapi.com/`,`Action=QueryBalanceAcct, Version=2022-01-01`,service=`billing`,region=`cn-north-1`。
- 标准 V4 五步:canonical request(method/uri/query/canonical headers/signed headers/payload SHA256)→ string-to-sign(算法/时间/scope/请求哈希)→ 派生密钥链(`HMAC-SHA256` 四层:SK→date→region→service→"volcengine")→ Authorization 头。与阿里云同等地只经环境注入、最小权限(IAM 子账号 + 账务只读策略)。
- 响应余额字段形态未实证(见 Open Questions):解析器按「找到账户可用余额数值字段」实现并容错,真机冒烟定字段;冒烟不过则该 kind 保留但文档标注「待实证」,不阻塞其余能力。

### D12: 管理 API 与前端

- 三端点进 `adminRouteTable` 表驱动注册(与既有纪律一致):`GET /admin/v1/balances`(总览,聚合 `provider_balances` + 实时燃速查询 + 告警态)、`POST .../manual`(录入,校验金额十进制 + 币种)、`POST .../refresh`(单渠道立即查询);鉴权/审计沿用管理面双轨与审计包装。OpenAPI 单源同步 + `pnpm generate:api` 再生。
- 前端新页 `web/src/pages/Balances.tsx`,TABS 注册(6→7,底部导航 `grid-cols-7` 同步):桌面 = Card + DataList 表格(每渠道:来源徽章、每 key 余额、有效余额、7 天日耗、剩余估算「≈ N 天 / 无消耗 / 数据不足 / 币种不一致」、告警态 StatusBadge、最近检查),窄屏走 DataList 实体卡;手动录入用页头 Card + FormRow + select 选渠道(对齐密钥页签发表单范式);「立即刷新」按钮逐渠道行内。金额列 `numeric` 右对齐 tabular-nums。
- 手动录入不是破坏性操作(可重复录入覆盖),用常规表单,不涉 AlertDialog。

## Risks / Trade-offs

- [上游余额端点无速率 SLA] → 每渠道每小时 ≤ 池大小次 GET(个位数请求),10s 超时,错峰;真实负载可忽略。
- [billing-compat 部署间语义漂移(one-api 关闭货币显示时返回 quota 数而非 USD)] → 文档声明前置条件(货币显示开启);解析加量级护栏(金额 > 1e7 USD 视为非货币形态,判失败并记错误码),真机冒烟核实。
- [火山响应字段未实证] → Open Questions;解析容错 + 冒烟裁决;冒烟不过则该 kind 如实标注待实证。
- [燃速均值对突发敏感] → 7 天窗口 + 最小样本 2 天 + 迟滞 1.2;残余抖动属估算本质,「≈」措辞管理预期。
- [快照后衰减与上游真实扣费的口径差(上游分时计价、缓存价近似)] → 上游定期查询天然校正(快照覆盖衰减累计误差);文档明示估算口径。
- [AK/SK 泄露面] → 仅环境变量注入、云侧只读最小权限、日志/告警/存储零凭证;README 安全清单提示。
- [控制台新增第 7 页签后移动端导航列数] → 底部导航 grid 列数与 TABS.length 联动已在既有注释约定,走查覆盖 375 档。
- [评估循环与 SIGHUP 的边界] → balance 配置改动需重启(非拓扑域),文档与既有 auth/metrics 不热更登记同口径,避免「改了没生效」的运维困惑。

## Migration Plan

1. 迁移 `0015_balance.sql` 追加两张表(已应用迁移禁改纪律);新表对既有读写零影响。
2. 全部渠道缺省 `source: none` + `balance.enabled: true`:升级后零行为变化(零请求/零落库/零告警),声明来源的渠道逐个启用。
3. 回滚 = 移除配置声明(观测停止);表留存无害,无需迁移回退。

## Open Questions

- `volc-bss` 的 `QueryBalanceAcct` 响应余额字段精确路径:待真机 AK 冒烟实证(D11 已定容错策略,不阻塞其余任务)。
- `billing-compat` 在主流 new-api 实际部署(含 owner 冒烟所用实例)的响应形态:实施期真机核实一次,量级护栏阈值据此定。
- `moonshot` 国际站(`api.moonshot.ai`)余额端点是否与国内站同形:文档只证国内站;国际站 key 使用者首次接入时冒烟核实,不同形则解析器加站点分支。
