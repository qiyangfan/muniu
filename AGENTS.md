# AGENTS.md — muniu 执行者工作契约

任何 AI 会话(包括超长自治任务)在本仓库内工作前**必须先读本文**;本文与 `docs/design/`、`docs/adr/` 共同构成项目宪法:设计文档说「做什么/为什么」,本文说「你被允许怎么做」。

## 项目一句话

木牛(muniu):Go 单二进制 LLM 网关,OpenAI 兼容入站、多供应商出站,带 key 池/failover/限流/计量计费/语义缓存;定位是**简历级工程作品 + kongming 平台的生产基建**,明确放弃商业化与冲星预期——因此**工程质量与设计文档的完整度优先于功能数量**。

## 阅读顺序(开工必读)

1. `HANDOFF.md` — 当前状态与下一步(唯一入口)
2. `docs/ROADMAP.md` — 你在哪个阶段、闸门判据是什么
3. 当前阶段的 `docs/plans/phase-N-*.md` — 任务清单
4. 任务引用的 `docs/design/*.md` 章节 — 行为契约
5. `docs/conventions.md` — 工程约定

## 硬性边界:契约级 vs 实现级

- **契约级(不得擅改)**:`docs/design/` 中的协议语义、failover 判定表、数据 schema、配置结构、错误信封、超时模型;`docs/adr/` 的选型结论;接口签名与包边界。
  执行中发现设计缺陷:在 `HANDOFF.md`「开放决策项」登记(现象/影响/建议方案),**按现行契约保守实现**,绝不半途改契约等确认。
- **实现级(自由裁量)**:包内部函数拆分、非导出类型、算法内部结构、测试组织方式。
  判断口诀:**改了会不会改变外部可观测行为(接口/存储/日志语义/配置格式)?会 → 契约级;不会 → 实现级。**

## 铁律(违反即返工)

1. **质量门禁是完成的唯一标准**:每个任务收尾必须 `make check` 全绿(vet + lint + race test);绿之前不得勾选任务、不得声称完成。
2. **测试先行域**:协议适配(quirk 归一化)、failover 判定、计量计费、限流配额——先写失败测试再实现(负面用例必须覆盖:非法请求/上游半途断流/usage 缺失/超限)。
3. **依赖白名单制**:每个 phase 的 plan 列出「允许新增依赖」;白名单之外的任何 `go get` 禁止,确需引入 → 记 HANDOFF 开放项。选型原则:stdlib 优先,单一职责的小库优先,禁止框架(web 框架/DI 容器/ORM 一律不引)。
4. **git 写操作(add/commit/push/tag)必须等用户逐次明确指令**,只读查询(status/diff/log)随时可以;这是用户全局铁律,在本仓库同样生效。
5. **文档与代码注释一律简体中文**;注释只写「代码本身说不出的约束」(为什么/边界/陷阱),不复述代码。
6. **诚实汇报**:测试失败贴原始输出,不粉饰;验收证据(命令+输出)原文粘进 HANDOFF 验收记录区;没跑过的验证不得说「已验证」。

## 执行循环(超长任务的标准节奏)

```
读 HANDOFF → 定位当前 phase plan 中首个未勾任务 → 读任务指向的设计章节
→ 测试先行域先写失败测试 → 实现 → make check 全绿 → 勾选任务+贴证据
→ 阶段闸门:跑完该 phase 全部验收判据 → 更新 HANDOFF(状态+验收记录)→ 进入下一 phase
```

- 严格按 phase 顺序推进,**上一阶段闸门未全绿不得开下一阶段的任何任务**。
- 任务粒度即勾选粒度:一个任务一次收尾(实现+测试+门禁+勾选),不留「半勾」状态。
- 会话因任何原因中断:HANDOFF 必须处于「下个会话可无缝续作」状态。

## 命令速查

```bash
make check          # 全量门禁 = vet + lint + race test(完成标准)
make test / bench   # 单跑测试 / 基准
make build / run    # 单二进制构建 / 启动(读 muniu.yaml,冒烟 GET /healthz)
make smoke PROVIDER=deepseek   # 真上游冒烟(需环境变量注 key,仅手动)
make tidy           # 白名单内新增依赖后必跑
```

## 目录地图

```
cmd/muniu/        入口(装配,禁止业务逻辑)      cmd/tokenctl/   Phase 0 学习用 CLI
internal/         全部私有包(config/protocol/proxy/adapter/keypool/failover/
                  ratelimit/metering/semantic/store/auth/tenant/logging/testkit)
conformance/      国产协议 fixture 测试集(独立资产,adapter 测试消费)
docs/             ROADMAP/design/adr/plans/conventions
muniu.example.yaml 配置样例(真实 muniu.yaml 已 gitignore)
```

包依赖方向(禁止循环):`protocol` 是 DTO 单源 ← `adapter`/`proxy` ← 编排层;`store` 只被 `metering`/`auth`/`tenant` 消费;`testkit` 只进测试构建。

