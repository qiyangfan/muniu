# Design: per-request-cost-budget

## D1 预算宿主:per-key 列,非 per-request header

单请求成本上界与 rpm/tpm/日配额同族(key 治理面字段,管理面统一裁量),挂 `api_keys` 列;per-request header 形态(调用方自带预算)留待真实多租户需求出现再立项。`KeyInfo` 增 `MaxRequestCostCredits`(0=不限),随鉴权链路进热路径;预算 0 时全部检查点旁路,零开销。

## D2 两道闸的分工与诚实边界

- **预检闸**(非流式唯一闸):条件 = 预算>0 ∧ 价目可得 ∧ `max_tokens`>0。估算 = 估算输入×输入价 + max_tokens×输出价(保守取整,缓存折扣不计——不低估)。`max_tokens` 缺失时上界不可知,预检不拒绝(不伪保护,spec 如实登记);流式请求由中断闸兜底。
- **流中闸**(流式专用):累计成本 = 输入(usage 帧真值到达即用,否则预检同款估算)+ 输出(delta 增量逐帧计数——逐帧计数比整文重计有轻微保守高估,预算场景可接受;usage 帧的累计 output_tokens 到达即覆盖为真值)。
- **非流式无中断闸**:上游非流式响应原子生成,中断只省客户端传输不省上游账单,做了就是伪保护——如实登记为限制。
- **embeddings 端点不进本变更**:输出形态不同(无 max_tokens、价目结构不同),登记后续独立立项。

## D3 拒绝/中断的信封语义

- 复用 `TypeQuotaExceeded`(402)——成本拒绝与配额超限同属支付族;错误类型表零膨胀,新 code `cost_budget_exceeded`(各功能自带 code 的先例一致)。
- 流中已开闸:复用断言出口形态(流内错误帧 + 干净收尾),客户端已收部分保留;message 如实说明「已生成部分保留,请求因超预算中断」。

## D4 failover 判定表扩格:网关策略类

`cost_budget_exceeded` 新错误类:与 `context_canceled` 同族的「非上游质量」类——`Decide` 映射静默终结(不换道:同别名跨候选价目相同,换道无解),`reportKeyOutcome` 不回写(不冷却不禁用不记 EMA);连续中断后 key 池保持 healthy。

## D5 计量口径

- 预检拒绝:**不落计量行**(与 strict 治理 400 / adapter_missing 早退同族,上游零调用零成本)。
- 流中中断:落行,`status=error`、`error_class=cost_budget_exceeded`,usage 按已发生部分如实记录(usage 帧有真值用真值,否则估算置位)。
- 语义缓存命中不进闸(命中成本为 0,且命中路径先于候选循环)。

## D6 直通与失格请求

翻译失格请求(canonical 不可得)旁路预检——直通语义字节保真优先,无 canonical 无从估算;流中闸仍生效(usage 帧真值路径不依赖 canonical)。native 直通流的 delta 文本非 OpenAI chunk 形态,估算累计自然为空,预算判定落在 usage 帧真值上(anthropic 系 usage 帧分帧到达)。
