# 2026-10-22 冻结任务执行报告

## 执行范围与终态

按[冻结快照](2026-10-22-task-snapshot.md)，工作集合为 OpenSpec change `dashscope-multimodal-embeddings` 的 1.1–1.9，以及 HANDOFF 关联的 Phase 0 T0.9、Phase 5 T5.2 和 T5.6。OpenSpec change 已归档至 `openspec/changes/archive/2026-10-22-dashscope-multimodal-embeddings/`，同步更新主规格。

| 条目 | 终态 | 关键结果 |
|---|---|---|
| 1.1 | 完成并验证 | 原生请求 DTO、路由模型解析、必填字段校验与原始 JSON 保留；Qwen 独立/融合和 Tongyi 多图 fixture。 |
| 1.2 | 完成并验证 | 新增 provider 原生 URL 配置及校验，贯通 YAML、SIGHUP 和控制面 topology 快照原子替换。 |
| 1.3 | 完成并验证 | 新增可选多模态出站能力；Qwen/DashScope 适配器使用显式原生 URL、Bearer key 和原生路径，只改写顶层 model。 |
| 1.4 | 完成并验证 | 新 handler 接入数据面中间件、白名单与插件；支持候选筛选、failover、原生响应/错误透传。Fake upstream 覆盖不支持候选、请求改写、换道和耗尽错误。 |
| 1.5 | 完成并验证 | Qwen 与 Tongyi usage 归一化；缺失、非法、不自洽及不完整 usage 保留已知 token 并标记估算，响应 body 原样透传。 |
| 1.6 | 完成并验证 | 迁移 0014 增加视觉用量与视觉价目字段；贯通批量写入、缓冲续传、计量汇及管理查询，旧记录默认 0。 |
| 1.7 | 完成并验证 | 视觉 token 与文本/缓存/输出分段整数计价；未配置视觉价回落 input；贯通 YAML、DB、管理 API、OpenAPI 和价目 UI，并覆盖纯文本兼容。 |
| 1.8 | 完成并验证 | 更新示例配置、配置/计量/适配器契约和 conformance 文档；记录 16 MiB 入站上限、模型快照范围、Qwen 已知参数边界及由百炼负责校验的参数边界。 |
| 1.9 | 完成并验证 | OpenSpec change strict、全仓 `make check`、Go 构建、web 检查/构建及 fake-upstream wire/计量测试完成。全量 strict 有一项范围外旧规格警告，详见验证表。 |
| Phase 0 T0.9 | 完成并验证 | 冻结时复用已完成的用户远端交付：`origin/main` 指向 `6de1614`，与当时本地 HEAD 相同；远端 `v0.3.0` tag 存在；README badge 指向实际仓库 `qiyangfan/muniu`。 |
| Phase 5 T5.2 | 待确认 | 既有 HANDOFF 证明 Kongming 接线已完成；本轮未进入 Kongming 仓，缺其当前 `make check` 与评估基线证据。 |
| Phase 5 T5.6 | 待确认 | 2026-10-14 的 v2 判据已取代两周观察期；release checklist 核对与 tag 仍需相应证据及单独授权。本次代码交付 push 不代表发版。 |

## 架构决策

本次选择显式 `vision_tokens` 与 `vision_input` 字段，将图像/视频作为一个视觉计价段；没有建设任意 modality-rate map 或价目子表。

