# Tasks

## 1. 控制台

- [x] 1.1 Playwright + axe 登录页冒烟（web-check 绿）
- [x] 1.2 jscpd/size-limit 接入（阈值取实测基线；web-check 绿）

## 2. 文档

- [x] 2.1 markdownlint 接入 `make infra-lint` 与 CI；94 文件 0 issues
- [x] 2.2 `.zcode/**`、归档变更、历史验收记录排除；`.zcode` 机械改动已回退

## 3. 供应链

- [x] 3.1 govulncheck 移入 security.yml（无 `if: go`、周排程）
- [x] 3.2 syft SBOM 作业（CycloneDX + artifact）
- [ ] 3.3 推送后 CI 实跑（外部账单/授权）

## 4. 验证

- [x] 4.1 `make check` exit 0（含 web-check、插件覆盖率 70.45%）
- [x] 4.2 actionlint 通过
