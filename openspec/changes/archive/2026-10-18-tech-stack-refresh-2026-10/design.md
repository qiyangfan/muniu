# Design: tech-stack-refresh-2026-10

> 调研基准日 2026-10-18;版本数据经 `go list -m -versions` / `pnpm view` / `pnpm list` 实测与官方 release notes 交叉核对。本文件是「逐依赖怎么升、为什么、炸了怎么退」的决策记录。

## 0. 基线澄清:package.json 的 range ≠ 实装版本

web 侧调研以 lockfile 实装为准(`pnpm list --depth 0`):react 19.2.8、tailwindcss 4.3.3(已非 range 所写的 4.0)、eslint 9.39.5、vite 6.4.3、typescript 5.7.3。因此不少包只是「minor 补齐」,真正的大版本迁移集中在 vite/plugin-react/recharts/lucide/eslint/TS/pnpm 七件。

## 1. 「手搓替代」审计结论(本 change 的机会面)

用户核心诉求:上游新版本有没有功能增量可以替代我们手搓的行为。逐项核查结论:

| 手搓/三方域 | 上游增量 | 结论 |
|---|---|---|
| `google/uuid` 三方依赖 | **Go 1.27 新增 stdlib `uuid` 包**,项目所用 API(New/NewV4/NewV7/Parse)全覆盖 | ✅ **退役**,11 文件 14 处调用点机械替换;注意 stdlib 无生成版 `Must` 包装(仅 MustParse),生成点按原错误语义改写 |
| web 手写 memo 化(8×useCallback/0×useMemo/0×memo) | **React Compiler 1.0 GA(2025-10)**,plugin-react ≥6.1 `compiler: true` 一行启用 | ✅ **启用**,存量代码极少、价值在增长兜底;附带修复「eslint 未装 react-hooks 插件」的 lint 缺口 |
| store 层查询性能 | **sqlite v1.59 原生 memcpy/memset**(替换转译 musl 版)+ UDF 上下文池化;v1.60.1 修参数绑定二次方复杂度 | ✅ **随版本升级白拿**,零代码动作 |
| `encoding/json` 热路径 | **Go 1.27 json/v2 GA,v1 API 已自动由 v2 实现背书**(go.mod 已 1.27.0,本仓 HEAD 已在享受) | ✅ 零动作;注意错误文案可能差异已被既有绿测覆盖,GOEXPERIMENT=nojsonv2 为逃生门 |
| SSE/事件流解析 | stdlib 三版本(1.25/1.26/1.27)release notes 全查,**无 SSE 解析器** | ❌ 手搓保留 |
| 退避/重试/限流原语 | stdlib **无 backoff/retry 包**;限流仍在 x/time/rate | ❌ 手搓保留 |
| YAML 解析 | encoding/yaml 提案被官方拒绝(golang/go#61023 closed),stdlib 永不会有 | ❌ goccy/go-yaml 保留(低频维护记观察项) |
| token 计量 | tiktoken-go v0.1.8 即最新,已支持 o200k_base | ❌ 无动作 |
| 时间类测试的假时钟 | Go 1.25 `testing/synctest` GA + miniredis v2.40 synctest 支持 | ⚠️ **存量不重写**(纯 churn),新写时间类测试按需采用 |
| 流式 JSON 增量解析(jsontext) | Go 1.27 `encoding/json/jsontext` 可用 | ⚠️ protocol 字节保真敏感区,**本轮不动**,记观察项 |
| goroutine 泄漏排查 | Go 1.27 `/debug/pprof/goroutineleak` 端点 | ⚠️ 本仓**无任何 pprof 暴露面**(grep 零命中),前置缺失,与本 change 无关 |

## 2. Go 侧逐项决策

### 2.1 go-redis v9.22.0 → v9.23.0
- v10 **不存在**(`go list -m -versions` 空,官方无迁移指南),升 v9 系内最新即可,预期零代码改动。
- 唯一注意:9.23 起**每个 Client 默认带专属 pipeline 连接池**(默认 size 10,空池不拨号)。muniu 仅单机 redis 做 ratelimit 语义,行为透明,但连接数形态变化 → 冒烟时看一眼连接数。
- 回退:`go get github.com/redis/go-redis/v9@v9.22.0`,零迁移代码意味着零回退成本。

### 2.2 modernc.org/sqlite v1.58.0 → v1.60.1
- 区间增量:v1.59 原生 memcpy(Linux 查询密集 CPU 最多 -1/3)+ UDF 回调池化;v1.60 WAL `-shm` 页故障不再崩进程、`StrictPragmas` DSN 注入防护(opt-in,不启);v1.60.1 修参数绑定二次方复杂度(批量 INSERT 计量写入直接受益)。
- 要求 go ≥1.26(本仓 1.27 ✓);`modernc.org/libc` 随版自动抬升(`go get` 处理,indirect 不单独钉)。
- 回退:纯版本回退,迁移体系(0013 止)与 SQLite 文件格式(上游 3.53.4 未变)无涉。

### 2.3 miniredis v2.39.0 → v2.40.0
纯测试域,无 breaking,顺带获得 synctest 支持(为未来时间类测试铺路)。

### 2.4 coder/hnsw:维持 v0.6.1(裁决已定)
- 事实:最后 release v0.6.1 = 2024-06-14,距今 2 年 4 个月;repo 未 archived 但实质休眠(2025-2026 每年零星 bugfix);**全仓 7 个 tag,v0.6.1 即最新正式版本**。v0.6.1 之后 main 分支上有多个未发版的正确性修复(#22 搜索终止/efSearch 约束、堆 `Max()/PopLast()` 返回错元素、`replenish()` 硬编码余弦距离、删除后空层清理),全部命中语义缓存热路径。
- 替代品审计:chromem-go(仅 FLAT 暴力检索,自述 beta)、vecgo(experimental 无 release)——「纯 Go + 活跃 + ANN」三者兼得者不存在,换库出局。
- **终局裁决(2026-10-18,owner 插话 + 实测证据双重否决原推荐)**:维持 v0.6.1。①owner 明示不钉哈希伪版本;②实测钉 main HEAD(36cab60)后 HNSWTop5 基准从 381µs/op 回退至 2.69ms/op(7 倍,allocs 323→1092;选型评估 HNSW p50=2.51ms/p99=8.35ms vs 原判据基线),正确性修复使搜索更彻底但延迟代价在缓存热路径上不可接受;③语义缓存本为近似命中,v0.6.1 已知缺陷属命中质量项而非正确性灾难。
- 后续:HANDOFF 登记观察项(上游若发 v0.6.2+ 正式 tag 即重新评估,bench 判据沿用本记录)。

### 2.5 google/uuid 退役 → stdlib `uuid`(Go 1.27)
- stdlib API 面:`New()/NewV4()/NewV7()/Nil()/Max()/Parse()/MustParse()` + `String()/MarshalText()/Compare()`;仅支持 v4/v7——本仓恰好只用 v7 与 v4 语义。
- 现有调用形态与替换:11× `uuid.Must(uuid.NewV7()).String()`(stdlib 无生成版 Must → 各点改为显式错误处理或本地小 helper,保持「失败即 panic」原语义由各调用点现形决定:middleware 已有 err 回退 `uuid.New()` 的先例,id 生成点可同法);1× `uuid.New()` 直接平替;测试内用法同批替换。
- ID 格式不变(小写连字符 v7 字符串),对存储/锦官 ingest/契约零影响——这是 skip_specs 的依据之一。
- 收益:依赖白名单净减一(stdlib 优先宪法),顺带消掉 google/uuid 3 年未动的「僵尸依赖」观感。

### 2.6 golangci-lint 2.14 → 2.15.0
- 2026-10-16 发布,纯依赖升级无新 linter;但 errcheck 1.20→1.30、exhaustive→0.15 等跨度大,**可能冒新告警**——升级后跑 `make check` 清零,告警若是误报按现行 disable 注释纪律登记。
- mise.toml(`aqua:golangci/golangci-lint`)与 ci.yml `version: "v2.14"` 两处同改,版本钉死纪律不变。本地自建二进制若需重编,沿用 golangci-lint-toolchain-fix 记忆中的 mise aqua 布局重建法。

## 3. web 侧逐项决策

### 3.1 顺序策略:渐进批 → 大件批,每步全门禁
纯 update 批先行(react 19.3.1/@types/radix-ui 1.8.1/tailwind-merge 3.7.0/vitest 5.0.5/jsdom 30.1.3/prettier 3.9.10/stylelint 17.16.0/typescript-eslint 8.71.2),随后 TS 6 → recharts 3 → lucide 1.x → Vite 8 + plugin-react 6 → React Compiler → ESLint 10 → pnpm 12。每步 `pnpm typecheck && pnpm lint && pnpm lint:css && pnpm test && pnpm format:check` + `vite build --outDir /tmp/dist-check` 全绿才进下一步,炸了能二分定位。

### 3.2 TypeScript ~5.7.2 → ~6.0.x(不上 7)
- 事实:TS 6.0(2026-03-23 GA)= JS 编译器线终点桥接版;TS 7.0(tsgo,2026-07-08 GA,8-12x 提速)= npm latest 7.0.2。**typescript-eslint 官方支持矩阵 `<6.1.0`**,对 TS 7 无 JS API(tsgo 不提供稳定 JS API),支持跟踪 issue #12518,完整支持预计 7.1+。
- 裁决:上 6.0(在支持矩阵内且是「最新稳定可用」),**7 明确观望**。6.0 三大 breaking 本仓免疫:strict 默认(已显式)、ES5 target 移除(target ES2022)、@types 不自动加载(全部显式依赖,动手时核查 vitest.config 与全局类型面)。
- 回退:`~5.9.3`(5.x 线末)。

### 3.3 recharts 2.15.4 → 3.10.1
3.0 重写状态管理,breaking 面不小(activeIndex 移除/CategoricalChartState 移除/accessibilityLayer 默认开等);但本仓**仅 Overview.tsx 一个文件**消费 Bars/BarChart/CartesianGrid/ResponsiveContainer/Tooltip/XAxis/YAxis 七件套,grep 无高危 API 命中、轴用默认 id。迁移 = 版本 + 该页按宪法三档×亮暗视觉回归。

### 3.4 lucide-react 0.469.0 → 1.59.0
1.0 breaking:移除全部品牌图标(本仓 grep 零命中 ✓)、图标默认 `aria-hidden=true`。走查义务:「图标即语义」且无伴随文本的位置需显式处理;纯装饰位(本仓大多数)不受影响。导入路径是否需改以官方 v1 指南为准(社区有新包名说法,官方指南未确认,动手时实证)。

### 3.5 Vite 6.4.3 → 8.3.4 + @vitejs/plugin-react 4.7.0 → 6.1.2
- Vite 8(2026-03-12)起 Rolldown+Oxc 为默认引擎,8.1/8.2/8.3 已过生态消化期。本仓 vite.config.ts 只有 base/两插件/server.proxy,**未触碰任何 8.x breaking 配置面**(manualChunks 对象形式/esbuild 配置项/CSS 压缩默认等全部未用)。
- 必验两点:①`@tailwindcss/vite@4.3.3` peer 兼容(装包时看 peer 警告);②产物回归——构建经 go:embed 进二进制、由 `/console/*` 提供,`base: "/console/"` 语义必须不变:`vite build --outDir /tmp/dist-check` + 临时实例冒烟 console 页。
- plugin-react 4→6:弃 Babel 改 oxc,旧式 `babel.plugins` 写法失效(本仓未用 ✓);Node 24 满足 Vite 8 的 20.19+ 要求。
- 回退:lockfile 回退 vite/plugin-react 两包即可。

### 3.6 React Compiler 启用(plugin-react `compiler: true` + eslint-plugin-react-hooks@7)
- 前提:plugin-react ≥6.1 `react({ compiler: true })`(需可选 peer `oxc-transform-react`);ESLint 侧 hooks 规则本仓**压根没装**,`eslint-plugin-react-hooks@7`(flat config,compiler 规则已并入,独立 react-compiler 插件已废弃)随批引入。
- 顺序:先装 hooks 插件跑 lint 基线(含 `react-hooks/react-compiler` 规则)清零违规,再开 `compiler: true` 全量启用(编译器对不合规组件自动跳过,渐进安全)。
- 期望管理:现仓手写 memo 仅 8 处 useCallback,编译器**不是删存量代码的工具**,是后续组件增长的自动 memo 兜底 + 修复 lint 缺口;不承诺可观测性能数字。
- 回退:`compiler: true` 一行摘除;hooks 插件保留(独立价值)。

### 3.7 ESLint 9.39.5 → 10.13.0
最大 breaking(eslintrc 全移除)对本仓免疫(eslint.config.js 已 flat);过一遍官方 codemod,typescript-eslint 8.71.2 官方支持 ^10。

### 3.8 pnpm 11.24.0 → 12.14.0
12.0(2026-08-26)安装引擎 Rust 重写,lockfile 兼容;breaking 面集中在 CI 参数(本仓 Makefile/CI 全部显式 `--frozen-lockfile` ✓)。`packageManager` 字段单点改动,pnpm/action-setup@v6(2026-09)晚于 pnpm 12 发布,读字段安装无虞。node 24 保持(24 处 maintenance 仍是支持线;26 于 2026-10-28 转 LTS 后下轮再议)。

## 4. CI 一致性修正

- plugins.yml `actions/checkout@v5` → `@v7`(与 ci/release.yml 对齐,落后两个大版本)。
- release.yml `node-version: 22` → `24`(与 ci.yml 收敛;node 22 LTS 2027-04 EOL,不新增风险)。
- 其余 action 主版本(checkout/setup-go/setup-node/pnpm-action/golangci-action)实测均已最新,零动作。

## 5. 明确不做清单(含理由,防反复)

| 项 | 理由 |
|---|---|
| Go 1.28 | 不存在(1.27 线 2026-08 起,最新 patch 1.27.2 已在位;本地/CI 均无需动作) |
| TS 7 | typescript-eslint 阻断(<6.1.0),关注 7.1+ 官宣后再评估 |
| json/v2 显式迁移 | v1 已自动由 v2 背书;jsontext 流式改造涉 protocol 字节保真敏感区,收益不抵契约风险 |
| synctest 存量重写 | 纯 churn,不改行为;新测试按需采用(惯例层面) |
| hnsw 换库 | 无合格候选(见 2.4) |
| pprof/goroutineleak 端点 | 无 pprof 暴露面前置,属独立功能立项不属技术栈刷新 |
| tw-animate-css 移除 | `animate-in/out` 官方 core 至 4.3 均不提供,标准件动画仍依赖 |
| node 26 | 2026-10-28 才转 LTS,不抢跑 |

## 6. 验证与回退总策略

- **门禁节奏**:每任务一收尾(实现+验证+勾选),Go 侧 `make check`,web 侧四件套+build;批间不跨绿。
- **顶层验收**:全量 `make check` 绿(coverage ≥65% 门禁只升不降)+ 单二进制 `CGO_ENABLED=0` 构建 + 临时实例冒烟(数据面一单 + console 页)+ 图表页/图标页 375/768/1280×亮暗走查 + go-redis 连接数观察。
- **回退**:全部升级均为版本级回退(单行 go get / lockfile 回退 / 一行配置摘除),无数据格式与契约迁移;唯一非对称项是 hnsw 伪版本(owner 裁决项)。
- **诚实汇报纪律**:升级若踩出上游 bug 或新告警,原始输出贴验收记录,不粉饰、不静默降级版本。
