# Proposal

## Why

跨仓质量门禁 handoff(../QUALITY-GATES-HANDOFF.md)§2 P0-1、§5 批次 1:secret 是六个仓最先要铺的 P0 门禁——成本最低、风险最高。muniu 现有阻断门禁只覆盖 vet/lint/race/覆盖率/build 与 web 五件套,凭据一旦提交即进 git 历史,清理需要改写历史,代价远高于入口拦截。

## What Changes

- 新增 `.gitleaks.toml`:启用 gitleaks 默认规则集(pinned v8.30.1),allowlist 只做路径级(模板自带的 fixtures/docs 图片 + 一条验收证据摘要清单的已核实误报,逐条注明原因/负责人/到期日)
- 新增 `.pre-commit-config.yaml`:本地 gitleaks hook(v8.30.1),在提交前拦截
- 新增 `.github/workflows/security.yml`:`secrets` job 独立于按路径裁剪的 `ci.yml`,push main 与 pull_request 均触发,`timeout-minutes: 10`,gitleaks 命中即非 0
- 分支保护把 `security / secrets` 设为 required check(仓库设置变更,需 owner 授权后执行,不在本 change 的代码 diff 内)

## Capabilities

无。门禁类改动按 handoff §0 走 `skip_specs`:不新增、不修改产品规格;归档时 `openspec archive --skip-specs`。

## Impact

- 新增 3 个文件:`.gitleaks.toml`、`.pre-commit-config.yaml`、`.github/workflows/security.yml`
- 不动 Go/web 代码,不影响运行时行为与既有 `make check`/`ci.yml`
- 唯一已知误报:`docs/acceptance/2026-10-13-review-evidence/scope.json`(文件路径 → sha256 摘要清单,键名含 `key` 被 generic-api-key 按熵命中),按路径豁免;历史提交无法改写,修复不适用于该文件
- 遗留:分支保护 required 需 owner 授权;CI 实跑受仓库 GitHub 账号 billing lock 阻塞(见 HANDOFF),以本地等价命令留证
