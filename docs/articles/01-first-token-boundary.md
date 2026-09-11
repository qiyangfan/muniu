# 首 token 边界:LLM 网关 failover 的正确语义

> muniu 设计复盘系列 ①。对应实现:`internal/proxy/stream.go`、契约文档 `docs/design/proxy-and-streaming.md`。可复现测试:`TestStream_mid_stream_cut_断言出口_流内错误帧加DONE`、`TestFailover_429换道_三断言`(internal/proxy)。

## 问题:换道不是重试那么简单

LLM 网关做 failover(上游 A 挂了换 B)时,流式请求有一个非流式世界没有的麻烦:**响应是逐渐生成的**。如果已经给客户端吐了一半《九歌》赏析,这时候上游断了,悄悄换上游 B 重放请求,客户端会收到两段语义重复的开头——对 Agent 工作流这是事故,不是体验问题。

## muniu 的答案:一条边界线,两种处理

muniu 把流式请求的生命周期用**首 token 边界**切成两个世界:

**边界之前**(尚未向客户端产出任何实质内容帧):

- 什么算「实质内容」?上游 SSE 流中第一个携带 `content` / `reasoning_content` / `tool_call` 增量的帧。`role` 帧、空 delta、usage 终帧都不算——这是与 OpenAI 生态对齐的精确定义,封装在 `protocol.StreamChunk.HasContent()`。
- 此阶段任何失败(dial_fail、首 token 超时、429、5xx、断流)都是**重放安全**的:请求对客户端零可见影响,直接换下一个候选,客户端甚至不知道发生过故障。

**边界之后**(客户端已收到部分生成内容):

- 换道重放必然内容重复/错乱,**禁止**。
- 唯一正确行为是**如实断言**:发一帧流内错误帧,再补发 `data: [DONE]` 让客户端 SDK 干净收尾:

```
data: {"error":{"message":"上游流中断,已生成部分可能不完整","type":"upstream_error","code":"mid_stream_fail","request_id":"01J..."}}

data: [DONE]
```

(与 OpenAI 官方断流行为的差异点:OpenAI 直接断,muniu 多发错误帧与 DONE——已登记 conformance 备注。)

## 判定表:把语义写成可以逐格测试的东西

两个世界的处理规则被固化为一张判定表(`internal/failover/decide.go`),一行一个错误分类,两列(未过/已过首 token):

| ErrorClass | 未过首 token | 已过首 token(流式) |
|---|---|---|
| dial_fail / timeout_first | 换下一候选 | —(不可出现,防御归断言) |
| upstream_5xx | 换下一候选 | 断言 |
| rate_limited_429 | 换下一候选(该 key 记冷却) | 断言 |
| auth_failed | 换下一候选(该 key 记禁用) | 断言 |
| bad_request_400 | **不换道直接返回** | 断言 |
| mid_stream_fail | (补充格:首 token 前断流=零输出,可重放) | 断言 |
| context_canceled | 静默退出 | 静默退出,计量已发生部分 |

注意 400 那一格:参数错误换家也会被拒,换道是徒劳的浪费,所以「不换道直接返回」——错误分类与动作的映射不是直觉,是表格。测试 `TestDecide_判定表` 一格一用例、用例名含坐标,共 26 格全绿。

## 工程细节:三个容易踩的坑

1. **响应头什么时候写给客户端?** muniu 刻意延迟到首内容帧(「开闸」)才写状态与头——此前失败仍可换道;开了闸状态码就被钉死为 200。
2. **帧间超时与首 token 超时要分开计时**,且 `time.Timer.Stop()` 不幂等——用第二次 `Stop()` 的返回值判断「是否超时过」会误判,muniu 用原子标志位(`internal/proxy/stream.go` `firstTokFired`)。
3. **读帧 goroutine 与取消的竞态**:错误投递与 `ctx.Done()` 同时就绪时 select 随机取胜,错误可能被吞。解法:读帧 goroutine 退出时保证 `close(events)`,主循环对 channel 关闭按取消原因归类——绝不悬挂。

## 结论

failover 的难点不在「重试」这个动作,而在**区分哪些状态可以假装什么都没发生**。首 token 边界就是这个分界线的名字:线前重放安全,线后如实断言。把这条线画清楚,判定表、测试、实现就都顺了。
