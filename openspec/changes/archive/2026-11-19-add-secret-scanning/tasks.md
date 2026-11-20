# Tasks

## 1. 实现

- [x] 1.1 新增 `.gitleaks.toml`:默认规则集 + 路径级 allowlist(muniu 无特殊排除)。唯一增量豁免 = `docs/acceptance/2026-10-13-review-evidence/scope.json`(路径→sha256 摘要清单,generic-api-key 误报),注释写明原因/负责人/到期日。验证:历史扫描 61 commits 无命中(豁免前 10 处命中全部由此清单产生)
- [x] 1.2 新增 `.pre-commit-config.yaml`:gitleaks hook 钉 `v8.30.1`。验证:YAML 解析通过;本机未装 pre-commit,hook 实跑待本地安装后复核(不影响 CI 门禁)
- [x] 1.3 新增 `.github/workflows/security.yml` 的 `secrets` job:独立 workflow(不塞按路径裁剪的 `ci.yml`),`actions/checkout@v7` + `fetch-depth: 0`(全历史扫描),gitleaks 8.30.1 + `--exit-code 1`,`timeout-minutes: 10`。验证:YAML 解析通过;因 /usr/local/bin 属 root,解包步骤用 `sudo tar`(handoff 原命令在 GitHub runner 上会 permission denied)

## 2. CI 落点

- [x] 2.1 触发覆盖 `push: branches: [main]` 与 `pull_request`;Action 版本按 handoff §5.0 基准(actions/checkout@v7),不 pin `latest`
- [ ] 2.2 分支保护 required:将 `security` workflow 的 `secrets` job 设为 main 的 required check【阻塞:仓库级设置需 owner 授权;main 无分支保护、CI billing lock 未解,原因未消除】

## 3. 负向验收

- [x] 3.1 干净树跑等价 CI 命令 `gitleaks detect --source . --redact --exit-code 1`,退出 0(本仓 61 commits 与临时 clone 各一次;存活证据 /tmp/gitleaks-clean.log、/tmp/gl-probe-clean.log)
- [x] 3.2 在临时 clone 提交探针,同一命令非 0(exit 1,`RuleID: aws-access-token`)。handoff 原探针 `AKIAIOSFODNN7EXAMPLE` 被默认规则 allowlist `.+EXAMPLE$` 拦截且字符集须匹配 `[A-Z2-7]`,改用非示例值(前缀 `AKIA` + 16 位 `[A-Z2-7]`,实测值不落库)后命中;GitHub PAT 形态探针另测亦命中
- [x] 3.3 临时 clone reset 回探针前 HEAD 后,同一命令恢复退出 0(/tmp/gl-final-revert.log)
- [x] 3.4 既有门禁 `make check` 全绿(vet + lint + race test + 覆盖率 + web 五件套):EXIT=0,coverage 72.5%;首次全量中 `TestLoop_逐key余额回写回调` 负载超时,单测复跑与二次全量均绿

## Workflow follow-up

- [ ] 分支保护 required 与 PR 创建/推送等 git 与仓库设置写操作【阻塞:git/仓库写操作需 owner 逐次授权,原因未消除】
- [x] 本 change 待 PR 合并后 `openspec archive --skip-specs` 归档【已完成(2026-11-20 复核):change 目录已在 archive/,归档为事实;PR/CI 受 billing lock 阻塞不影响本地归档】
