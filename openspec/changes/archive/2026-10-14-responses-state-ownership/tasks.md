# Tasks: responses-state-ownership

- [x] 1.1 失败测试先行：跨 key GET/DELETE 404 且上游零调用、跨 key prevID 自解释降级、nil KeyInfo fail-closed、同 key 全链零变化回放（先红）
- [x] 1.2 `affEntry.apiKeyID` + `Put` 三参 + `Get(id, apiKeyID)` 单点匹配（空归属条目永不匹配）；handleChat 成功回写传创建 key；三消费点接新签名
- [x] 1.3 make check 全绿 + validate --all + 既有粘性/状态 e2e 零改动回放核验
