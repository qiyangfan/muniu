# Design: anthropic-bridge-tool-roundtrip

## 上下文

桥接 `buildRequest` 现状:消息循环仅消费 user/assistant 的纯文本(`contentText`),tool 等角色丢弃;tools 定义与 tool_choice(auto/any/具名)已翻译;响应侧 tool_use→tool_calls 增量翻译完整(既有语料钉死)。`protocol.Message` 结构不携带 tool_calls/tool_call_id(gemini 适配器注释里的同缝),请求侧翻译须走 Raw 信封层解析。

## 目标形态

### 消息循环重写(块级内部形态,发射期保字符串兼容)

- 解析 Raw 信封:`{role, content, tool_calls[]{id,function{name,arguments}}, tool_call_id}`(与 gemini 适配器同缝,Message 结构不扩字段)。
- 内部累积形态升级为 `{role, blocks []aBlock}`;`aBlock` 单结构承载 text/tool_use/tool_result 三类(omitempty 分族序列化)。
- 发射规则:**消息内全部块为 text 块 → content 以字符串发射(多块按 `\n\n` 连接,与既有合并语义逐字节一致);含非 text 块 → content 以块数组发射**。保证纯文本对话出站 wire 零变化(既有 fixture 是守门钉子)。
- 合并规则:连续同角色追加块(纯文本+纯文本退化为字符串拼接路径不变);`role=tool` 先进 pending 缓冲,遇到非 tool 消息或循环结束时任 `flush` 为一条 user 消息(多 tool_result 块);pending user 与后续 user 相邻时按块合并。
- `tool_call_id` 配对:循环中登记已见 tool_use id;tool 消息 id 未登记 → `fmt.Errorf` 报错(归 bad_request,与 tools 形态非法同口径)。
- `tool_use.input`:arguments 为 JSON 字符串,空/`null`/非法 → `{}`;非法 JSON 不报错(上游以对象形态接受,宽松落底与 gemini 双向一致)。

### tool_choice "none"

`{"type":"none"}` 映射加入 switch;**先冒烟后定稿**:dashscope anthropic 分支不接受则删除映射、维持登记限制并修订 spec 条目(证据驱动,禁假性闭环)。

### bridgegate 翻正与防御性保留

- `bridge.ToolRoundtripComplete() → true`(与 gemini 同口径:请求侧翻译完整)。
- `bridgeExempted` 判定式不变(`Bridger ∧ ¬ToolRoundtripComplete`);真实拓扑无 false 实例后,豁免语义由 testkit 假桥接(实现 Bridger + 声明 false)在单元测试钉住——机制删除会让未来桥接型的保守缺省失守。
- `chatToolLoopInflight` 检测逻辑零改动(检测仍服务豁免判定与日志留痕)。

## 权衡

- **扩 protocol.Message 结构 vs Raw 层解析**:选 Raw 层——与 gemini 适配器同缝,避免为单一消费者改公共 DTO;gemini 先例已验证该缝的可维护性。
- **合并进既有消息循环 vs 独立预处理**:选循环内重写——tool 缓冲/块合并与交替约束纠缠在遍历顺序上,拆两遍反而要做中间形态转换。
- **tool_result content 形态**:取 `contentText` 文本串(anthropic tool_result.content 接受字符串);多模态结果块不翻译(与入站 messages 侧 image 失格同哲学,canonical 本就无多模态 tool 载体)。

## 冒烟与验证

- 单元:桥接翻译表驱动(全轮形态/连续合并/未配对拒绝/纯文本逐字节);bridgegate 假桥接钉子;既有全部测试零改动回放(纯文本 wire 不变式)。
- 真上游:temp 实例 + dashscope /apps/anthropic——chat 入站声明 weather 工具 → 上游回 tool_calls → 回传 tool result → 终答;`tool_choice:"none"` 同轮实证。
