# Tasks

## 1. 门禁实现

- [x] 1.1 `.golangci.yml` 启用结构类 linter（funlen/cyclop/gocognit/nestif/maintidx/lll/dupl）与 gosec/revive/misspell/unconvert，全部保留原生默认参数，不设 `//nolint` 白名单
- [x] 1.2 `.golangci.yml` 增加 depguard 分层规则（protocol/config/adapter/store 四条），把 AGENTS.md 包依赖方向变成可执行断言
- [x] 1.3 结构类 linter 的排除仅限 `_test.go` 与 `internal/testkit/`（对齐 PMD `includeTests=false`），生产代码零排除

## 2. 存量治理（先拆热点，再上复杂度门禁）

- [x] 2.1 拆分 `cmd/muniu` 装配层超限函数（assemble 及子命令），行为保持
- [x] 2.2 按包拆分 proxy/adapter/admin/store/metering/auth/balance/health/config/plugin 等热点函数，行为保持，包内测试全绿
- [x] 2.3 治理 lll 超长行与 gosec 发现：优先真实修复，仅假阳性允许带原因定点 `//nolint:gosec`（共 34 处，全部带具体原因，无复杂度类豁免）

## 3. CI 落点

- [x] 3.1 门禁继承 `make check` 与 `ci.yml` 的 golangci-lint-action@v9（version v2.15），本地与 CI 同一配置
- [x] 3.2 全仓 `golangci-lint run ./...` 0 issues（`--max-issues-per-linter=0 --max-same-issues=0`）

## 4. 负向验收

- [x] 4.1 干净树 `make check` 全绿（EXIT=0，coverage gate 73.7%）
- [x] 4.2 逐类植入违规，对应命令非 0，移除后恢复 0：funlen(probeFunlen) / cyclop(12 分支) / gocognit(41) / nestif(15) / maintidx / lll(160 字符) / dupl(两同构函数) / gosec(G401/G404/G501) / depguard(config→store) 全部命中；移除后全仓 0 issues、build OK

## 备注

- 门禁默认 `uniq-by-line` 会对同一行只报第一条（如长函数同时触发 funlen/cyclop/gocognit），属 golangci-lint 默认行为，未调整。
