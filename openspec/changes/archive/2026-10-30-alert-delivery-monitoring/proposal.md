# Proposal

## Why

告警管道当前的最终失败处理是「退避重试后放弃并留日志」——webhook 端点宕机期间,余额告警、key 池告警、候选全失败告警全部静默丢失,管理面无任何痕迹,运维既不知道「出过什么事」也不知道「通知通道已断」。告警系统自身的静默失效是最危险的一类故障,且存在递归陷阱:通道挂了无法用通道自报,因此唯一正确的形态是落库持久化 + 管理面可见。

## What Changes

- webhook 投递经退避重试仍最终失败时,失败事实(事件键/严重级/摘要/失败时刻/错误归类)落库持久化;其后任一次投递成功将失败态标记解除,管理面可区分「正在失败」与「历史失败」。
- 投递失败状态本身必须 (MUST) NOT 再经 webhook 外发(通道已断,避免递归)。
- 新增管理端点 `GET /admin/v1/alerts/delivery-status`(纳入既有管理面鉴权与审计),契约同步进 OpenAPI 单源并再生前端类型。
- 管理面总览页在存在未解除的投递失败时展示警示横幅(NoticeBanner),通道恢复后消失;webhook 未配置时端点返回关闭态、无任何行为。
- 事件本体不做回放队列:通道恢复后丢失的历史事件不补发(既有「不干扰主路径、尽力通知」语义不变)。

## Capabilities

### New Capabilities

(无——投递失败可见性是 `webhook-alerting` 能力「事件→去重→退避重试→webhook」管道的自监控延伸)

### Modified Capabilities

- `webhook-alerting`:「告警通知不干扰主路径」requirement 扩展投递最终失败的持久化与解除语义(失败不再只是日志);新增「投递失败自监控」requirement(落库、管理端点、UI 可见、不递归外发、未配置零行为)。

## Impact

- `internal/alerting`:Notifier 增投递结果回调挂点(依赖方向不变,alerting 不 import store,经注入回调落库)。
- `internal/store`:新增投递失败状态表(迁移只追加)与读写函数。
- `internal/server` + `contracts/openapi.yaml`:新端点 + 类型再生(`pnpm generate:api`)。
- `web`:Overview 页未解除失败警示横幅;`cmd/muniu/main.go` 装配接线。
- `docs/design/balance-alerting.md` 事件与通知节(或对应告警设计章节)同步。
