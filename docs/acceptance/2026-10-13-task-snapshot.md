# 本轮任务冻结快照

冻结时间：2026-10-13T05:57:10.539346+00:00

范围：启动时所有 OpenSpec 未勾项与 HANDOFF 遗留；后续文档不扩大范围。

- `openspec/changes/admin-operators-audit/tasks.md:3` [ ] 2.1 依赖白名单落地:`go get golang.org/x/crypto`(argon2)+ `make tidy` + ADR-0017 白名单行登记
- `openspec/changes/admin-operators-audit/tasks.md:4` [ ] 2.2 失败测试先行:argon2id PHC 往返(哈希/校验/参数迁移解析)+ 登录锁定状态机(阈值/锁定期/重启清零)+ 会话签发/校验/注销/过期惰性清理
- `openspec/changes/admin-operators-audit/tasks.md:5` [ ] 2.3 迁移 0012(三表)+ store 读写(账号/会话/审计)+ 停用改密的会话即时清理
- `openspec/changes/admin-operators-audit/tasks.md:6` [ ] 2.4 失败测试先行:双轨中间件——会话轨通过/令牌轨零回归(既有管理面测试零改动)/双 actor 注入/401 信封形态不变/挂载条件放宽(token 或活跃管理员)
- `openspec/changes/admin-operators-audit/tasks.md:7` [ ] 2.5 失败测试先行:审计中间件——变更动作归因/敏感字段脱敏/GET 不留痕/登录成败与锁定拒绝入审计/游标分页稳定
- `openspec/changes/admin-operators-audit/tasks.md:8` [ ] 2.6 管理面路由:auth 三件套(login/logout/whoami)+ admins CRUD + audit 查询;路由表与漂移门禁同步
- `openspec/changes/admin-operators-audit/tasks.md:9` [ ] 2.7 CLI:`muniu admins` 子命令族(add tty 双次口令/stdin、list、disable、enable、passwd)
- `openspec/changes/admin-operators-audit/tasks.md:10` [ ] 2.8 控制台:登录页双轨 + whoami 探测路由 + 顶栏身份/退出 + 审计页(openapi 同步 + generate:api;标准件,移动范式)
- `openspec/changes/admin-operators-audit/tasks.md:11` [ ] 2.9 文档:data-and-config 三表 schema + 管理面身份节(ADR-0017 为纲)+ muniu.example.yaml 注记
- `openspec/changes/admin-operators-audit/tasks.md:12` [ ] 2.10 `make check` 全绿 + web 四门禁 + 独立 review;archive + 主 specs 同步
- `openspec/changes/archive/2026-10-02-recover-pending-usage-settlement/tasks.md:10` [ ] 2.3 两仓 HANDOFF 写入同一验收链路及结果；完成实现和验证后再同步主规格与归档。（≤ 2h）
- `openspec/changes/image-generation-endpoint/tasks.md:3` [ ] 1.1 失败测试先行:protocol images DTO 解析(model/prompt 必填、Raw 保留)+ 档位键归一(`size|quality` 拼接、缺省不猜测)+ 预算精确预检判定(n×价 超界拒/价缺失旁路/零预算零回归)
- `openspec/changes/image-generation-endpoint/tasks.md:4` [ ] 1.2 失败测试先行:候选循环语义——正常交付计量(cost=n×档位价,tokens 全 0,estimated=0)/ 档位未命中走兜底 / 无价目记零 / 失败候选 cost=0 观测行 / 5xx 换道只扣交付成本 / 断流不交付半包 / `images_not_supported` 400 不换道 / 别名不存在 400
- `openspec/changes/image-generation-endpoint/tasks.md:5` [ ] 1.3 迁移 0011(`image_pricing` 表)+ store 读写 + YAML `image_pricing` 配置段与静态兜底目录
- `openspec/changes/image-generation-endpoint/tasks.md:6` [ ] 1.4 `adapter.ImagePreparer` 能力接口 + OpenAI 兼容基座实现;`internal/proxy/images.go` lane handler(嵌入 dataPlaneMW 挂载、插件钩子、64MiB 响应上限、预算预检挂候选循环前)
- `openspec/changes/image-generation-endpoint/tasks.md:7` [ ] 1.5 管理面:`GET/PUT/DELETE /admin/v1/image-pricing`(校验整单拒绝、原子热替换、来源标注)+ 路由表与漂移门禁同步
- `openspec/changes/image-generation-endpoint/tasks.md:8` [ ] 1.6 openapi.yaml 同步(入站端点 + 管理端点)+ `pnpm generate:api`;控制台价目页 images 价目区(列表/编辑/删除,标准件)
- `openspec/changes/image-generation-endpoint/tasks.md:9` [ ] 1.7 文档:metering.md「per-image 计价」节 + proxy-and-streaming 通道注记 + data-and-config schema 增补 + provider-presets 待验证标注
- `openspec/changes/image-generation-endpoint/tasks.md:10` [ ] 1.8 `make check` 全绿 + web 四门禁 + 独立 review;archive + 主 specs 同步

## HANDOFF 遗留项

- key 级模型白名单：待确认，缺少范围、存储/API 与四通道行为契约。
- Git 提交、push、tag：待确认，未获逐次明确写操作指令。
- T5.3/T5.6：历史阻塞搁置，容器/计量卷丢失，观察证据中断，恢复或改判据待 owner。
- Gemini 真实 wire、MiniMax 域名：历史外部条件搁置，缺真实凭据。
- images 厂商实证：待确认，缺本轮 images 真实凭据；本轮只做实现与假上游验收。
- T5.2 历史计划未勾：HANDOFF 已记录接线完成，跨仓基线证据不在本仓，不扩为跨仓实现。

本快照保持冻结；结果在收尾记录回写。
