# Phase 0:工具链与仓库初始化(bootstrap)

> 状态:已完成(T0.1–T0.8,2026-09-09;T0.9 待用户执行) · 前置:无 · 版本:— · 允许新增依赖:**无(仅标准库)**

## 目标

让仓库「能开发」:工具链齐、骨架可编译、门禁打通、CI 亮绿,并交付一个练手 CLI(tokenctl)把 Phase 2 要用的计价换算函数**测试先行**地做出来。

## 任务清单

- [x] **T0.1 仓库初始化**:`cd /Users/solvela/workspace/muniu && git init -b main`;`git status` 确认 `bin/ data/ muniu.yaml` 不出现(.gitignore 生效)。
- [x] **T0.2 Go 工具链**:`go version` 须 ≥ 1.25。缺失或过旧 → `brew install go`(用户已预授权超长任务自治执行工具链安装);安装后再次 `go version`。**go.mod 的 `go 1.25` 行不动**——本机更新无害,本机更旧则升工具链而非降 go.mod。把版本号记入 HANDOFF 验收记录。
- [x] **T0.3 骨架编译验证**:`go vet ./... && go build ./...`;`MUNIU_ADDR=127.0.0.1:8787 go run ./cmd/muniu &` 后 `curl -s 127.0.0.1:8787/healthz` 期望 `{"status":"ok","version":"dev"}`;结束后台进程。编译报错按报错修(实现级自由)。
- [x] **T0.4 golangci-lint**:`brew install golangci-lint`(或官方安装脚本);`golangci-lint version` 须为 v2;`golangci-lint config verify` 校验 `.golangci.yml`——schema 不兼容处按报错最小修正(实现级);`make lint` 绿。
- [x] **T0.5 CI/发布文件核对**:核对 `.github/workflows/*.yml` 中 actions 的最新 major 版本并更新;若已安装 goreleaser 则 `goreleaser check`,未安装不装(Phase 2 发版前再装)。
- [x] **T0.6 计价换算(测试先行)**:新建 `internal/metering/price.go`:
  - `ParsePrice(human string) (micro int64, err)`:人类可读十进制字串("2.00"、"0.15")→ 整数 μ/百万Token;**字符串解析,不经 float**(逐位十进制,最多 6 位小数,超出报错);
  - `Cost(inTokens, outTokens int64, inMicroPerM, outMicroPerM int64) int64`:逐段 `ceil(tokens×price/1e6)` 求和(metering 契约);
  - 先写 `price_test.go` 表驱动(0 token / 恰 1e6 / 向上取整临界 / 6 位小数 / 非法字串),跑红,再实现跑绿。
- [x] **T0.7 tokenctl CLI**:`cmd/tokenctl/main.go`,`flag` 子命令两个:
  - `tokenctl cost --currency CNY --in-price 2.00 --out-price 8.00 --in 1200 --out 800` → 打印各段与合计(μ 转人类可读:`¥0.008800`,6 位小数);
  - `tokenctl chars < file` → 字节数/字符数/中文字符数与「粗估 token(中文 ≈ 字符数×0.7,英文 ≈ 单词数×1.3)」并**明示这是粗估非 BPE**;
  - 复用 T0.6 函数;`go build ./cmd/tokenctl` 通过;三条命令实录进 HANDOFF。
- [x] **T0.8 门禁与交接**:`make check` 全绿;更新 HANDOFF(状态/验收记录),把「用户侧待办」列清:GitHub 建仓查名(`solvela/muniu` → 备选 `muniu-gateway`,需同步改 go.mod 模块路径与 Makefile `PKG`)、首提交、push。
- [ ] **T0.9 用户执行项(agent 只准备命令,不执行)**:
  ```bash
  git add -A && git commit -m "chore: 仓库初始化——治理文档、设计契约、六阶段计划与可编译骨架"
  git remote add origin git@github.com:solvela/muniu.git && git push -u origin main
  ```
  push 后 README 顶部补 CI badge(`https://github.com/solvela/muniu/actions/workflows/ci.yml/badge.svg`)——此为 push 后的一次 docs 改动,随 Phase 1 提交搭车。

## 设计指针

`docs/design/metering.md`「计价」· `docs/conventions.md`「金额整数」「测试范式」· `docs/adr/0001`。

## 验收闸门(全部留证进 HANDOFF)

1. `go version` 输出;2. `make check` 全绿输出;3. `/healthz` 响应原文;4. tokenctl 三条命令实录;5. `golangci-lint config verify` 通过;6. (若用户已 push)CI 首跑绿链接。

## Non-goals

任何业务逻辑 · 配置加载 · 真上游调用 · 引入任何第三方依赖。
