# Phase 1:端到端流式透传(vertical slice)

> 状态:全部完成,闸门 6 判据全过(2026-09-09;判据 3 以 ollama 真上游实录,见 HANDOFF) · 前置:Phase 0 闸门全绿 · 版本:— · 允许新增依赖:`github.com/goccy/go-yaml`(配置)、`github.com/google/uuid`(UUIDv7:request_id,后续 DB 主键同源)

## 目标

一条链路真跑通:OpenAI 兼容请求进 → 单上游(openai 基座)→ SSE 逐事件透传出。**纵切面即最终架构**:本阶段写下的包边界、DTO、中间件链、fake 上游都是终态组件,禁止一次性脚手架。

## 安全前提

本阶段无鉴权:本地验证一律 `MUNIU_ADDR=127.0.0.1:8787`;**鉴权落地(Phase 2)前不得部署到任何非本机环境**。

## 任务清单

- [x] **T1.1 protocol DTO**:`internal/protocol`:`ChatRequest`(只解析网关需要的字段:model/stream/messages/tools 存在性/temperature/max_tokens;**保留原始字节** `Raw json.RawMessage`)、`ChatResponse`、`StreamChunk`(delta 结构含 content/reasoning_content/tool_calls)、`Usage`(统一形态)、`ErrorEnvelope`(conventions 格式)。出站 body 生成规则:以 `map[string]json.RawMessage` 解原文 → 只替换 `model` → 重编,**未知字段零丢失**(表驱动测试:带未知字段的请求经改写后字段集合等价)。
- [x] **T1.2 config**:`internal/config`:定位(`-c` > `MUNIU_CONFIG` > `./muniu.yaml`)→ `${VAR}` 展开(缺失变量列全后一次报错)→ 结构校验一次性报全(必填/枚举/URL/路由引用 provider 存在/weight>0/priority≥0);测试先行:缺变量、坏 URL、悬空路由、合法样例(`muniu.example.yaml` 必须通过校验——把它当 fixture)。
- [x] **T1.3 logging**:`internal/logging`:slog JSON/text 按配置装配;`WithRequestID(ctx)` 与从 ctx 取值的 helper;字段名严格按 conventions 表。
- [x] **T1.4 server 与中间件**:`internal/server`:`http.ServeMux` + `Chain(...)`;中间件 `RequestID`(UUIDv7,写 ctx + 响应头 `X-Muniu-Request-Id`)、`Recover`(500 信封+堆栈日志)、`AccessLog`(进出两条);`/healthz` 从 main 迁入;优雅退出(`signal.NotifyContext` → `Shutdown` 15s);httptest 测试:panic 被兜住、request_id 回带、退出时在途请求完成。
- [x] **T1.5 fake 上游(核心测试设施)**:`internal/testkit/fakeupstream.go`:`httptest.Server`,剧本由测试以事件序列声明:每事件 `{data, delayBefore, splitAt(半帧切分点), closeAfter(突然关闭)}`;内置剧本 `normal / slow_chunk / mid_stream_cut / half_frame / usage_missing`;非流式剧本 `json_ok / json_500`。剧本自身要有测试(fake 也是代码)。
- [x] **T1.6 adapter openai 基座**:`internal/adapter`:接口(五方法,upstream-adapters 契约)+ 注册表 + `openai.go`:`Prepare`(URL 拼 `/chat/completions`、`Authorization: Bearer <key>`、model 改写经 T1.1 规则)、`Classify`(按状态码粗分:400/401/403/429/5xx/其他)、`NormalizeUsage`(OpenAI 列)、`StreamEvent`(passthrough 原字节 + 解析 meta:是否含实质内容/是否 usage 帧/是否 `[DONE]`)。本阶段 key 取 provider 第一把 key 的环境变量值。
- [x] **T1.7 proxy 转发核心**:`internal/proxy`:
  - 非流式:出站调用 → 状态透传 → 2xx 整包回写 / 非 2xx 归一为错误信封(状态映射表);
  - 流式:`bufio.Reader` 读上游,按空行切**完整事件**(半帧缓冲),逐事件 `Write+Flush`;响应头固定三项(proxy-and-streaming);三层超时(dial 用 `net.Dialer`、first_token 用 `time.Timer` 在首内容帧到达时停止、chunk_idle 用每帧重置的 timer);`r.Context()` 取消 → cancel 上游;
  - 候选迭代器**接口就位但只走单候选**(Phase 3 填 failover 循环,此处留清晰扩展点,不预实现);
  - 测试:T1.5 全部剧本 × 断言「客户端收到的字节序列」逐字节等价(passthrough 契约)+ 超时剧本断言错误信封形态。
- [x] **T1.8 装配与真上游实测**:`cmd/muniu/main.go` 装配 config→logging→adapter 注册→server;`cp muniu.example.yaml muniu.yaml` 填一家真上游(环境变量注 key);`make run` 后 `curl -N` 一条流式请求 + 一条非流式,**脱敏实录**进 HANDOFF;`MUNIU_ADDR` 环境变量仍覆盖配置(调试便利)。
- [x] **T1.9 基准与交接**:`BenchmarkSSEForward`(fake 剧本 normal,1000 事件)记录 ns/op 与 allocs/op 进 HANDOFF;`make check` 全绿;更新 HANDOFF。

## 设计指针

`architecture.md`「请求生命周期」1-3、7、10、12 步 · `proxy-and-streaming.md`「超时模型」「SSE 透传实现契约」 · `upstream-adapters.md`「Adapter 接口」「字段改写白名单」 · `data-and-config.md`「配置加载」。

## 验收闸门

1. fake 剧本 7 项测试全绿(`go test -race ./internal/proxy/...` 输出);2. 半帧剧本证明「不解析半事件」(测试名可见);3. 真上游流式+非流式脱敏实录;4. 未知字段零丢失测试绿;5. 基准数字;6. `make check` 全绿。

## Non-goals

鉴权 · 计量落库 · 限流 · 多候选/failover · quirks · SQLite · 桥接 · 任何管理接口。
