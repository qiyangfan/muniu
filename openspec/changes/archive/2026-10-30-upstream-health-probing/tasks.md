# Tasks

## 1. 健康状态机与被动统计(测试先行)

- [x] 1.1 internal/health 状态机纯函数:相位迁移表(healthy→degraded→open 到期→half-open→recovered/回退降级)、降级原因三值(控制面/推理面/被动)与同层恢复判据(免费探针翻案控制面、被动降级拒收探针解除)、连续失败阈值、探针 429 维持原态、半开成败分叉;表驱动单测验证 `go test ./internal/health`;突变验证:临时破坏阈值判定/同层恢复守卫/半开迁移确认测试变红后还原复绿
- [x] 1.2 被动统计:别名环形窗口换道率(最低样本数不足不判定、适配性豁免不计、降级跳过不计换道)+ 渠道失败聚合喂状态机;单测验证 `go test ./internal/health`

## 2. 两层探针

- [x] 2.1 免费控制面探针:各 adapter 声明免费端点路径(OpenAI 兼容族 `GET {base_url}/models`、anthropic `/v1/models` 等),经渠道 key 池取 key 发控制面请求(缺省全开、渠道退订、总闸),结果只进健康判定;单测钉死「零触碰 keypool 状态」与「429 维持原态」,验证 `go test ./internal/health ./internal/adapter`
- [x] 2.2 付费推理探针(声明制):`probe_model` 渠道复用 adapter 出站栈发最小 chat 请求(max_tokens 微值、独立超时);未声明渠道零请求断言用例;探针循环(启动首轮/间隔/错峰/panic 按渠道隔离/优雅停止)验证 `go test ./internal/health`

## 3. 路由挂接与告警(测试先行)

- [x] 3.1 proxy:候选循环收缩跳过(先于取 key、不计错误类)+ 单候选降级期 503 `provider_circuit_open` 快速失败(零上游调用、零计量行)+ 换道信号输出;fake 上游剧本验证判定表相邻格,验证 `go test ./internal/proxy`
- [x] 3.2 三事件发射(provider_degraded/provider_recovered/switch_rate_exceeded,事件键/严重级/载荷上下文)+ 与粘性路由候选域收缩既有契约对齐的回归测试,验证 `go test ./internal/proxy ./internal/balance`

## 4. 配置与文档

- [x] 4.1 config:顶层 `health` 节(enabled/free_probe_enabled/probe_interval_sec/probe_timeout_sec/failure_threshold/open_seconds/switch_window_sec/switch_rate_milli/min_switch_samples)+ `providers[].health`(free_probe 退订、probe_model 声明制),校验正整数域、无 key 渠道声明推理探针拒绝、攒齐报全;config_test 表驱动 + `muniu.example.yaml` 同步,验证 `go test ./internal/config`
- [x] 4.2 docs:新建 `docs/design/upstream-health.md`(信号源/状态机/路由挂接/信封契约);`docs/design/proxy-and-streaming.md` 判定表补收缩跳过与快速失败两格;新立 ADR(被动熔断缺省生效的路由行为变化)

## 5. 管理面

- [x] 5.1 server:`GET /admin/v1/upstream-health`(相位/探针结果/换道率,鉴权与审计按管理面惯例,handler 测试覆盖 开启/关闭/降级三态);`contracts/openapi.yaml` 同步并 `pnpm generate:api` 再生类型
- [x] 5.2 web:Providers 页渠道健康态展示(桌面表格列 + 移动卡片徽标,降级显著标识)+ API 封装;组件测试(降级标识/正常态/关闭态),验证 `pnpm typecheck && pnpm lint && pnpm lint:css` + vitest

## 6. 收尾

- [x] 6.1 全量门禁 `PATH=/tmp/gclbin:$PATH make check` 全绿 + `openspec validate upstream-health-probing --strict` 通过
