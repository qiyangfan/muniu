# Design

- **词汇借官方、形态沿本仓**:事件类型用官方 `response.reasoning_summary_text.*`(客户端生态已认识);item done 形态沿用非流式冻结契约(`type=reasoning`+`text` 原文,与官方 summary 数组形态不同)——两处口径差异在 design 登记:本网关 reasoning 语义=「原文透传」,官方=「摘要」。替代方案(发明私有事件类型/砍成 encrypted 占位)均否决:前者客户端不认识,后者丢信息。
- **状态机第三条**:msgOpen/tools 之外新增 reasonOpen+reasonBuf;混流切换对称收束(reasoning→content:closeReasoning 后开 message;content→reasoning:closeMessage 后开 reasoning)。
- **usage 零改动**:reasoning_tokens 明细口径系既有冻结契约。
- **测试先行域(协议适配)**:事件序列全序断言+usage 口径负例(先红后绿);fixture 由测试内嵌剧本承担(与既有 encoder 测试同法)。
