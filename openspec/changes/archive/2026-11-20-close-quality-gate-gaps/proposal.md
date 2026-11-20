# 变更提案：质量门禁缺口收口（控制台 + 文档 + 供应链）

## Why

门禁盘点发现：控制台缺 e2e/a11y、重复率与体积预算；文档只有链接检查，没有 markdown 结构
门禁；SBOM 只在 release 产出；govulncheck 放在按路径裁剪的 ci 作业里，web-only 变更与
「无代码变更的新披露漏洞」都不会跑。

## What Changes

1. **控制台**：Playwright + axe 登录页壳层冒烟；jscpd 与 size-limit 接入 web-check/CI。
2. **文档**：markdownlint-cli2 接入 `make infra-lint` 与 CI；排除 `.zcode/**`、
   `openspec/changes/archive/**`、`docs/acceptance/**` 等工具/历史目录；行宽与表格样式不做
   机器强制，避免中文文档无意义 churn。
3. **供应链**：govulncheck 移入独立 `security.yml`（无条件、加周排程）；新增 syft SBOM
   作业并上传 artifact；ci.yml 旧 vuln 作业删除。

## Non-goals

- 不改网关协议、存储或计量语义。
- 不为文档历史批次做回溯重排。

## Impact

- 门禁文件：Makefile、CI/security workflow、web 配置、markdownlint 配置。
- 无生产代码行为变化。
