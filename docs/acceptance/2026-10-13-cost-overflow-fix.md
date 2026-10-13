# 两项遗留修复（2026-10-13）

本轮只承接用户明确点名的旧 token 计费溢出与 OpenSpec 严格占位失败；两项均已修复并验证。检查计费调用方时同步修正 tokenctl 的旧 μ 展示单位。此前冻结任务与评审保持历史记录，未扩为新一轮全仓巡检，未执行 Git 写操作。

## 计费溢出与精度裁决

结论：保留 `1 credit = 10⁻⁹ 本币` 与 int64 存储，使用 128 位中间乘积安全除法，再逐段向上取整、安全求和。缓存价缺省、cached 钳制、现有函数签名和普通业务金额保持兼容。预算与计费共用 metering.CostForTokens，删除重复的预算乘除实现。

int64 最终金额上限为 9223372036.854775807 本币，约 92.2 亿。当前缺陷是除法前的乘积回绕：100000000 token × 1000 本币/百万 token，实际金额只有 100000 本币（100000000000000 credit），中间乘积却为 10²⁰；旧实现算为 7766279631453 credit。宽乘积现已精确算出 100000000000000。

| 维度 | 旧 int64 中间乘积 / 降低精度 | 采用的宽整数计算 |
|---|---|---|
| 复杂度 | 原乘除 O(1)，但无法保证正确；降低精度仍不能消除所有乘积溢出 | 固定三段，O(1)，不引入大整数热路径或新框架 |
| 性能 | 修复前同一动态输入基准 1.878 ns/op | 修复后 6.289 ns/op；两者均 0 B/op、0 allocs/op；单机微基准，不冒认吞吐结论 |
| 兼容性 | 降低精度会改变金额、配额、取整口径 | 普通请求与保存格式不变；真正无法表示的最终金额显式标异常 |
| 测试 | 普通牌价掩盖缺陷 | 六类宽乘积边界、500 组三段 big.Int 对拍、255 组预算对拍及超界全链路 |
| 迁移 | 降低精度需迁移所有历史 credit、价目与额度 | 无迁库、无历史重算；已回绕的历史记录不能据错误 cost 自行恢复，须按原始 usage/价目核对 |
| 长期维护 | 预算与落账各保留一次乘除易漂移 | 单段 checked 算法在计费域复用；三段业务差异保留 |

真正超出最终 int64 金额范围时，经 owner“你帮我决定”授权采用显式未结算语义：保留原始 usage，行 `status=error/error_class=cost_overflow/estimated=true/cost_credits=0`，零值代表未结算；不截顶、不伪造正常零成本账单。proxy 记录 request/tenant/key/provider/model、usage、价格、币种与原上游错误类供核对；HTTP 交付结果不改变、不惩罚上游 key。回归验证 200 响应仍交付，SQLite 异常观测和结构化核对日志均存在。

原 Cost/CostWithCached 以 -1 返回无法结算的信号，所有生产调用方已处理：NewRow 转换为异常观测，tokenctl 拒绝输出金额。学习 CLI 原先仍按 μ（10⁻⁶）格式化 credit，把本币金额放大 1000 倍；现显示 credit 并按九位小数精确格式化，不涉及存储精度变更。CLI 正常例现为 `100000000000000 credit(¥100000.000000000)`；费用超界或负 token 均 exit 1、无伪金额 stdout。

关键位置：internal/metering/price.go · CostWithCached/CostForTokens；estimate.go · NewRow；internal/proxy/metering.go · record/budgetCost；cmd/tokenctl/main.go · formatCredits/runCost。计量设计与 pricing-management 主规格同步。

## OpenSpec 占位

platform-data-plane 的 Purpose 依据已实现 SIGHUP 拓扑加载契约补全：合法变更原子发布、失败保持旧态、未变 provider 保留 key 池状态、身份/管理面/静态价目不热更。仅补能力目的，不扩 Requirements；全仓严格检查从 22/23 转为 23/23。

## 验收记录

原始红绿与命令输出保存在 [证据目录](2026-10-13-cost-fix-evidence/)，包括成本与超界先红后绿、CLI 展示旧值、全链路测试和微基准。

```text
PATH=/Users/solvela/.local/share/mise/installs/aqua-golangci-golangci-lint/2.14.0/golangci-lint-2.14.0-darwin-arm64:$PATH make check
0 issues.
Test Files  6 passed (6)
Tests  15 passed (15)
coverage gate PASS: 69.9%
openspec validate --all --strict
Totals: 23 passed, 0 failed (23 items)
make build：exit 0
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/muniu-cost-fix-linux-arm64 ./cmd/muniu：exit 0
```

聚焦：`go test ./internal/metering ./internal/proxy ./cmd/tokenctl`、三段数学对拍、超界 proxy/SQLite/log 链路、CLI 单位与非零退出均通过。基准以 go test -overlay 的只读旧 price.go 对照同一基准代码，动态输入并保存结果防止消除计算；`-run '^$' -bench '^BenchmarkCost$' -benchmem -benchtime=300ms`。git diff --check 通过，无新增依赖，无前端改动；上一轮前端构建与布局证据有效。

两项遗留均关闭，无新增待确认事项；历史外部凭据与观察期阻塞保持原状，未重试。代码仍未提交、未推送。本轮结束。
