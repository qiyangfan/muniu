# 计量与计费(metering)

## 计量语义(契约)

**每次上游尝试一行** `usage_logs`(含失败请求,`error_class` 区分)。failover 换道 N 次即同 `request_id` 落 N 行——每行如实对应一次真实上游调用与其成本归因(provider/model/错误类),合并成「每请求一行」会丢失重试链路的可追责性。读侧口径由此统一:

- **请求数** = `COUNT(DISTINCT request_id)`(控制台/对账不按行数计请求);
- **token/成本** 只取终态成功行(`status='ok'` 携带上游权威 usage);错误/断连行 usage 为 0 或估算,单列作故障证据;
- 语义缓存命中行(`provider='semantic-cache'`)未到达上游,跨系统对账时两侧剔除;
- 对账实现见 `scripts/reconcile.sql`(判据:两侧按日差异 ≤1%)。

字段与来源优先级:

1. **上游 usage 帧**(归一表见 upstream-adapters,quirk/桥接各自落地)——唯一权威来源;
2. **本地估算**(上游缺失/断言中断):输入=请求 messages 序列化后 tiktoken 计数;输出=已转发 delta 文本累计计数;`estimated=true`。

估算实现(Phase 2):`internal/metering/tiktoken` 封装,编码按模型前缀映射(未知模型默认 `o200k_base`),BPE 词表**编译期嵌入二进制**(禁止运行时下载——单二进制零外联纪律);中文场景误差容忍 ±10%,`estimated` 行在成本对账中默认剔除。reasoning/cached token 估算不到就置 0 + estimated,不猜。

## 计价(整数运算,零浮点)

- 内部记账单位:**credit,1 credit = **本位币**(metering.settlement,CNY|USD)的十亿分之一(1e-9)**——`cost_credits`,int64,`currency` 列恒记本位币。选型:①美元廉价档($0.003/百万Token)在 1e-9 下逐 token 恰为整数 credit;②锚定本位币而非各上游账单币种,配额域单币种自洽。异币种报价加载期按 `fx_usd_cny_milli`(1 USD = fx/1000 CNY)一次换算。
- 配置价目**人类可读**(如 `input: 2.00` 表示 ¥2.00/百万 Token),加载期一次性换算为整数 `credit/百万Token`,此后全链路纯整数:
  `cost_credits = ceil(in_tokens × pin / 1e6) + ceil(out_tokens × pout / 1e6)`(pin/pout 单位 credit/百万Token;逐段向上取整,误差 ≤2 credit = 2e-9 本币/请求;规则进 conformance 断言)。
- cached/reasoning 差异价(Phase 3 落地):pricing 增列 `cached_input`(缺省=input),计价扩为三段
  `ceil(未命中输入×input) + ceil(缓存命中×cached_input) + ceil(输出×output)`(实现 `metering.CostWithCached`,逐段独立取整)。
- **分时段倍率**(2026-09-11 开放):pricing 增 `timezone`("UTC" 或 ±HH:MM 固定偏移,缺省 UTC;不引 tzdata——
  单二进制零外联纪律,受 DST 影响的时区应换算为 UTC 表达)与 `time_windows[{window, multiplier}]`:
  `window` 形如 "HH:MM-HH:MM"(终点 `24:00`=当日末;start>end 为跨午夜窗,如 `16:30-00:30`);
  `multiplier` ≤4 位小数、取值 (0,10],作用于整张价目卡(三段统一缩放)。
  换算发生在**加载期一次性完成**(基点整数运算,有效价向上取整——窗口价永不低估),运行时零浮点查表;
  多窗按**声明顺序先匹配优先**(重叠即声明序 precedence),无命中回落缺省价;
  `cached_input` 缺省=input 的语义在窗口内保持(0 值缩放仍为 0,由 CostWithCached 统一解析)。
  设计取舍记录:选倍率而非逐窗全价(厂商错峰价的真实形态是整卡折扣,配置最简);
  判定精确到请求完成时刻(`record` 时选窗,行内价目即该窗有效价,对账可逐行复核)。
- **禁止 float 参与任何金额运算**(conventions 铁则);配额扣减即 `SUM(cost_credits)` 聚合,同一整数域;配额字段 `daily_quota_credits` 以 credit 计(抽象额度单位,可直接表达 820 亿这类自然大数)。

## 动态价目(运营态,2026-09-11 开放)

厂商改价是**运维动作**,不应触碰配置中心更不应重启——价目归入 ADR-0004 的「运行态」一侧:

- **合并语义**:YAML `pricing` 降级为静态兜底;SQLite `model_pricing` 表(迁移 0004)为运营覆盖。
  解析按别名:DB 行存在即整体取代该别名(含窗口),否则回落 YAML;`DELETE /admin/v1/pricing/{alias}` 撤销覆盖即回落。
- **生效路径**:管理面 `PUT /admin/v1/pricing/{alias}`(人类可读价目/倍率,同一套零浮点解析器校验)→ 持久化 →
  重建合并表 → `PricingDirectory` 原子替换。转发只读目录,在途请求用旧表完成,**下一请求即新价,零重启**。
- **入口**:`GET/PUT/DELETE /admin/v1/pricing`(管理 API)+ 控制台「价目管理」页。
- **兜底纪律**:重建时任何非法行(DB 脏数据)使重建整体失败并保持旧目录——宁可维持旧价,不让脏数据进热路径;
  刷新失败时持久化已生效,下次刷新/重启收敛(日志告警)。
- 配置中心的职责边界不变:只管静态拓扑(providers/model_routes/pricing 兜底),改拓扑仍需重启(低频且要求全链一致)。
- **控制面纳管期间撤销覆盖非粘性**:已配置 `control_plane.pricing_url` 的实例,每轮按控制面全量期望同步(内容不变跳过);
  本地 DELETE 撤销会在下一轮被控制面期望重新覆盖——纳管实例的撤销必须在控制面侧执行。

## 异步落库(背压优先于丢单)

```
请求 goroutine --channel(cap 4096)--> flusher(单 goroutine)
                                        每 500ms 或攒满 256 条 → 单事务批量 INSERT
```

- channel 满:**阻塞请求路径**(计量行是钱,宁可让请求等 500ms 级批量写,绝不 drop),同时 warn 日志暴露背压;
- 优雅退出排干(architecture「优雅退出」第 2 步);
- 幂等:`id`=UUIDv7 主键天然去重,重复投递由 `INSERT OR IGNORE` 兜底。

## 对账与可观测

- `estimated=false` 的行为唯一对账口径;`estimated=true` 仅成本参考。
- 聚合读侧(auth/quota 中间件消费):当日已耗 = `SUM(cost_credits) WHERE created_at ≥ 当日 AND estimated=false`,进程内缓存 30s(配额一致性声明见 traffic-governance)。
- 每请求 slog 成本字段:`prompt_tokens/completion_tokens/reasoning_tokens/cached_tokens/cost_credits/estimated`。

## 测试要求(测试先行域)

计价整数运算表驱动(含边界:0 token、恰 1e6、向上取整临界)· 估算路径(usage 缺失→estimated 置位)· 断言中断只计已转发部分 · channel 满背压行为 · 批量落库与优雅排干。