## HANDOFF 维护纪律

只写四块:当前状态 / 下一步动作序列 / 开放决策项 / 验收记录。**不写变更明细**(那是 git log 的事),单次更新控制在 30 行以内。

## 明确不做(全局 Non-goals)

商业化与平台功能(计费支付/套餐商城/用户注册与门户/渠道管理门户——归独立控制面产品,边界见 ADR-0005)、Kubernetes Operator、插件市场、管理面 SSO、对 gRPC 的入站支持、分布式强一致限流——出现此类诉求一律记 backlog,不进当前 phase。

集群**数据面**形态(无状态副本 + 控制面同步)不属于本清单,按 ADR-0005 演进;涉及跨仓契约的 P1 前置 ADR:0006(自校验 key 与状态同步)、0007(可插拔计量汇)、0008(集群限流语义)。

## 前端宪法(隆中聚合层单源;2026-09-26 owner 裁决:Radix 标准件路线)

`web/` 控制台统一遵循聚合目录 `../FRONTEND-CONSTITUTION.md`(Article 0–10);本节为执行摘要,冲突时以宪法为准。
> 2026-09-26 owner 裁决补记:控制台组件体系收敛为「Radix 无头库承担交互语义 + 本地标准件承担
> 视觉配方」;antd 一度被引入后经裁决剥离(antd 不入本仓,除非未来出现其真正擅长的密集 CRUD
> 需求面,且届时必须三仓一起迁)。

- 元原则:**用组件不用手搓;用默认不胡乱覆盖;用 token 不用魔法值。**
- **交互语义必须来自成熟库,禁止无库手搓交互件;视觉与组合走标准件**(owner 2026-09-26 裁决
  沉淀):select 已采用 Radix 组合式(焦点圈定/键盘导航/portal/aria 语义由 Radix 承担,
  配方对齐 kongming 同位文件),后续新增交互件同理优先 Radix 原语 + 本地配方。
- muniu 是「本地标准件」仓:`web/src/components/ui/` 的组件名、props API、变体名必须与
  kongming/jinguan 对齐(组件注释「本地标准件,API 对齐 kongming」);
  ui/ 标准件的**语义级改动**应先发生在 kongming,再手工对齐本仓(双仓逐字节一致规则不适用本仓)。
- 色彩:只引用 index.css @theme 语义 token(词汇表三仓同名),禁 `#hex`/字面色板;图表第二
  系列走 `--color-chart-series-2`。暗色是一级公民(`.dark` 覆盖同名变量整仓翻转):
  **新增 token 必须同时在 `.dark` 补值**;主题契约 = index.html 防 FOUC 内联脚本 + `lib/theme.ts`
  (key `muniu-theme`),双侧注释互指,改动必须同步(Article 3/10)。
- 组件:基础交互件一律 `components/ui/`,业务代码禁裸 `<button>/<input>/<select>/<textarea>`;
  破坏性操作必须 AlertDialog 确认(禁 window.confirm);≥2 处同类 UI 必须抽公共组件;
  间距只用 Tailwind 刻度(Article 1/2/4)。
- 页面骨架只用标准件:FormRow / Table / ErrorAlert / NoticeBanner / EmptyState / Skeleton /
  StatCard / DataList(一套列定义驱动桌面表格 ↔ 移动实体卡片),禁每页手搓;
  错误走 ErrorAlert(role=alert),禁静默空白(Article 6/7)。
- 响应式(2026-09-26 owner 插话升格:**移动端按原生范式重设计,不做「桌面缩小」**):
  窄屏 = 底部图标+短标签栏主导航(粘性顶栏只留品牌+主操作,页签切换回顶,安全区
  `viewport-fit=cover`+`env(safe-area-inset-bottom)`)+ 数据表格转实体卡片 + 表单单列全宽;
  md+ 恢复顶栏导航与表格;统计网格/图表高度带 `sm:` 折降。多屏验收 = 375/768/1280 三档 ×
  亮暗,移动端按移动范式逐页走查,桌面确认无移动范式泄漏(Article 9)。
- **web 依赖白名单**(对齐铁律 3 的白名单制):react 系 / vite 系 / tailwind v4 / tw-animate-css /
  lucide-react / recharts / clsx / tailwind-merge / class-variance-authority / radix-ui /
  openapi-typescript;之外的任何 `pnpm add` 先记 HANDOFF 开放项。
  (2026-10-02 增补 tw-animate-css:随三仓标准件视觉收敛入仓,聚合层宪法 Article 4 同步。)
- 收尾门禁:`pnpm typecheck && pnpm lint && pnpm lint:css && pnpm format:check` +
  `pnpm exec vite build --outDir /tmp/dist-check`(禁直接写 web/dist,那是 Go embed 的发布产物)
  + 裸元素自检 grep(business 代码禁裸 button/input/select/textarea)+ 亮暗两态 ×
  375/768/1280 走查。
