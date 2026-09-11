# conformance:把「兼容」变成测试集

> muniu 设计复盘系列 ③。对应实现:`conformance/` 目录、`internal/adapter/conformance_test.go`。定位:**测试集是独立资产,网关是它的第一个消费者**(muniu 的差异化立身之本,ADR-0003)。

## 「兼容 OpenAI」是一个没有测试的承诺

每一家国产大模型 API 都自称 OpenAI 兼容,但兼容的粒度没人定义:字段名(`reasoning_content` 有没有)、位置(缓存命中在 details 里还是顶层)、时序(usage 帧在 [DONE] 前还是根本没有)、错误码(401 是 authentication_error 还是自己的码)——每一条都可能是某家 SDK 用户半夜撞上的 bug。

one-api 们的做法是用大量 if/else 表达这些差异(所谓「经验适配」)。muniu 的做法不同:**先把差异固化成数据(fixture),再让代码去消费它**。fixture 变更必须连同官方文档链接一起提交,修复必须先补一个能复现 bug 的 fixture——先红,再绿。

## fixture 的解剖

每个 fixture 是一个自包含 JSON,四段:

```json
{
  "provider": "deepseek", "scenario": "usage-cache-tokens",
  "source_url": "https://api-docs.deepseek.com/api/create-chat-completion",
  "inbound":    { "OpenAI 形态的入站请求" },
  "upstream_raw": { "body": { "脱敏的上游原始响应" } },
  "expect":     { "usage": {"prompt_tokens":100,"cached_tokens":64} }
}
```

三条纪律(写进了 `conformance/README.md`,执行时逐条对照):

1. **`source_url` 必填**——每个字段断言可溯源,这是与「拍脑袋兼容」的分界线。muniu 的 DeepSeek 缓存字段断言能直接链接到官方 API Reference;
2. **不入真实 key、不含用户数据**——fixture 是可以开源的数据集,不是日志倾倒;
3. **CI 只回放 fixture,真上游冒烟走手动档**——测试集的回放不依赖任何外部服务的可用性与计费。

## 回放器:一个测试消费整个目录

`internal/adapter/conformance_test.go` 遍历 `conformance/<provider>/<scenario>.json`:

- 非流式:把 `upstream_raw.body` 的 usage 交给适配器 `NormalizeUsage`,与 `expect.usage` 逐字段比对;
- 流式:逐事件断言 `meta`(has_content/is_usage/is_done,驱动首 token 判定与计量),passthrough 与上游字节**逐字节等价**(字段改写白名单:兼容型供应商零改写),usage 帧合并后归一比对。

fixture 里还有一个自我描述的开关:`passthrough_lossless=false` 只给桥接型(Anthropic)用——它们是唯一**必须**改写字节的适配器(异构协议翻译成 OpenAI 形态)。

## 它如何改变开发顺序

Phase 3 给 DeepSeek 做缓存计费接入时的实际顺序:

1. 官方文档核对:`prompt_cache_hit_tokens` / `prompt_cache_miss_tokens` 是顶层字段(不是 OpenAI 的 details 嵌套),`source_url` 记录锚点;
2. 先写 `deepseek/usage-cache-tokens.json`,期望 `cached_tokens:64`——基座适配器跑它是**红的**(基座读的是 details 嵌套,读到 0);
3. 在 `quirks/deepseek.go` 落地字段重映射——变绿。

红→绿之间那一步就是 quirk 的全部实现,没有多余代码。之后任何重构(比如适配器接口签名调整)只要 11 个 fixture 还是绿的,兼容性就没有回归——**测试集是资产,网关是消费者**,资产不坏,消费者怎么改都有底。

## 覆盖现状

截至本文:6 家(deepseek×2 / moonshot / glm / qwen / doubao / openai×2)+ Anthropic 桥接×3(纯文本流/工具流/thinking 流)= 11 个 fixture 全绿,每一个的 `source_url` 都指向官方文档锚点。Gemini 桥接与各家 tool_calls 增量拼装顺序在 backlog,按同样的流程补。
