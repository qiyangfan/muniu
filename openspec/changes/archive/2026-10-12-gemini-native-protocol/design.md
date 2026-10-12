# Design

## Context

`gemini` 类型名曾因无实现被 `retire-gemini-adapter-token` 退役(配置能过校验、装配必失败的教训);ADR-0013 将 Gemini 入站记为「出现真实需求再立法」,owner 本次点名触发。upstream-adapters.md 的 Gemini usage 归一表「备查」可转正。owner 参考方案的核心洞察:模拟 Gemini 只需 wire-format 对齐,不需要谷歌服务——外部转换器(llm-emulator/gemini-cli-router)印证了形态,但外部 Node 工具不进仓(依赖白名单制),muniu 的最优解是把转换器(入站翻译)与被测方向(出站适配器)都内置,用同一份 fixture 契约在无 key 条件下完成 CI 冻结与真模型联调。

## Goals / Non-Goals

**Goals:**

- Gemini 原生 wire 双向(入站翻译 + 出站适配)全链可用,CI 无 key 冻结契约。
- 无 key 真模型联调拓扑可文档化复现(双实例自替身),且兼容 owner 自备的外部 Gemini 兼容端点。
- 出站适配器工具多轮翻译完整(functionCall/functionResponse 双向),不落入 anthropic 桥接的登记缺口。

**Non-Goals:**

- Vertex gRPC 形态、`countTokens`/`files`/`caching`、Gemini 原生 embeddings、array-form 流式、音频/视频多模态、`responseSchema` 结构化输出(均登记 backlog,触发条件见 tasks)。
- 对 `protocol-native-passthrough` 提案的依赖(两变更无耦合;齐备后直通路由自动受益)。

## Decisions

### 决策 1:内置化而非引外部工具,fixture 单源双向对拍

转换器本体 = 入站边缘翻译层(ADR-0012/0013 同款架构),不是外部进程。`conformance/gemini/` 语料库为唯一 wire 契约源,被两方向**反向消费**:出站翻译的产物必须逐字节命中语料,入站翻译的消费对象取自同一语料——回环对拍(出站翻译 ∘ 入站反向映射 = 恒等,在可翻译子集域内)作为强漂移探测器冻结进测试。备选「两方向各建 fixture」否决:同一 wire 契约两套语料必然漂移。外部工具(llm-emulator 等)仅作开发期人工对拍参考与联调替身,不进任何依赖清单。

### 决策 2:REST `v1beta` wire,非 gRPC;鉴权形态显式声明

对齐公有 Generative Language API REST 形态(`{base}/v1beta/models/{model}:generateContent` 与 `:streamGenerateContent?alt=sse`);Vertex gRPC 不做(按既有裁决:目标 JD 明确要求再立项)。出站鉴权声明 `x-goog-api-key` 头(官方支持形态中唯一不把 key 暴露进 URL/日志的一种);入站同时适配 `x-goog-api-key` 与 `?key=`(SDK/生态缺省形态),后者读后从 URL 剥离。流式仅 `alt=sse`(主流 SDK 缺省),array-form 显式 400。

### 决策 3:无 key 联调拓扑(owner 方案内置化),三层验证

① **CI 层**:fixture 冻结 + testkit 假上游,零网络零 key;② **真模型联调**:双实例拓扑——实例 B 跑本变更入站 + 真实上游(如 dashscope,qwen-plus「你好」13/1 tokens 冒烟口径沿用),即构成「模拟原生 Gemini 端点」;实例 A 跑本变更出站指向 B,wire 全保真往返且 B 侧有完整计量/日志可观测;③ **替换性**:实例 A 只对 wire 契约编程,owner 自备的任何 Gemini 兼容端点(gemini-cli-router、llm-emulator、或未来拿到 key 的官方端点)可直接替换 B——真 key 到位后仅换 baseURL,契约零改动。**插件系统定位澄清**:ADR-0011 WASM 插件挂点在 canonical chat 请求体策略层(脱敏等),不承载 wire 翻译;owner 所述「通过插件转一遍」由协议边缘层承担,外部转换工具属可选替身而非仓库组件。

### 决策 4:gemini 出站为「翻译完整」的桥接型,bridgegate 升级为声明式缺口

Gemini 适配器按 ADR-0003 属真异构协议整家实现,请求侧工具多轮翻译(functionResponse/functionCall)完整——**结构性问题**:现行 bridgegate 按 `Bridger` 标记豁免工具多轮流量,而该豁免的真实依据是 anthropic 桥接的翻译缺口;gemini 如实标记 Bridger 会被误豁免,不标记则「桥接型=唯一改写者」的分类学失真。根因修复:豁免判定升级为**声明式缺口**——新增可选能力接口(如 `ToolRoundtripComplete() bool`),anthropic 桥接声明不完整(现状行为逐字节不变),gemini 声明完整;gate 按「Bridger ∧ 缺口声明」执行。备选「gemini 不实现 Bridger」否决:改写白名单的分类学撒谎;备选「gate 硬编码 kind 清单」否决:登记表式硬编码,新增桥接必漏。

### 决策 5:tool_choice 完整映射与 usage 归一转正

Gemini `functionCallingConfig.mode` 原生支持 NONE——anthropic 桥接的 `tool_choice:"none"` 登记缺口在 gemini 适配器上不存在,映射表完整覆盖 auto/required/none;`usageMetadata` 归一表从「备查」转正(thoughtsTokenCount 归入 reasoning 口径,登记);`promptFeedback.blockReason` 归 content_filter,禁止空内容伪造成功。

## Risks / Trade-offs

- [无真实 Gemini 流量样本,契约按公开文档保守合成] → fixture 逐条标注来源与 ⚠ 待核对项(anthropic 桥接同款先例:区域屏蔽按公开契约保守实现,可访问后逐条复核);owner 提供的真实样例/模拟端点响应可随时充实语料,不改契约结构。
- [流式无 [DONE] 与既有管道假设] → `EventMeta.IsDone` 由流关闭产生,计量多帧合并口径复用;混沌/回归测试覆盖断流收尾(错误帧诚实收尾,不伪造正常终帧)。
- [`gemini` 回归值域的装配回归] → `types_test` 强制每个值域成员可被 `For` 构造(retire 教训执法面),装配冒烟进任务验收;既有 6 类全量回放零改动。
- [usageMetadata 字段在部分 Gemini 兼容端点缺失] → 沿用「usage 缺失」既有负面用例纪律(不粉饰、按登记语义落账),fixture 增缺失形态。
- [上游 HTTP 200 表象下流内错误帧被误判成功] → kongming e2e 对百炼的实证形态(response.failed 帧藏于 200 SSE 流内):gemini 流中错误帧与断流诚实收尾已入 spec;语料补该负面形态,联调 runbook 断言口径定为「终帧内容」而非 HTTP 状态码。
- [双实例联调的配置与端口复杂度] → runbook 文档化(沿用 127.0.0.1:1878x 一次性实例惯例),非 CI 依赖。

## Migration Plan

纯增量:新入站端点、新适配器类型、新 fixture 集;`gemini` 回归值域属配置面扩容,不改变既有取值行为。回滚即代码回退。落地顺序建议:协议层 DTO/翻译核心 → 出站适配器 → 入站 handler → 联调 runbook;ADR 编号 0015(0014 已被 protocol-native-passthrough 预定,按实际 apply 顺序微调)。

## Open Questions

无——影响 spec、方案或任务分解的未知项已闭合;真实 wire 样本属语料充实,进入既有 fixture 增量通道,不动契约。
