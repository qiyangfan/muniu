# 设计文档与实现一致性核对记录

> Phase 5 设计指针要求:「全部设计文档回读一遍——生产前最后一次文档与实现一致性核对,发现不一致:改文档对齐实现或立开放项,不许留双版本。」
> 核对日期:2026-09-10 · 核对人:Phase 4/5 执行会话

## 核对范围与结论

| 设计文档 | 结论 | 处理 |
|---|---|---|
| architecture.md | 1 处漂移:包结构列表缺 Phase 1 新增的 `server/`、Phase 4 新增的 `admin/` 与 `web/`(go:embed) | ✅ 已改文档对齐实现 |
| metering.md | 1 处漂移:计价公式仍为两段;Phase 3 落地缓存价后为三段(CostWithCached) | ✅ 已改文档对齐实现 |
| proxy-and-streaming.md | 判定表/SSE 契约/流内错误帧/超时模型与实现一致;26 格判定表测试全绿 | ✅ 一致 |
| upstream-adapters.md | 矩阵 ⚠ 已于 2026-09-10 按官方文档核对回填清零(qwen/doubao/anthropic);Anthropic 文档区域屏蔽项保留备注 | ✅ 一致(残余项在开放决策) |
| traffic-governance.md | key 池三态/WRR/EMA/迭代器/限流与实现一致 | ✅ 一致 |
| data-and-config.md | 1 处缺失:Phase 4 新增 `semantic:` 配置节未记录 | ✅ 已补文档 |
| semantic-cache.md | 准入/阈值/TTL/LRU/命中计量与实现一致;`temperature≤0.3 ⚠`、`8K tokens ⚠`、`TTL 24h ⚠` 等 ⚠ 项已按保守值定稿实现 | ✅ 一致 |
| conventions.md | 错误信封/日志字段/迁移纪律/金额整数在实现中一致 | ✅ 一致 |
| README.md | 快速开始/压测数字/核心语义/控制台说明已按 Phase 4-5 实现更新 | ✅ 一致 |

## 实现侧与文档的已知差异(有意为之,非漂移)

1. **Adapter 接口新增 `RewriteResponse` 方法**(五方法契约之外):桥接型非流式翻译的客观需要,upstream-adapters「实现时可微调签名」条款覆盖;已登记 HANDOFF 开放决策项历史记录。
2. **config `adapter:` 字段语义**:值为适配器类型(openai|deepseek|moonshot|glm|qwen|doubao|anthropic|gemini),provider 实例名可任意取——比初稿「adapter 仅三选一」更精确,匹配 quirk 分类学。
3. **语义缓存表时间列使用 RFC3339Nano**(其余表 RFC3339):短 TTL 测试与亚秒边界需要;同列同格式,字典序比较语义不变。

## 遗留开放项

见 HANDOFF「开放决策项」:DeepSeek 峰谷四档计价 pricing 扩列、Anthropic 文档区域屏蔽复核、(已解决)one-api 对照改用官方 arm64 二进制。
