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

商业化功能(计费支付/套餐商城)、Kubernetes Operator、插件市场、管理面 SSO、对 gRPC 的入站支持、高可用集群形态——出现此类诉求一律记 backlog,不进当前 phase。
