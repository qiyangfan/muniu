# 流式 usage 计量:各家差异与估算兜底

> muniu 设计复盘系列 ②。对应实现:`internal/adapter`(quirks)、`internal/metering`、契约文档 `docs/design/metering.md`、`docs/design/upstream-adapters.md`。可复现测试:`TestConformance/*`、`TestCostWithCached_*`、`TestPipeline_*`。

## 一条 usage,五副面孔

OpenAI 兼容是国产大模型 API 的「普通话」,但 usage 字段恰恰是大家各说方言的地方。muniu 归一化表(upstream-adapters.md,2026-09 官方文档核对)里最典型的三行:

| 统一字段 | OpenAI | DeepSeek | Kimi/Moonshot |
|---|---|---|---|
| cached_tokens | `prompt_tokens_details.cached_tokens` | **顶层** `prompt_cache_hit_tokens` | **顶层** `cached_tokens` |
| reasoning_tokens | `completion_tokens_details.reasoning_tokens` | 同 OpenAI(已确认存在) | —(计入 completion) |
| 流式 usage 帧 | `stream_options.include_usage` 后,[DONE] 前空 choices 帧 | 同 OpenAI | 同 OpenAI,**不开就没有**(估算兜底路径) |

同样的语义,三种字段形态。网关若把「OpenAI 的形状」当真理,DeepSeek 的缓存命中计费和 Kimi 未开 include_usage 的请求就会全部漏记。

## muniu 的两个设计决定

### 1. 归一发生在 quirk 层,不是 DTO 层

统一 Usage(`protocol.Usage`:prompt/completion/reasoning/cached 四个整数)是**对齐基准**,不是解析目标。各家字段差异在 `internal/adapter/quirks/*.go` 各自翻译:

```go
// deepseek quirk:顶层缓存命中字段 → 统一 CachedTokens
return protocol.Usage{
    PromptTokens:     u.PromptTokens,
    CompletionTokens: u.CompletionTokens,
    ReasoningTokens:  u.CompletionDetails.ReasoningTokens,
    CachedTokens:     u.PromptCacheHit, // prompt_cache_hit_tokens
}
```

基座(openai.go)只处理 OpenAI 原生形态;DeepSeek/Moonshot/GLM/Qwen/Doubao 在基座外套 quirk patch——**只声明与基座的差异**,禁止整家覆写(ADR-0003)。每个翻译都钉死在 conformance fixture 上:`deepseek/usage-cache-tokens.json` 断言 `prompt_cache_hit_tokens:64 → cached_tokens:64`,想改字段名先过不了测试。

### 2. 缺失不硬凑:estimated 兜底

流式请求上游不给 usage 帧是常态(Kimi 缺省配置、断流中断)。muniu 的优先级是**上游 usage 帧 > 本地估算**,估算行置 `estimated=1`:

- 输入 = 请求 messages 序列化后 tiktoken 计数(词表编译期嵌入,运行时零下载);
- 输出 = 已转发 delta 文本累计计数(流式边转发边攒);
- reasoning/cached 估算不到就置 0,**不猜**;
- 计价与对账:`SUM(cost_credits) WHERE estimated=0`——估算行只是参考,不进对账口径。

这套兜底的真实价值在故障时:断流断言出口后,已转发部分照常估算计费——供应商少赚的部分如实记录,用户的账单不缺行。

## 计价:整数的仪式感

成本从第一天起就是整数:`cost_micro = ceil(输入tokens×输入价/1e6) + ceil(输出tokens×输出价/1e6)`,逐段向上取整(μ = 货币百万分之一,价格经 `metering.ParsePrice` 从人类可读 "2.00" 解析,全程不碰 float)。测试先行的负面用例还抓过一个真 bug:`strconv.ParseInt` 接受负号,负价差一点漏过校验。

> 修订(2026-09-13):计量基座已由 μ(1e-6)迁移为 credit(1 credit = 本位币 1e-9,迁移 0007),上式与字段名(`cost_credits`)口径相应 ×1000;「整数仪式感」纪律不变。

Phase 3 加入缓存价后扩为三段:`ceil(未命中输入×input) + ceil(缓存命中×cached_input) + ceil(输出×output)`,DeepSeek 的 `prompt_cache_hit_tokens` 直接参与分档计费。

## 一组值得记住的对照

| 家 | 缓存命中字段 | 流式 reasoning | usage 缺失风险 |
|---|---|---|---|
| OpenAI | details 嵌套 | (reasoning 模型) | 低(include_usage 稳定) |
| DeepSeek | 顶层 hit/miss 分列 | reasoning_content,与 content 逐帧互斥 | 低 |
| Kimi | 顶层 cached_tokens | 思考模型才有 | **高:缺省配置流式无 usage** |
| GLM/Qwen/Doubao | 继承 OpenAI 形态 | reasoning_content | 中(以 conformance fixture 实证为准) |

把这张表连同字段来源固化成可回放的 fixture(muniu 的 `conformance/` 目录),「兼容」就从口号变成了 CI 里的一行绿灯。