| 维度 | 当前视觉字段方案 | 可行的通用 modality 价目表方案 |
|---|---|---|
| 复杂度 | 在现有固定 token 价目和计量结构上增加一个明确字段，热路径直接做有限段整数计算。 | 需要模态注册/键值解析或子表查询，并让管理 API、OpenAPI、控制台支持动态行与未知类型治理。 |
| 性能 | 固定字段读取，无额外映射查询；对现有文本请求保持原公式。 | 小型 map 查询本身成本不大，但不会改善本次热路径；任意模态处理还需迭代各段。 |
| 兼容性 | 迁移为加列且默认 0，未配置视觉价回落 input；旧配置和纯文本请求保持兼容。 | 需要从现有 input/cached/output 价目双读或回填，明确未知模态和旧客户端序列化行为。 |
| 测试与迁移 | 只需验证视觉 token 子集、分段取整、缺省价回落、旧行默认值与原文本路径。 | 需要覆盖动态模态增删、重复键、缺价、迁移回填、API/UI round-trip 和每一种结算组合。 |
| 长期维护 | 新增第二种独立计价模态时要再次做字段/迁移；当前只有视觉段，规则直观。 | 后续增加模态较少改 schema，但需长期维护通用扩展、验证和 UI 语义。 |

结论：当前需求只有视觉 token 需要区别计价，图像与视频共享同一 usage 子集。显式字段是范围内最小且兼容的结构；通用模型的前置复杂度和迁移面没有当前收益支撑。若以后出现第二种独立计价模态，再以实际需求重新评估，不预建通用框架。

## 验证证据

| 命令 | 结果 |
|---|---|
| `openspec validate dashscope-multimodal-embeddings --strict` | 通过；归档前 change 校验有效。 |
| `openspec archive dashscope-multimodal-embeddings --yes` | 成功；新增 `native-multimodal-embeddings` 主规格并更新计量、状态同步和价目规格。 |
| `openspec validate --all --strict` | 23/24 通过。唯一失败是既存 `openspec/specs/protocol-native-passthrough/spec.md` 的 `requirements[3]` 超过 500 字警告。该规格不在冻结工作集合内，未为清理它扩展范围。 |
| `PATH=/tmp/gclbin:$PATH make check` | 通过：Go vet、golangci-lint 0 issues、web typecheck/lint/lint:css/format、Vitest 7 files/16 tests、Go 全包 race tests；coverage gate 70.7%。裸 PATH 首次使用旧 golangci 2.13.0 时复现 Go stdlib 导出格式不兼容，按 HANDOFF 用当前 Go 临时构建 v2.15.0 后全门禁通过。 |
| `make build` | 通过，静态 muniu 构建完成。 |
| `pnpm exec vite build --outDir /tmp/dist-check` | 通过；Vite 提示一个压缩后超过 500 kB 的 chunk，不影响构建。 |
| `rg -n -P --glob '!**/components/ui/**' '<(button|input|select|textarea)(\s|>)' web/src` | 无命中，业务层未发现裸交互元素。 |
| `git diff --check` | 报告与 HANDOFF 更新后执行，检查通过。 |

DashScope 真上游 smoke 未运行；该检查保留在手动档，不作为 CI 验收。

## 代码审查复验

- 修复并复验 4 项已证实缺陷：候选耗尽时保留 3xx 状态；原生请求不跟随上游重定向；压缩响应保留原始编码与响应头；只把 adapter 声明支持的两款模型纳入候选。
- OpenAPI 原先只声明 200，与 handler 已支持的 2xx 及原生 3xx 透传不一致；已改为声明 `2XX`/`3XX` 并重新生成前端类型。
- 新增回归测试先复现 300 被改写为 502、重定向变成目标响应、gzip 自动解压和不支持模型仍被调用；修复后聚焦测试及全量 `make check` 通过。
- 重复顶层 `model` 键的供应商解析差异尚未验证；留在 HANDOFF 开放决策项，不据此改变入站契约。

## 阻塞与搁置项

- 冻结时没有仍登记为阻塞且原因未消除的任务。
- 历史 T5.3/T5.6 两周观察基座中断已由 2026-10-14 owner 裁决关闭；本轮不重试。当前 T5.6 是 v2 发版判据待确认，不等同于重启旧观察期。
- 全量 strict 中 `protocol-native-passthrough` 长 requirement 是范围外既存校验失败；新 change strict 已通过，受影响主规格及新增主规格在全量结果中通过。
