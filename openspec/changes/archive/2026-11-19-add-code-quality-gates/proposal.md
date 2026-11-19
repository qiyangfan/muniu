# 提案：落地代码质量门禁（复杂度/体积/行长/import 卫生/安全）

## 为什么

`make check` 现有 lint 只跑 golangci-lint 默认集，缺少对「上帝函数、圈复杂度、超长行、依赖方向倒置」的结构性约束。AGENTS.md 已把包依赖方向写成纪律，但没有机器执法，重构后容易回退；跨仓 handoff 要求对齐 PMD 等价门禁（复杂性、体积、行长、import 卫生），且不得靠豁免蒙混。

## 变更内容

- `.golangci.yml` 启用主流 linter 并全部使用原生默认参数：`funlen`(60 行/40 语句)、`cyclop`(10)、`gocognit`(30)、`nestif`(5)、`maintidx`(20)、`lll`(120)、`dupl`(150)、`gosec`、`revive`、`misspell`、`unconvert`、`depguard`。
- `depguard` 把 AGENTS.md 的包依赖方向固化为规则：`protocol` 不得依赖 adapter/proxy/store，`config` 不得依赖 store，`adapter` 不得依赖 proxy，`store` 为叶层。
- 结构类指标（funlen/cyclop/gocognit/nestif/maintidx/lll/dupl/gosec/depguard）只作用于生产代码。行文口径：测试与 `internal/testkit` 不参与体积/复杂度判定，对齐 PMD 默认 `includeTests=false`；这不是给生产代码豁免。正确性与安全规则（vet/errcheck 等标准集）仍覆盖全仓。
- 治理存量：拆分超限热点函数（行为保持），修正安全发现；不修改任何 linter 阈值、不新增结构类排除、不以 `//nolint` 压制复杂度类问题（gosec 假阳性仅允许带具体原因的定点 `//nolint:gosec`）。

## 影响面

仅工程门禁与内部实现结构，协议语义、配置格式、存储 schema、日志语义、错误信封均不改变；`make check` 保持单一完成标准。

## 非目标

不引入新的第三方静态分析平台；不做跨仓共享配置；不调整 CI 触发路径。
