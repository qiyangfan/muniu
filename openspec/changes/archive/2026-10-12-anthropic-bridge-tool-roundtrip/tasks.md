# Tasks: anthropic-bridge-tool-roundtrip

## 1. 桥接请求侧翻译

- [x] 1.1 失败测试先行:桥接翻译表驱动(assistant.tool_calls→tool_use 块含 input 对象/role=tool→user tool_result/连续 tool 合并单 user/未配对 id 报错/纯文本 wire 逐字节不变——先红)
- [x] 1.2 buildRequest 消息循环重写:Raw 信封解析、blocks 内部形态、发射期字符串兼容、tool pending 缓冲与配对校验(转绿)
- [x] 1.3 tool_choice `"none"` 映射加入(先按接受实现,冒烟证据定稿)

## 2. bridgegate 翻正与防御性保留

- [x] 2.1 `bridge.ToolRoundtripComplete()` 翻正 true;bridge/README 登记限制解除注记
- [x] 2.2 testkit 假桥接(Bridger+声明 false);bridgegate 单测迁移到替身(豁免跳过/独占信封/无健康度副作用三语义钉住)
- [x] 2.3 既有 chat×anthropic 独占工具多轮 400 测试改写为翻译成功路径;多候选换道测试按替身改写

## 3. 文档与语料

- [x] 3.1 upstream-adapters.md 矩阵 Anthropic 行与 bridge/README 限制段同步;proxy-and-streaming 桥接边界节收口注记
- [x] 3.2 请求侧翻译形态钉死:`TestBridge_Prepare_工具多轮往返` 五子测(含纯文本逐字节金串)——请求侧无 harness 消费面,不为语料而语料(避免双源)

## 4. 真上游冒烟(证据定稿)

- [x] 4.1 dashscope /apps/anthropic:chat 入站工具全轮(声明→tool_calls→tool_result→终答)经翻译路径跑通;`tool_choice:"none"` 实证(400 则删映射+修订 spec 条目)
- [x] 4.2 冒烟资源纪律:临时 key 吊销、实例停机、临时配置删除

## 5. 收口

- [x] 5.1 `make check` 全绿;`openspec validate --all` 通过
- [x] 5.2 HANDOFF 验收记录;`openspec archive` + 主 specs 同步
