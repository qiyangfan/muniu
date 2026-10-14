# Gemini 原生协议双实例联调 runbook(gemini-native-protocol;人工步骤,非 CI)

> 目标:无真实 Gemini key 完成 wire 全保真往返。断言口径=**流终帧内容**(finishReason/usageMetadata),
> 而非 HTTP 状态码——上游存在「HTTP 200 + 流内错误帧」失真样式(百炼实证)。

## 拓扑

```
genai SDK / gemini-cli ──► 实例 A(出站 adapter: gemini)──► 实例 B(入站 /v1beta/*)──► 真实上游(dashscope 等)
                                              ▲
                     实例 B 即「模拟原生 Gemini 端点」(wire 对齐,无需谷歌服务)
```

实例 B = 本仓 muniu(入站翻译 + openai 系出站指向真实上游);实例 A = 本仓 muniu(出站 gemini 指向 B 的 /v1beta)。任何外部 Gemini 兼容端点(gemini-cli-router/llm-emulator/未来真 key 官方端点)可直接替换 B——实例 A 只对 wire 契约编程,仅换 baseURL。

## 步骤(127.0.0.1:1878x 一次性实例惯例;临时 key create→revoke、用毕拆除)

1. **实例 B**:`MUNIU_ADMIN_TOKEN=dev-b` 起实例(端口 18781),配 dashscope 真上游(qwen-plus 冒烟口径:「你好」≈13/1 tokens),签发数据面 key。
2. **实例 A**:`MUNIU_ADMIN_TOKEN=dev-a` 起实例(端口 18782),provider `adapter: gemini`、`base_url: http://127.0.0.1:18781`(base_url 含版本段 /v1beta——官方端点为 https://generativelanguage.googleapis.com/v1beta),签发 key。
3. **入站冒烟(直打 B)**:`curl -H "x-goog-api-key: <B-key>" -d '{"contents":[{"role":"user","parts":[{"text":"你好"}]}]}' "http://127.0.0.1:18781/v1beta/models/<alias>:generateContent"` → 断言 candidates[].parts 有文本、usageMetadata.promptTokenCount≈13。
4. **往返冒烟(打 A)**:同请求体打 A 的同名端点 → 断言同上;B 侧计量落账核对(usage_logs 一行,ok)。
5. **流式**:`:streamGenerateContent?alt=sse` → 断言增量帧在先、终帧含 finishReason=STOP 与 usageMetadata(12/8 口径按真实上游回填);**不以 200 判成功**。
6. **负面**:未知别名(400 model_not_found Gemini 信封)、缺 alt=sse(400 stream_form_unsupported)、`?key=` 形态放行且访问日志无 key。
7. **用毕**:两实例停机,临时 key revoke,临时配置/库删除。

## 已知边界(v1,登记)

`safetySettings`/`topK`/`candidateCount`/`seed` 静默丢弃;音频/视频与 fileData 入站 400;array-form 流式 400;工具流式按收束帧发出;`countTokens`/`files`/`caching` 不提供。真 key 到位后:同一适配器仅换 baseURL 指官方端点冒烟,逐条复核 conformance/gemini 语料 ⚠ 项并登记 HANDOFF。

## 第三方多方认证(2026-10-14 实跑;破「双实例自产自销」的循环验证)

双实例互打只证明自家出站/入站契约自洽;升级形态是每个 wire 跳点都换独立实现互认:

```
Google 官方 genai SDK ──► muniu(入站 lane)──► muniu(出站 adapter)──► new-api(第三方 Gemini-native 服务端)──► dashscope
```

步骤(全部一次性工件,放 /tmp,用毕整体删除):

1. **上游**:new-api(QuantumNous/new-api,发行版二进制 + 本地 SQLite)建 OpenAI 型渠道指 dashscope compatible-mode(**base_url 不带 /v1**,带版本段会拼出 /v1/v1/… 404),发中继令牌;其 `/v1beta/models/{model}:{action}` 即 Gemini-native 服务端,鉴权收 `x-goog-api-key`。
2. **客户端**:临时 Go module(不动本仓 go.mod)引 `google.golang.org/genai`,`ClientConfig{APIKey, HTTPOptions{BaseURL}}` 指向 muniu——官方 SDK 序列化请求/解析响应,是入站 lane 的独立裁判。
3. **muniu**:provider `adapter: gemini`、`base_url: http://<new-api>/v1beta`(版本段约定同官方端点)、key=new-api 令牌。
4. 断言:非流式/流式文本往返(SDK 解析 usage 与 finishReason)+ **多工具往返**(SDK 声明双工具→真实上游并行 functionCall→SDK 回传 functionResponse→终答合成)。
5. 拆除按端口 kill(lsof -t)后核验,进程 argv 是相对路径,pkill -f 按目录名匹配不到(2026-10-14 踩坑:残留实例顶着已删除的二进制继续服务)。

2026-10-14 实跑全过:非流式 15/74、流式终帧 STOP+真实 usage、并行双 functionCall(qwen 不回 id,空 id 双向容忍)、终答正确;new-api 回包携带大量扩展 usage 字段,适配器未知字段容忍实证。剩余缺口收窄为「真实 Google 官方端点逐字节复核」(见 HANDOFF 开放项)。
