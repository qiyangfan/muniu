# Design

## Context

`alerting.Notifier.deliver` 在退避序列耗尽后仅留日志(见 internal/alerting/alerting.go deliver);Notifier 被 balance 与 proxy 两处消费,自身无 DB 依赖、构造于 main 装配。失败状态的消费方是「人」——管理面,而非任何程序化重试。见 proposal.md「Why」。

## Goals / Non-Goals

**Goals:**

- 失败事实持久化、可查询、可解除,UI 一眼可见「通道正在断」。
- alerting 包依赖方向不变(不 import store),落库经注入回调完成。

**Non-Goals:**

- 不做事件回放/补发队列(通道恢复后丢失事件不重投,尽力通知语义不变)。
- 不做 per-event 失败历史表(消费方只关心「通道是否活着」与最近一次失败事实)。
- 不做失败状态的 webhook 外发或任何程序化升级(递归陷阱,见 spec)。

## Decisions

**D1 结果挂点 = Notifier 注入回调,不是 notifier 持库,也不是借道 balance 转发。**
`Options` 增投递终局回调(最终失败:事件事实+错误归类;成功:事件键),deliver 在终局两处挂点调用;回调 nil 时零开销,调用侧 defer recover 防御(投递已在独立 goroutine,不得因自监控 panic 反噬)。备选:notifier 直持 *sql.DB——包依赖倒挂;balance service 转发——proxy 侧事件不经过 balance,覆盖不全。main 装配时把回调接到 store 写函数。

**D2 存储形态 = 单行状态表,不是事件历史。**
`alert_delivery_status` 单行(failing、最近失败事件事实、连续失败计数、最终失败时刻、解除时刻、更新时刻):失败 upsert 推进计数,成功仅在 failing 时解除(置 resolved_at,保留失败事实供追溯),历史失败不删。备选 per-event-key 行集——增长无界且 UI 不消费。并发语义:多 deliver goroutine 并发回调,upsert last-writer-wins 可接受(时刻近邻,无正确性敏感序)。

**D3 端点返回三态,未配置显式化。**
`GET /admin/v1/alerts/delivery-status` 返回 `configured`(webhook 未配置=false)+ 失败态事实;configured=false 与无失败均为 200(不是错误,前端按字段渲染)。鉴权与审计纳入既有管理面惯例;契约进 OpenAPI 单源并再生前端类型。

**D4 UI = Overview 页 NoticeBanner。**
`configured && failing` 时渲染警示横幅(最近失败摘要+时刻),解除或未配置不渲染;数据随总览页既有加载节奏获取(独立轻端点,不与余额总览耦合——投递状态覆盖全部事件族,不只余额)。

## Risks / Trade-offs

- [回调内落库失败→自监控自身失效] → 只留日志、不影响投递路径;自监控是尽力而为面,与「告警丢失不拖垮网关」同序。
- [成功解除依赖「后续有事件投递成功」:长时间无新事件时失败态持续展示] → 语义正确(确实未证明恢复);横幅文案含最近失败时刻,运维可自行研判。
- [单行覆盖丢失并发失败明细] → 计数与最近事实足够;明细是日志的事。

## Migration Plan

新表只追加迁移;端点与横幅均为新增面,升级即生效,无数据回填;回滚=回退二进制(新表闲置无害)。

## Open Questions

(无)
