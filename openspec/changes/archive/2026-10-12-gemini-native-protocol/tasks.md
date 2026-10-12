# Tasks

## 1. conformance 语料与协议层翻译核心(测试先行域)

- [x] 1.1 冻结 `conformance/gemini/` 语料库(请求/非流式响应/流式事件/error 信封四类 fixture,合成并逐条标注来源与 ⚠ 待核对项;含「HTTP 200 + 流内错误帧」负面形态——百炼实证的上游失真样式;`schema.json` 扩展 gemini 形态),`conformance/README.md` 注记双向消费方式;fixture 可被 1.2/2.1/3.2 测试加载
- [x] 1.2 先写失败测试再实现 `internal/protocol` Gemini 入站翻译纯函数:contents(role user/model)/systemInstruction(字符串与 parts 两形态)/functionDeclarations/functionCall+functionResponse 部件/generationConfig(temperature/topP/maxOutputTokens/stopSequences)/inlineData(data URL)→ canonical chat;fileData/音视频部件/array-form 流式显式拒绝机器码;`go test ./internal/protocol/ -race` 全绿
- [x] 1.3 先写失败测试再实现 `internal/protocol` Gemini 响应反向映射纯函数:非流式 GenerateContentResponse 编码(candidates/parts/usageMetadata/finishReason 映射)、alt=sse 事件机(首帧不变式/usage 帧/finishReason 终帧/断流错误帧诚实收尾)、错误信封 `{error:{code,message,status}}`;`go test ./internal/protocol/ -race` 全绿

## 2. 出站 gemini 适配器(测试先行域)

- [x] 2.1 先写失败测试:Prepare 逐字段 fixture(URL/`:generateContent` 拼接/`x-goog-api-key` 头/systemInstruction 提升/role 映射/functionDeclarations/tool_choice auto|required|none→functionCallingConfig/inlineData/role=tool 完整)、Classify 判定表(INVALID_ARGUMENT/RESOURCE_EXHAUSTED/UNAVAILABLE 等)、NormalizeUsage(含 usageMetadata 缺失形态)、StreamEvent 事件机(meta 序列/流关闭=IsDone/错误帧)、RewriteResponse;含回环对拍(出站翻译 ∘ 入站翻译在可翻译子集域恒等);`go test ./internal/adapter/...` 可运行
- [x] 2.2 实现 `internal/adapter/gemini.go` 与 `For`/`Types()` 分支(值域单源,`types_test` 断言 gemini 可真实构造),Bridger 标记如实声明;`go test ./internal/adapter/... -race` 全绿
- [x] 2.3 bridgegate 升级声明式缺口:先写失败测试(anthropic 桥接豁免行为逐字节不变;gemini 候选工具多轮正常出站不豁免、零 EMA 副作用)→ 实现翻译完整性可选能力声明接口,gate 条件改为「Bridger ∧ 缺口声明」;`go test ./internal/proxy/ -run TestBridgeGate -race` 全绿

## 3. 入站 handler、路由与鉴权适配(测试先行域)

- [x] 3.1 先写失败测试再实现 `AdaptGeminiKey`(对齐 AdaptAnthropicKey 先例):`x-goog-api-key` 头与 `?key=` 查询参数两形态适配为平台 Bearer;`?key=` 读后从 URL 剥离(访问日志/结构化日志断言无 key);`go test ./internal/server/ -race` 全绿
- [x] 3.2 先写失败测试:HandlerGemini 路径解析(`{model}:generateContent` / `:streamGenerateContent` + `alt=sse` 校验)、翻译委托既有管道、非流式/流式反向映射、路径别名不存在同规错误、翻译期 400 机器码信封;`go test ./internal/proxy/ -run TestGemini -race` 全绿
- [x] 3.3 实现路由表条目(`POST /v1beta/models/{model}:generateContent`、`POST /v1beta/models/{model}:streamGenerateContent`,数据面全链 + AdaptGeminiKey)并进漂移门禁;`make check` 全绿

## 4. 组合回归与文档

- [x] 4.1 组合测试冻结:跨协议缓存命中(chat 写入/Gemini 命中、反向)、治理与计量等价口径、换道矩阵(gemini 候选 ↔ openai 系候选,含流式首字节前换道)、工具多轮经 gemini 候选全链、usage 缺失负面用例;`go test ./internal/proxy/ -race` 全绿
- [x] 4.2 文档落地:ADR-0015(Gemini 原生协议出入站,含内置化否决外部工具、声明式缺口根因修复记录)、proxy-and-streaming.md「Gemini 入站翻译」契约节(冻结映射表+登记限制)、upstream-adapters.md(Gemini 归一表从备查转正+真异构分类注记+tool_choice 完整映射对照)、provider-presets.md 补条目、ADR-0013 备选记录解除注记;`docs/` 交叉引用可达
- [x] 4.3 收尾门禁:`make check` 全绿(vet + lint + race test);验收证据(命令+输出)按仓库纪律贴 HANDOFF 验收记录区;双实例无 key 联调 runbook 文档化(127.0.0.1:1878x 惯例;断言口径=流终帧内容而非 HTTP 状态码;冒烟资源纪律=临时 key create→revoke、一次性实例,对齐 kongming e2e 配方)

## Workflow follow-up

- 双实例无 key 真模型联调(人工,按 runbook):实例 B=本变更入站 + DASHSCOPE 真上游构成模拟 Gemini 端点,实例 A=本变更出站指向 B,genai SDK/gemini-cli 从 A 或 B 侧打通 wire 往返,B 侧计量落账核对;证据补录 HANDOFF。
- owner 自备模拟端点(gemini-cli-router/llm-emulator 等)替换实例 B 的替换性联调,记录差异(如有)回填 fixture ⚠ 项。
- 真实 Gemini key 到位后:同一适配器仅换 baseURL 指官方端点冒烟,逐条复核语料 ⚠ 待核对项并登记 HANDOFF。
- 直通路由判定表增补 gemini×gemini 行(协议平等:Gemini 原生通道与 chat/messages/responses 同权),补同构直通组合测试;并入 passthrough 落地后的后续小变更,owner 指令触发。
- 归档前按 openspec 纪律同步主 specs(gemini-inbound / gemini-native-outbound 新建、adapter-taxonomy 值域示例更新),由 owner 指令触发 archive 流程。
