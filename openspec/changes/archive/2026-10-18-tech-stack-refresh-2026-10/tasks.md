# Tasks: tech-stack-refresh-2026-10

> 每任务一收尾(实现+验证+勾选),批间不跨绿;Go 侧门禁 = `make check`,web 侧门禁 = 四件套 + `pnpm exec vite build --outDir /tmp/dist-check`。回退路径均见 design.md 对应节。

## 1. Go 依赖批

- [x] 1.1 三连升 go-redis v9.23.0 + modernc.org/sqlite v1.60.1 + miniredis v2.40.0(`go get` + `make tidy`,libc 随版自动抬升);验证:ratelimit/store/semantic 既有全量测试绿 + `make check` 全绿 + 冒烟观察 redis 连接数形态(9.23 pipeline 连接池默认变化)
- [x] 1.2 google/uuid 退役 → stdlib `uuid`:11 文件 14 处调用点替换(`uuid.NewV7`/`uuid.New` 平替;`uuid.Must` 生成包装按各点现形改写,保持原失败语义;stdlib 无生成版 Must 已在 design 2.5 登记);`make tidy` 确认 google/uuid 出走 go.mod;验证:ID 格式回归断言(小写连字符 v7)+ `make check` 全绿
- [x] 1.3 coder/hnsw **维持 v0.6.1(上游最新正式 tag)**:钉 master 伪版本路线被两重证据否决——①owner 裁决不钉哈希(2026-10-18);②实测伪版本热路径回退 7 倍(HNSWTop5 381µs→2.69ms,p99 8.35ms,正确性修复使搜索更彻底的延迟代价不可接受);验证:回退后 semantic 测试绿;HANDOFF 登记观察项(上游发新 tag 即评估跟进)
- [x] 1.4 golangci-lint 2.14 → 2.15.0(mise.toml + ci.yml `version` 两处同改);验证:`make check` 全绿,新增告警逐条处置(误报按 disable 注释纪律登记,不得静默降版)——实测 `golangci-lint run` 0 issues,无新告警

## 2. web 渐进批(每步全门禁后才进下一步)

- [x] 2.1 纯 update 批:react/react-dom 19.3.1(+@types 19.3.0)、radix-ui 1.8.1、tailwind-merge 3.7.0、vitest 5.0.5、jsdom 30.1.3、prettier 3.9.10、stylelint 17.16.0、typescript-eslint 8.71.2;验证:四件套 + vite build(/tmp)绿
- [x] 2.2 TypeScript ~5.7.2 → ~6.0.3;验证:typecheck/tsc -b/vitest/vite build 全链绿;唯一迁移点 = 新增 `src/vite-env.d.ts`(TS 6.0 新增 TS2882 对 `import "./index.css"` side-effect 导入报错,Vite 官方脚手架标准修法);如遇阻断回退 ~5.9.3 并登记原因(未触发)
- [x] 2.3 recharts 3.10.1:Overview.tsx 单文件迁移(零 API 破坏,typecheck 直接过);验证:vitest 绿 + 图表页 375/768/1280 × 亮暗六帧走查(dev server + 路由 mock 数据,双系列/万\/亿轴格式/移动端 h-48 折降/暗色对比度全过)
- [x] 2.4 lucide-react 1.59.0;验证:构建绿,导入路径零变化,全仓无品牌图标;aria 走查过(CopyButton/theme/导航/删除控件均有 aria-label,装饰图标由 1.x 默认 aria-hidden 隐藏)
- [x] 2.5 Vite 8.3.4 + @vitejs/plugin-react 6.1.2;验证:peer 零警告,四件套绿,Rolldown 构建 720ms(vite6 时代 2.6s);embed 冒烟过(`/console/` 200 + `/console/assets/*.js` 200 + healthz 200,临时实例 127.0.0.1:18785 已清理)
- [x] 2.6 React Compiler:eslint-plugin-react-hooks@7 接入(flat `configs.flat.recommended`)跑基线——4 错 2 警全根修(Usage 抽 keyed 子组件替代 effect 首行同步 setState/Audit·ImagePricing 同步重置拆回事件处理器+取数改 promise 链形态/Keys Date.now 内联 onClick 闭包),再开 `react({ compiler: true })`(新 devDep oxc-transform-react,plugin-react 6 官方指定载体,白名单增补记 HANDOFF);验证:lint 零问题 + 15/15 测试 + build 787KB(compiler 关态 769KB,+18KB 运行时),bundle 内 `_c(`/`useMemoCache` 特征实证编译器生效,可一键摘除
- [x] 2.7 ESLint 10.13.0:flat config 已就位零迁移(codemod 未触发);验证:`pnpm lint` 零问题 + typecheck/test/build 绿
- [x] 2.8 pnpm 12.14.0:packageManager 字段 + 项目 mise.toml 钉 `pnpm = "12.14"`(双钉纪律);验证:pnpm 12 迁移 lockfile 后 `install --frozen-lockfile` 过,四件套 + build 全绿(pnpm/action-setup 读 packageManager,CI 自动跟进)

## 3. CI 一致性批

- [x] 3.1 plugins.yml checkout@v5 → @v7 + release.yml node 22 → 24;验证:三 workflow YAML 语法过,checkout@v7/node 24 全仓一致

## 4. 收口

- [x] 4.1 顶层验收:全量 `make check` 绿(coverage gate PASS 70.4%,Go 侧 golangci-lint 2.15 + web 侧 pnpm12/TS6/eslint10 全新门禁链)+ CGO_ENABLED=0 构建过 + openspec validate 24/24 + embed 冒烟(/console/ 200+assets 200+healthz 200)+ 真上游数据面单笔(dashscope qwen-plus 200,9/20 tokens,usage 落库读回一致)+ 走查(图表页 375/768/1280×亮暗六帧 + 改码四页 1280 实渲染);证据见 HANDOFF 验收记录
- [x] 4.2 HANDOFF 更新 + 开放决策项登记:hnsw 伪版本裁决结果与回退口径、goccy/go-yaml 与 tiktoken-go 低频维护观察项、TS 7/typescript-eslint 跟踪点(#12518,7.1+)、node 26 下轮议题、jsontext 流式解析观察项

## Workflow follow-up

- 归档:`openspec archive tech-stack-refresh-2026-10`(skip_specs 变更,无主规格同步)
- commit/push 等用户逐次明确指令(铁律 4)
