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
- 灰度路径:一周正常使用无故障(2026-09-12 判据修订:个人本地项目不适用生产式每日对账,月末与上游账单一次量级核对);reconcile.sql 保留作按需自检工具。
- 语义缓存:kongming 租户灰度期关闭(bootstrap 默认),基线确认后再经 PATCH 租户开启。

## 切换状态(2026-09-11)
- 租户 kongming 与专用 key 已签发(bootstrap);kongming `.env` 三处切换完成,
  e2e 问答 + 计量 + 计价实证全过(详见 muniu HANDOFF 验收记录 19)。
- 三别名牌价已录入运营态(`PUT /admin/v1/pricing`,官方定价页 2026-09-11 核对:
  qwen-plus 0.8/2(0-128K 阶梯首档非思考)、qwen-vl-max 1.6/4、text-embedding-v2 0.7,
  CNY/百万 token)。
- 同日晚 muniu 常驻形态迁 podman 容器(命名卷全新库),key 与牌价**重签/重录过一次**
  (数字不变,旧 key 已随旧库作废),对账以新 key 起算。
- 灰度一周自 2026-09-11 起计时,期满无故障 → 全量,本表与 ADR-007(kongming 侧)同步收口。
