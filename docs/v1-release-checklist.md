# v1.0.0 发版判据核对表(T5.6,v2 判据基线)

> **2026-10-14 裁决改版(owner 委任)**:原「生产承载连续 2 周零人工干预」日历观察期方法论废止,
> 裁决记录见 HANDOFF 开放决策项 2026-10-12 行(改判)。核心理由:①soak 测的是起算日二进制
> 快照,而本仓持续演进,快照证据对 HEAD 归零——观察期与持续交付的代码库不兼容;②真实日流量
> 个位数,14 天 soak ≈ 百余请求,信息量近零,期间主要事件反而是环境性的(machine 重建);
> ③常驻载体已两次死亡于同一形态(machine 重建清卷),`--restart=unless-stopped` 救不了重建,
> 且 owner 已否决开机自启——该环境设计上就是瞬态的。
> **替代判据 = 当版可重复生成的证据**(门禁 + 真上游冒烟 + 跨仓实录 + 边界清点)。
> 全部勾选后由用户执行 tag v1.0.0 与 push(tag 时机仍由 owner 逐次指令)。

## 1. 版本门禁(替代原生产承载观察期;发版当日全部复跑)

- [ ] `make check` 全绿(vet + lint + race + coverage gate ≥65%,含 web typecheck/lint)
  - 2026-11-21:除 `api-types.d.ts` 未提交 git-diff 外全绿;提交后等价(生成幂等已证)
- [x] `make chaos` 全绿(2026-11-21 exit 0)
- [x] `openspec validate --all` 全绿,且无「已立项未实施」的挂起 change(2026-11-21:30/30)
- [ ] web 四件套 + `vite build --outDir /tmp` 绿
  - 2026-11-21:typecheck/lint/knip/test/build/e2e/size 均绿;整套 target 仍卡在未提交生成文件 diff
- [x] conformance 全 fixture 绿(2026-11-21 exit 0)

## 2. 真上游冒烟(发布前必跑;dashscope 单 key 承载,key 经 .env.local 注入,临时实例随跑随清)

- [ ] chat 兼容 lane(qwen-plus「你好」13/1 tokens → 12400 credits 计量 sanity)
- [ ] anthropic 直通 + 工具多轮(/apps/anthropic,bearer 声明)
- [ ] responses 快捷形态(省略 type + 字符串 content)
- [ ] gemini 双实例全链(按 docs/gemini-e2e-runbook.md)
- [ ] images(真厂商凭据到位后补跑;未到位如实登记,不阻塞其它 lane 判定)
- 冒烟实录贴验收记录(2026-10-12 已有三组实录可引用,发布当版复跑为准)

## 3. 跨仓证据(引用既有实录,不重跑)

- [x] 锦官 ingest 联调实录:欠费→Mock 充值→恢复器补扣恰一笔→账实相符(2026-10-02 跨仓记录)
- [x] 控制面四管子(价目/吊销/ingest/拓扑)联通状态(platform-data-plane 拓扑下发实录)

## 4. 压测(已完成证据引用)

- [x] 报告:`docs/benchmarks/2026-09-10.md`(muniu 单边 + one-api 对照;峰值 38,498 rps,p50 1.15ms@100)
- [x] README 关键数字已回填
- [ ] 可选:大版本演进后复核一轮(非前置)

## 5. 设计文档系列

- [x] `docs/articles/` 四篇(首 token 边界/流式 usage 计量/conformance 资产化/单二进制取舍)
- [ ] 外发(掘金/博客):用户决定,非发版前置

## 6. 边界清点与收尾(发布诚实性)

- [x] HANDOFF 开放决策项无「待裁决/待 owner」悬空项(2026-11-21:仅余带触发条件的登记项)
- [x] 已知限制在设计文档有登记(2026-11-21 核对:proxy-and-streaming「诚实边界」段)
- [x] 既有真实库迁移链 0001→0022 依序应用无误(2026-11-21:`data/muniu.db` v18 副本升级到 v22)
- [x] 文档一致性复核(2026-11-21:修正 cookie Secure/限流口径,设计文档与代码对齐)
- [ ] README 顶部「施工中」横幅撤下,改为稳定性声明(引用压测数字 + 门禁/冒烟证据,无观察期数据)
- [ ] tag v1.0.0 与 push:**由用户执行**
