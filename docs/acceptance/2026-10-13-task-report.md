# 冻结任务终态报告（2026-10-13）

冻结输入见[任务快照](2026-10-13-task-snapshot.md)。保留启动时上一批未提交改动；没有 Git add/commit/push/tag，没有部署或重启既有实例。

## 逐项终态

| 项目 | 终态 | 改动与证明 |
|---|---|---|
| images 1.1 | 已完成并验证 | DTO 保真、缺省张数、档位、精确预算与溢出测试 |
| images 1.2 | 已完成并验证 | 换道/断流/取消/失败零成本/上游 usage 不计价回归 |
| images 1.3 | 已完成并验证 | 迁移 0011、严格档位 JSON、YAML 静态报价、本位币换算 |
| images 1.4 | 已完成并验证 | ImagePreparer、OpenAI 基座、独立 lane、数据面链、插件、64MiB 上限 |
| images 1.5 | 已完成并验证 | 管理路由、完整候选目录先校验再持久化发布、并发串行与损坏保持旧态 |
| images 1.6 | 已完成并验证 | OpenAPI/生成类型、标准件 UI；移动切币种/保存/确认撤销实测 |
| images 1.7 | 已完成并验证 | 计量/管道/schema/配置与真实厂商 wire 验证边界文档 |
| images 1.8 | 已完成并验证 | make check/Web/独立初审、集中返工；已归档同步主规格 |
| admins 2.1 | 已完成并验证 | x/crypto v0.58.0 白名单与 tidy，已有 x/sys 升至要求的 v0.49.0 |
| admins 2.2 | 已完成并验证 | PHC/参数迁移、锁定/重启、会话注销/过期/即时失效测试 |
| admins 2.3 | 已完成并验证 | 迁移 0012 三表；账号变更与会话删除同事务，签发再次核验密码版本 |
| admins 2.4 | 已完成并验证 | 会话/Bearer actor、原管理测试不修改、纯账号挂载、401 核心信封兼容 |
| admins 2.5 | 已完成并验证 | 成败/拒绝审计、脱敏、单 JSON、大整数精度、稳定游标 |
| admins 2.6 | 已完成并验证 | auth/admins/audit 路由、漂移门禁、request_id/Recover 装配 |
| admins 2.7 | 已完成并验证 | CLI 五动作；真实 PTY 双次确认/禁回显/恢复回显实证 |
| admins 2.8 | 已完成并验证 | 双轨登录/whoami/身份/退出/审计，11 项组件测试；文案为“用户名/密码” |
| admins 2.9 | 已完成并验证 | 三表/身份/启动清理/内存锁定/HTTP cookie/CSRF 与示例配置文档 |
| admins 2.10 | 已完成并验证 | make check/Web/独立初审、集中返工；归档同步 admin-operators 主规格 |
| 历史 recover-pending-usage-settlement 2.3 | 已完成并验证，补漏勾 | 两仓已有 2026-10-02 相同 :8788 联调、归档与主规格证据，不重跑跨仓链路 |
| HANDOFF 拓扑读侧漂移 | 已完成并验证 | 复用 Topology()；SIGHUP 测试验证 providers/keypool gauge 随 p1→p2，Counter 保留历史 |

共 20 项完成。两个新 change 归档为 2026-10-13-image-generation-endpoint（8/8）与 2026-10-13-admin-operators-audit（10/10）；主规格新增 9 个 Requirement、修改 1 个 Requirement。

## 待确认

| 遗留事项 | 原因与下一步 |
|---|---|
| key 级模型白名单 | 只有标题，无存储/API、空名单、四通道拒绝与生命周期契约；先补行为契约。 |
| form-row help API | 依赖 kongming 的 label/tooltip 链、当前无消费需求；语义须源仓先裁决。 |
| Responses reasoning 事件 | 原开放项按 v1 最小事件集保守实现；须先确定外部事件契约。 |
| T5.2 完整跨仓基线 | HANDOFF 记接线完成，但旧 phase plan 未勾；完整 kongming 回归/评估证据不在本仓。 |
| 本批 Git 写操作 | AGENTS.md 要求逐次明确指令；此前 workflow 阻塞已解除，本地/远端 main 均为 93ec4fb，本批未提交。 |
| images 厂商真实验收 | 缺本轮真实凭据；不把 chat 预设当 images 证明，provider-presets 保持待验证。 |

## 搁置的历史阻塞项

