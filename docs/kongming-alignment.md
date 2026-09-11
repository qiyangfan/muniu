# kongming 模型别名对齐表(T5.1;2026-09-11 已回填)

> muniu 侧为 kongming 灰度准备:`scripts/bootstrap-kongming.sh` 创建租户与专用 key。
> 本表逐行核对 kongming `.env` 与代码中的模型名,确保 muniu `model_routes` 全覆盖。

| kongming 侧模型名(.env/代码) | muniu model_routes 别名 | 上游 provider | 上游 model | 状态 |
|---|---|---|---|---|
| qwen-plus(settings.llm_model 缺省,主力) | qwen-plus | dashscope | qwen-plus | ✅ 已配 |
| qwen-vl-max(MODEL_REGISTRY_JSON 条目 name=qwen-vl,vision 路由) | qwen-vl-max | dashscope | qwen-vl-max | ✅ 已配 |
| text-embedding-v2(settings.embedding_model 缺省,base_url 回退 llm_base_url) | text-embedding-v2 | dashscope | text-embedding-v2 | ✅ 已配 |

核对说明:
- kongming LLM 出口仅 agent 面(apps/agent);api 面零 LLM 调用(ADR-002 分工)。
- kongming 现状直连 `https://dashscope.aliyuncs.com/compatible-mode/v1`(openai 兼容),
  muniu 侧 provider 用 openai 基座同端点直连——请求/响应形态与直连同构,零行为差异。
- embedding 批量上限 25/批是 kongming 侧分批逻辑,经 muniu 透传后上游约束不变。
- 别名与上游模型名直通(不换名),kongming 侧切换只改 base_url/key 两项。

## 限额计划(traffic-governance 流程)
- rpm_limit / tpm_limit / daily_quota_micro:按 kongming 现有用量 ×2 起步,灰度一周后回调;
  bootstrap 起步值 rpm 120 / tpm 不限 / 日配额不限。
- 灰度路径:先切非关键数据源一周 → 全量;每日对账(scripts/reconcile.sql),差异 >1% 立案。
- 语义缓存:kongming 租户灰度期关闭(bootstrap 默认),基线确认后再经 PATCH 租户开启。
