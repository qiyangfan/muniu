# 计量与计费(metering)

## 计量语义(契约)

每个请求一行 `usage_logs`(含失败请求,`error_class` 区分),字段与来源优先级:

1. **上游 usage 帧**(归一表见 upstream-adapters,quirk/桥接各自落地)——唯一权威来源;
2. **本地估算**(上游缺失/断言中断):输入=请求 messages 序列化后 tiktoken 计数;输出=已转发 delta 文本累计计数;`estimated=true`。

估算实现(Phase 2):`internal/metering/tiktoken` 封装,编码按模型前缀映射(未知模型默认 `o200k_base`),BPE 词表**编译期嵌入二进制**(禁止运行时下载——单二进制零外联纪律);中文场景误差容忍 ±10%,`estimated` 行在成本对账中默认剔除。reasoning/cached token 估算不到就置 0 + estimated,不猜。

## 计价(整数运算,零浮点)

- 内部记账单位:**货币的百万分之一(μ)**——`cost_micro`(μCNY / μUSD,由 `currency` 列区分),int64。
- 配置价目**人类可读**(如 `input: 2.00` 表示 ¥2.00/百万 Token),加载期一次性换算为整数 `μ/百万Token`,此后全链路纯整数:
  `cost_micro = ceil(in_tokens × pin / 1e6) + ceil(out_tokens × pout / 1e6)`(逐段向上取整,误差 ≤2μ/请求,对供应商友好;规则进 conformance 断言)。
- cached/reasoning 差异价(Phase 3 落地):pricing 增列 `cached_input`(缺省=input),计价扩为三段
  `ceil(未命中输入×input) + ceil(缓存命中×cached_input) + ceil(输出×output)`(实现 `metering.CostWithCached`,逐段独立取整)。
- **禁止 float 参与任何金额运算**(conventions 铁则);配额扣减即 `SUM(cost_micro)` 聚合,同一整数域。

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
- 聚合读侧(auth/quota 中间件消费):当日已耗 = `SUM(cost_micro) WHERE created_at ≥ 当日 AND estimated=false`,进程内缓存 30s(配额一致性声明见 traffic-governance)。
- 每请求 slog 成本字段:`prompt_tokens/completion_tokens/reasoning_tokens/cached_tokens/cost_micro/estimated`。

## 测试要求(测试先行域)

计价整数运算表驱动(含边界:0 token、恰 1e6、向上取整临界)· 估算路径(usage 缺失→estimated 置位)· 断言中断只计已转发部分 · channel 满背压行为 · 批量落库与优雅排干。