- T5.3：原容器/计量卷丢失、灰度对账证据中断；恢复观察或调整判据待 owner，本轮不重启、不重起算。
- T5.6：两周连续日志与观察期证据不足，时间依赖未消除；tag 仍需明确指令。
- Gemini 真实多工具 wire：仅检查凭据存在性，GEMINI_API_KEY 未配置，不输出密钥、不打真上游。
- MiniMax 域名归一：快照仍登记真实凭据/实录缺失，不重试真上游。原 Gemini 轻形态搁置已被 ADR-0015 取代，不再实施。

## 验证结果

~~~text
PATH=/Users/solvela/.local/share/mise/installs/aqua-golangci-golangci-lint/2.14.0/golangci-lint-2.14.0-darwin-arm64:$PATH make check
0 issues.
Test Files  3 passed (3)
Tests  11 passed (11)
All matched files use Prettier code style!
coverage gate PASS: 69.5%
EXIT 0
make build → EXIT 0
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/muniu-task-browser/muniu-linux-arm64 ./cmd/muniu → EXIT 0
pnpm exec vite build --outDir /tmp/muniu-task-dist
✓ built in 2.07s
EXIT 0
~~~

原文：[make check](2026-10-13-evidence/make-check.txt)、[构建](2026-10-13-evidence/build.txt)、[Web 构建](2026-10-13-evidence/vite-build.txt)、[images 归档](2026-10-13-evidence/images-archive.txt)、[admins 归档](2026-10-13-evidence/admin-archive.txt)。初始 PATH 命中 lint 2.13，无法读取 Go 1.27 导出数据；使用仓库 mise/CI 钉定的 2.14 后通过，没有跳过 lint。

聚焦验证复用有效结果。追加全量门禁仅在遗留项/审计整数精度修复导致原证据失效时进行。SIGHUP 测试初版误要求历史 Counter 删除 p1，后收窄到实时 gauge，历史计数没有被删除。原始失败也保存于 evidence 目录。

### 严格文档校验的既有阻塞

~~~text
openspec validate --all --strict
✗ spec/platform-data-plane
Totals: 22 passed, 1 failed (23 items)
~~~

旧 platform-data-plane 的 Purpose 仍为“TBD - created by archiving change sighup-config-reload”。它不是冻结实施清单中的修订项，本轮记录为整体严格文档校验的既有阻塞，未扩展修改。两份本轮 change 与受影响主规格通过，已正常归档。完整[原始输出](2026-10-13-evidence/openspec-strict.txt)。Web 构建保留告警：Some chunks are larger than 500 kB after minification.（主 JS 770.07 kB），未扩展性能优化。

### 浏览器与交互

独立 SQLite、:8797 网关和 :5173 Vite；保留既有 :8787 实例。登录/图片价目/审计 × 375/768/1280 × 亮暗共 18 组，scrollWidth == innerWidth。移动卡片/底栏与桌面表格分别走查；实际账密登录、whoami 恢复、退出，密码文案已统一。375px 实测编辑：USD→CNY→保存 0.4（db）→AlertDialog 确认撤销→yaml 回落，零 ErrorAlert。审计显示登录及图片覆盖/删除，密码只显示“已脱敏”。真实 PTY 输出：TTY 双次确认、关闭回显、结束恢复回显：PASS。业务裸交互元素检查零命中（排除 ui 标准件与测试）。

截图：[登录](2026-10-13-evidence/login-375-dark.png)、[移动价目](2026-10-13-evidence/pricing-375-dark.png)、[768 审计](2026-10-13-evidence/audit-768-light.png)。临时服务、配置和数据库在收尾清理，不注册常驻任务。

## 技术结论与审查边界

生成通道采用独立 lane 与平行价目表，复用既有鉴权/候选/出站/计量汇/本位币机制。向 chat/token 结构塞字段会增加无效分支与迁移耦合；当前结构仅增一表和小目录，热路径一次快照读取与整数乘法，保留 chat 契约、追加迁移、假上游可完整验证。预算与交付使用同一价目快照，避免改价时口径错位。

管理员保留 Bearer 并新增本地账号/服务端会话。共享 token 无个人归因，无状态 cookie 无法即时踢会话；服务端表以逐请求单次读取换取事务失效与归因。低频密码哈希、只增审计，不增后台清扫。拓扑读侧复用现有 Topology()，不新造 watcher 或状态副本。

按 ai-code-review 方法进行了两个独立 sober 初审，共 5 项确认缺陷，集中返工和回归通过。追加独立修复复核两代理均报“Selected model is at capacity”，按一次执行纪律不重试，不声称追加独立复核通过；根代理已复核修复 diff 和调用链。详见两个归档 change 的 review.md。
