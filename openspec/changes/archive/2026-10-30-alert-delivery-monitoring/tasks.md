# Tasks

## 1. 存储与 Notifier 挂点

- [x] 1.1 store:`alert_delivery_status` 单行表迁移(只追加)+ 失败 upsert(推进计数与最近事实)/ 成功解除(置解除时刻、保留事实)/ 查询三函数;单测覆盖 失败→查得、持续失败更新不新增、成功解除、解除后再失败重新计数,验证 `go test ./internal/store`
- [x] 1.2 alerting:`Options` 增投递终局回调(最终失败/成功两点,nil 零开销,回调 panic 防护),deliver 挂点接线;alerting_test 用例(失败回调恰一次、成功回调、nil 回调零行为)通过 `go test ./internal/alerting`

## 2. 端点与装配

- [x] 2.1 server:`GET /admin/v1/alerts/delivery-status`(configured/failing/失败事实三态,鉴权与审计按管理面惯例,handler 测试覆盖 关闭态/失败态/解除态);`contracts/openapi.yaml` 同步并 `pnpm generate:api` 再生类型
- [x] 2.2 cmd/muniu/main.go:回调→store 写函数装配接线,`go build ./...` 通过

## 3. 前端与文档收尾

- [x] 3.1 web:总览页未解除失败 NoticeBanner(configured&&failing 渲染,解除/未配置不渲染)+ API 封装;组件测试三态用例,验证 `pnpm typecheck && pnpm lint && pnpm lint:css` + vitest
- [x] 3.2 `docs/design/balance-alerting.md`「事件与通知」节补投递失败自监控语义(落库/解除/不递归/未配置零行为)
- [x] 3.3 全量门禁 `make check` 全绿 + `openspec validate alert-delivery-monitoring --strict` 通过
