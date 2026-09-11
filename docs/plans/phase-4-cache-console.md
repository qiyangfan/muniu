# Phase 4:语义缓存 + 管理 API + 控制台(v0.3.0)

> 状态:主线完成(2026-09-10;T4.8 Gemini 可选项未做,见 HANDOFF) · 前置:Phase 3 闸门全绿 · 版本:**v0.3.0** · 允许新增依赖:向量索引**至多一个**(T4.4 评估后定,零依赖暴力法达标则不引);前端 `web/` 独立 pnpm 工程(React 19 / Vite / Tailwind v4 / shadcn/ui / lucide-react / openapi-typescript / ECharts 或 Recharts 择一)

## 目标

网关「聪明且可视」:语义缓存省 token,管理 API 与嵌入二进制的 React 控制台让运营不用碰 sqlite。

## 任务清单

- [x] **T4.1 管理 API**:`/admin/v1/*`,鉴权 = `MUNIU_ADMIN_TOKEN` 环境变量(Bearer,单管理员;缺失则管理面整体关闭并 warn):tenants CRUD、keys CRUD(复用 Phase 2 auth 库函数,CLI 与 API 同源)、usage 聚合(按日/按 key/按模型/按 provider;时间范围参数)、providers 只读拓扑 + key 池实时状态、缓存统计。错误信封同格式。
- [x] **T4.2 契约与漂移门禁**:`contracts/openapi.yaml` 手写维护(单源);Go 测试对照 yaml 路径集合 ≡ mux 注册集合(轻量漂移门禁);`web/` 用 `openapi-typescript` 生成类型,**禁止手写 API 类型**(沿 kongming ADR-005 纪律)。
- [x] **T4.3 embeddings 入站通道**:`POST /v1/embeddings` 经 openai 基座透传(语义缓存的嵌入依赖它;也是对外能力);fixture 1 个;计量走同链路(embedding 计价:pricing 支持仅 input 价)。
- [x] **T4.4 向量索引选型评估(记录进 HANDOFF)**:生成 1 万条 1536 维随机向量,基准「暴力余弦 Top5」与候选 HNSW 库(`github.com/coder/hnsw` 或当时维护最好的纯 Go 实现)p99;暴力法 < 5ms 则**不引依赖**,否则引一个。评估代码留 `internal/semantic/bench_test.go`。
- [x] **T4.5 语义缓存(测试先行)**:`internal/semantic`:指纹归一化、准入白名单(semantic-cache 契约;tenant 开关默认关)、嵌入调用(经 T4.3 通道,租户自备 embedding 模型配置)、索引(T4.4 结论)、阈值 0.97、TTL 24h + LRU 容量、命中回放(非流式直接返回;流式命中按 chunk 拼包回放,`finish_reason=stop`);迁移 0003 `semantic_cache` 表(id/tenant_id/fingerprint/model_alias/embedding BLOB/response BLOB/hits/created_at/expires_at);命中行记 `usage_logs`(`provider='semantic-cache'`,cost 0)。测试:准入拒绝各条件、阈值边界、TTL 过期、命中计量。
- [x] **T4.6 控制台 web/**:`pnpm create vite` React+TS → Tailwind v4 → shadcn init(**基础交互组件一律 shadcn,禁手搓;色值经 `@theme` token;图标 lucide;图表色板从 token 派生**——沿 kongming 前端铁律);页面:总览(用量/成本/命中率图表)、密钥管理、用量明细(筛选+分页)、供应商健康(key 池三态);登录 = 输入 admin token 存 sessionStorage;`pnpm build` 产物经 `//go:embed web/dist` 嵌入二进制,`/console/*` 路由服务静态资源(**单二进制自带控制台**)。`web/` 有自己的 `pnpm typecheck && pnpm lint` 并纳入 `make check`(存在 pnpm 时执行,否则跳过并提示)。
- [x] **T4.7 e2e 冒烟**:管理 API curl 全路径实录;控制台四页截图(Playwright 有则脚本化,无则手动)进 HANDOFF。
- [ ] **T4.8 Gemini 桥接(可选,主线闸门通过后)**:`bridge/gemini.go` + fixture ≥2;不阻塞发版。
- [x] **T4.9 文档与发版**:README 增控制台截图与语义缓存说明(含「缓存的是相似问题的一次参考答案」声明);HANDOFF;`v0.3.0` 待用户指令。

## 设计指针

`semantic-cache.md` 全文 · `data-and-config.md`「迁移机制」 · `conventions.md` · kongming 前端铁律(AGENTS.md「技术栈锁定」6 条,本仓库同样遵守)。

## 验收闸门

1. 漂移门禁测试绿;2. 索引评估数据与结论;3. 缓存准入/阈值/TTL 测试绿 + 一次真实命中实录(同义两问第二问 `provider=semantic-cache`);4. 控制台四页截图;5. `make check`(含 web typecheck/lint)全绿。

## Non-goals

多管理员/SSO · 缓存预热/跨租户共享 · 告警 · 控制台国际化。
