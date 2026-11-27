# ADR-0026:failover 首 token 前预算与池耗尽信封

- 状态:已接受(2026-11-27;openspec `failover-hardening` 承载)
- 背景:候选循环只有三层单次超时,没有首 token 前的阶段总预算;N 个候选各 60 秒时客户端可能被拖到 N×65 秒。key 池全不可用时统一写 502 `all_candidates_failed` 且最后错误类为空;`Retry-After` 只识别整数秒。
- 关联:ADR-0019(渠道健康)、ADR-0025(key 池桶结构)、ADR-0027(错误证据作用域)、openspec `failover-hardening`

## 决策

1. **预算是首 token 前的阶段上界,不是整请求总超时**:候选循环入口记录 deadline;每次 attempt 前检查 `failover_deadline_sec` 与 `max_attempts`;首 token 已提交后预算失效,长响应不受影响。三层单次超时模型不变。
2. **缺省 120 秒 / 4 次,显式 0 = 不限**:120 秒约两个首 token 超时,覆盖常见 2–3 候选;4 次覆盖正常路由长度。0 用于灰度回退;跳过候选不计入次数。
3. **超限信封**:返回 504 `failover_budget_exhausted`,message 携带最后错误类与已完成尝试数;不触上游、不新增 usage 行(已发生 attempt 各自计量);若已提交首 token,按现状断言出口,不因预算改写已输出响应。
4. **Retry-After 支持 delta-seconds 与 HTTP-date**:HTTP-date 转成到当前时刻的时长;缺失/非法/过去时刻回落既有指数阶梯,不得当成 0 秒冷却立即重试。
5. **池耗尽分级信封**:全部候选均因 key 池不可用而跳过且无真实上游错误时,全部为限频/欠费冷却 → 429 `keypool_rate_limited` + 最早恢复 `Retry-After`;存在禁用或混合原因 → 503 `keypool_exhausted`(有计时冷却时带 `Retry-After`)。不得落 502 空错误类;零上游调用、零 usage 行。混合了健康熔断/oauth/桥接跳过时维持既有具体信封或 `all_candidates_failed`。
6. **契约变更**:新增配置、默认预算行为、504 与 429/503 错误码;实施前同步 `docs/design/proxy-and-streaming.md`、错误信封文档与 OpenAPI。

## 备选与否决

- 只设整请求总超时:会误杀已开始的长回答——否决。
- 默认关闭(0):等于不修缺口——否决;保留显式 0 作为回退。
- 池耗尽统一 502:客户端无法区分限频与不可用,也拿不到退避提示——否决。
- 池耗尽统一 429:禁用 key 不是限频语义,误导客户端重试——否决。

## 后果

- 配置缺省即生效;多候选/长挂起请求行为变化,发布说明标注;显式 0 可回退。
- 需要 key 池提供跨桶最早恢复的只读视图(依赖 ADR-0025)。
- 测试必须覆盖:候选挂起命中预算、首 token 后不截断、0=不限、跳过不计数、Retry-After 四形态、池耗尽 429/503/混合原因。
