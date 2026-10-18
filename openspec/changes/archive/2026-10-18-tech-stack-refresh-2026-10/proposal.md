# Proposal: tech-stack-refresh-2026-10

## Why

调研基准日 2026-10-18:Go 侧 3 个依赖落后且携带白捡的性能增量(sqlite v1.59 原生 memcpy 使查询密集负载 CPU 最多降 1/3、v1.60.1 修参数绑定二次方复杂度),Go 1.27 新增 stdlib `uuid` 包使 `google/uuid` 这个三方依赖可整体退役;web 侧整体落后 1-2 个大版本(Vite 6→8、ESLint 9→10、recharts 2→3、lucide 0.x→1.x、TS 5.7→6.x、pnpm 11→12),且 React Compiler 已 GA 一年可启用。工程质量与文档完整度优先是本仓定位,依赖与「最新稳定版」的差距本身是负资产;本轮同时完成一次「上游增量替代手搓/三方」的机会审计(SSE/退避/YAML 等手搓域经逐项核查 stdlib 确认无替代,结论沉淀进 design.md)。

## What Changes

分四批推进,逐依赖决策与风险证据见 design.md:

**Go 依赖批**
- go-redis v9.22.0 → v9.23.0(无 API breaking;每 Client 默认专属 pipeline 连接池属连接行为变化,冒烟观察)
- modernc.org/sqlite v1.58.0 → v1.60.1(查询密集 CPU 降耗 + 批量 INSERT 修复,store 层直接受益;libc 随版抬升)
- miniredis v2.39.0 → v2.40.0(测试域增量,含 synctest 支持)
- coder/hnsw v0.6.1 → 钉 master 伪版本(推荐,可回退):上游 2 年 4 个月未发版,v0.6.1 之后 master 上有多个**命中语义缓存热路径的 HNSW 正确性修复**(搜索终止条件/堆弹出错元素/replenish 硬编码余弦距离/删除后空层清理);「纯 Go + 活跃维护 + ANN 索引」兼得的替代品不存在,换库出局。若 owner 否决伪版本路线,回退 v0.6.1 并登记 HANDOFF 观察项
- **`google/uuid` 退役**:Go 1.27 stdlib `uuid` 包(New/NewV4/NewV7/Parse/MustParse)覆盖项目全部调用形态(11 文件 14 处:`uuid.NewV7()`/`uuid.New()`/`uuid.Must`),机械替换后依赖净减一,正合「stdlib 优先」宪法
- golangci-lint 2.14 → 2.15.0(mise.toml 与 ci.yml `version` 两处同改;errcheck 1.20→1.30 等大跨度 linter bump 可能出新告警,升完 make check 清零)

**web 前端批**
- 纯 update 批:react/react-dom 19.2.8→19.3.1(+@types 19.3.0)、radix-ui→1.8.1、tailwind-merge→3.7.0、vitest 5.0.5、jsdom 30.1.3、prettier 3.9.10、stylelint 17.16.0、typescript-eslint 8.71.2(tailwindcss 4.3.3 已最新零动作;tw-animate-css 经核查仍必需——`animate-in/out` 不在官方 core)
- TypeScript ~5.7.2 → ~6.0.x:typescript-eslint 官方支持上限 <6.1.0;本仓 `strict` 已显式、target ES2022、@types 全显式依赖,三大 breaking 天然免疫。**TS 7 本轮明确不上**(typescript-eslint 被 tsgo 阻断,等其 7.1+ 支持再评估)
- recharts 2.15.4 → 3.10.1(迁移面 = Overview.tsx 单文件 + 图表视觉回归)
- lucide-react 0.469.0 → 1.59.0(全仓零品牌图标命中;走查 `aria-hidden` 默认化的语义图标点)
- Vite 6.4.3 → 8.3.4 + @vitejs/plugin-react 4.7.0 → 6.1.2(Rolldown 默认化;本仓 vite.config.ts 未触碰任何 8.x breaking 配置面;@tailwindcss/vite peer 兼容与 `/console/` embed 产物回归必验)
- **React Compiler 启用**:plugin-react `compiler: true` + 新增 eslint-plugin-react-hooks@7(现行配置连 hooks 规则都未装,本身是 lint 缺口);现仓手写 memo 极少(8×useCallback/0×useMemo/0×memo),价值在后续组件增长的自动 memo 兜底——这是本轮「新版本增量替代手搓」在 web 侧的落点
- ESLint 9.39.5 → 10.13.0(flat config 已就位,免疫最大 breaking eslintrc 移除;过官方 codemod)
- pnpm 11.24.0 → 12.14.0(packageManager 字段;本仓 install 全部显式 `--frozen-lockfile`,免疫其主要 breaking)

**CI 一致性批**
- plugins.yml `actions/checkout@v5` → `@v7`(落后两个大版本);release.yml node 22 → 24(与 ci.yml 收敛;node 24 仍 LTS,26 于 2026-10-28 转 LTS 后下轮再议)

**明确不做(本轮,理由见 design.md)**
TS 7 / Go 1.28(不存在)/ json·v2 显式迁移(v1 API 已自动由 v2 实现背书,零动作吃性能;protocol 字节保真敏感区不做 jsontext 改造)/ SSE 解析·退避重试·YAML 的 stdlib 替代(确认不存在,手搓保留)/ synctest 存量测试重写 / hnsw 换库。

## Capabilities

### New Capabilities

(无——纯工程底座变更,零外部可观测行为变化,不发明能力域。)

### Modified Capabilities

(无——不改任何能力 Requirement;按 openspec 规则对本 change 声明 `skip_specs: true` 显式豁免。)

## Impact

- **go.mod/go.sum** + 11 个 uuid 调用点(internal/auth、internal/metering/id.go、internal/server/middleware.go、internal/store/semantic_cache.go、internal/store/admin.go、internal/admin/operators.go 及测试)+ mise.toml + .github/workflows/ci.yml
- **web/package.json + pnpm-lock.yaml** + vite.config.ts(compiler 选项)+ eslint.config.js(react-hooks flat config 接入)+ tsconfig 族(TS 6 核查)
- 验证面:`make check` 全量门禁 + web 四件套 + `vite build`(/tmp)+ 单二进制 CGO_ENABLED=0 构建 + go-redis 连接行为冒烟 + 图表页/图标页亮暗×三档走查
- 风险集中点:Vite 8 产物 embed(base `/console/` 语义不变性)、recharts 3 图表回归、hnsw 伪版本(钉 commit 哈希可复现,一键回退 v0.6.1)
