# kongming 模型别名对齐表(T5.1;待 kongming 仓库可访问后回填)

> muniu 侧为 kongming 灰度准备:`scripts/bootstrap-kongming.sh` 创建租户与专用 key。
> 本表逐行核对 kongming `.env` 与代码中的模型名,确保 muniu `model_routes` 全覆盖。

| kongming 侧模型名(.env/代码) | muniu model_routes 别名 | 上游 provider | 上游 model | 状态 |
|---|---|---|---|---|
| (待 kongming 仓库对齐回填) | | | | ⏳ |

## 限额计划(traffic-governance 流程)
- rpm_limit / tpm_limit / daily_quota_micro:按 kongming 现有用量 ×2 起步,灰度一周后回调;
- 灰度路径:先切非关键数据源一周 → 全量;每日对账(scripts/reconcile.sql),差异 >1% 立案。
