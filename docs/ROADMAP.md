# ROADMAP — 六阶段路线

> 每个 phase = 一份 `docs/plans/phase-N-*.md` 可执行任务清单 + 一组验收闸门。
> **闸门纪律:上一阶段全部判据留证通过,才允许开下一阶段第一个任务**(AGENTS.md 执行循环)。

## 总览

| Phase | 主题 | 交付物 | 版本 | 核心闸门(全量判据见对应 plan) |
|---|---|---|---|---|
| 0 | 工具链与仓库初始化 | 可编译骨架、tokenctl CLI、门禁打通、远端建立 | — | `make check` 绿 + healthz 冒烟 + 骨架已入库 |
| 1 | 端到端流式透传(纵切面) | 单上游 OpenAI 兼容代理,SSE 逐 chunk 透传,假上游测试设施 | — | 对 fake 上游的流式/非流式各 3 场景实测通过 |
| 2 | MVP | OpenAI 兼容基座 + quirk 层(3-4 家)、apikey 签发鉴权、usage 计量落库、RPM 限流、错误信封 | v0.1.0 | conformance fixtures ≥3 家全绿 + 计量与牌价核对 + tag |
| 3 | 流量治理 | key 池(加权/冷却/健康度)、failover 完整语义、TPM 滑动窗口、日配额、多租户列生效、国产五家 quirk 补齐、Anthropic 桥接 | v0.2.0 | failover 判定表全格测试覆盖 + 故障演练(chaos)通过 |
| 4 | 语义缓存 + 控制台 | 语义缓存(v0 设计)、管理 API、React 控制台(复用 kongming 前端范式) | v0.3.0 | 缓存命中率/阈值回归测试 + 控制台 e2e 冒烟 |
| 5 | 生产集成 + 压测 | 承载 kongming 全部模型流量、压测报告(vs one-api)、设计文档系列(≥4 篇) | v1.0.0 | 生产承载连续 2 周零人工干预 + 压测数字入 README |

阶段间依赖:1→2→3→4→5 严格串行;Phase 4 的控制台可与 3 末尾并行(但版本仍随 4 发)。

## 各阶段一句话目标

- **Phase 0**:让仓库「能开发」——工具链齐、门禁通、CI 亮绿。
- **Phase 1**:让一条链路「真跑通」——请求进、SSE 出,纵切面即最终架构,禁止一次性脚手架。
- **Phase 2**:让网关「可用」——多供应商、有鉴权、有计量、有限流,能替 kongming 扛流量(灰度)。
- **Phase 3**:让网关「可信」——故障自愈(key 冷却/换道)与成本闸门(配额)有完整、可测试、可演练的语义。
- **Phase 4**:让网关「聪明」——语义缓存省钱,控制台可视。
- **Phase 5**:让网关「被证明」——生产数据与压测数字是简历叙事的弹药。

## Backlog(明确不进当前阶段,防 scope creep)

~~SIGHUP/文件监听热加载~~(已落地:openspec change `sighup-config-reload`,cmd 集成测试自发自捕信号钉住;热更范围如实登记:auth/metrics/管理面/静态价目兜底/告警装配不热更) · ~~OpenAI Responses API 入站桥接~~(已升格:ADR-0012,openspec change `responses-api-inbound`) · ~~Redis 后端分布式限流~~(已落地:openspec change `cluster-ratelimit-shared-view`,ADR-0008 修订,GCRA+TPM 槽双实现与降级纪律) · ~~Anthropic Messages 入站形态~~(已落地:ADR-0013,`POST /v1/messages` 边缘翻译) · gRPC 入站 · MCP/A2A 协议(观察 agentgateway 动向) · ~~告警通知(webhook)~~(已落地:openspec change `webhook-alerting`,internal/alerting 去重窗口+退避重试+失败隔离,nil 零开销;接入候选全失败/key 池不可用/计量汇降级三事件) · 控制台多用户/SSO · ~~插件机制(WASM)~~(已升格:ADR-0011,openspec change `wasm-plugin-runtime`) · 请求级 cost 预算中断 · ~~Anthropic 桥接请求侧工具往返翻译~~(已落地:openspec change `anthropic-bridge-tool-roundtrip`,chat×anthropic 工具多轮请求侧翻译完整——tool_calls→tool_use 块/role=tool→合并 tool_result/配对校验;真异构两家均整家实现,登记缺口清零) · OpenAI images/audio/files 端点(有具体消费场景才立项——按张/按秒计价是新计量维度) · key 级模型白名单 · key 启停 · ~~Gemini 原生桥接~~(已落地:ADR-0015 `gemini-native-protocol`,`adapter: gemini` 整家实现回归值域;此前的「裁撤+值域移除」裁决(ADR-0003 更正/`retire-gemini-adapter-token`)被本 ADR 取代)。
Backlog 升格规则:用户点名 + 影响当前架构设计时,先补 ADR 再排期。

## 配套:人类 Go 学习路径(与执行并行,写给仓库所有者)

按阶段读对应代码,每次执行会话结束后对照自查:

1. Phase 0-1:A Tour of Go → 《Effective Go》;重点:`net/http` 接口、`io.Reader/Writer`、`context`。
2. Phase 2-3:读 `internal/adapter`、`internal/keypool`;重点:接口与组合、`sync.Mutex`、channel 与 `select`、`errgroup`。
3. Phase 4:《100 Go Mistakes and How to Avoid Them》;重点:泛型使用边界、内存模型与 race。
4. 每阶段自查题:能否不看代码讲清「请求生命周期 12 步」?能否画出 failover 判定表?——能,才算消化。
