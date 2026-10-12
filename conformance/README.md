# conformance — 国产协议 fixture 测试集

独立数据资产,网关(adapter 层)是它的消费者。CI 只回放 fixture(打 fake 上游),永不打真上游。

## 目录结构

```
conformance/
├── README.md               本文件
├── schema.json             fixture 结构定义(JSON Schema)
└── <provider>/<scenario>.json
```

## fixture 结构

见 `schema.json`。核心字段:

- `provider` / `scenario`:归属供应商与场景名(文件路径即二者)。
- `source_url`:**必填**,官方文档锚点;每个字段断言可溯源,这是与「拍脑兼容」的分界线。
- `inbound`:OpenAI 形态的入站请求(网关收到什么)。
- `upstream_raw`:脱敏的上游原始响应——`stream` 为 SSE data 载荷序列(不含 `data:` 前缀与空行,按到达序);`body`+`status` 为非流式形态。
- `expect`:归一化断言——`usage`(统一 Usage 形态逐字段)、`events`(流式逐事件 meta:has_content/is_usage/is_done,按序对齐**可见事件**序列:零翻译输出的事件——桥接的 ping/content_block_start(text)/content_block_stop/空 data 心跳——不进入断言序列,兼容型 fixture 全帧可见故与 stream 等长)、`passthrough_lossless`(透传字节与上游字节逐字节等价,缺省 true;桥接型翻译 fixture 恒为 false)。

## 回放方法

`internal/adapter/conformance_test.go` 遍历本目录全部 `*.json`:非流式 fixture 断言 `NormalizeUsage`;流式 fixture 逐事件断言 meta 与 passthrough 字节等价,并归一 usage 帧。新增 quirk 修复前必须先补一个能复现 bug 的 fixture(先红后绿)。

## 直通语料(conformance/<provider>/native/)

`native/` 子目录存放**协议层直读语料**,不属于上节 provider 回放 schema(回放测试只 glob
一层 `*.json`,不会误消费):

- `anthropic/native/`:同构直通语料(protocol-native-passthrough),由
  `internal/adapter/bridge/native_test.go` 消费——断言出站 wire 除 model 值外逐字节等于
  客户端原始体 + 流式事件 meta 对照;`native_in.raw` 为紧凑 JSON 字符串(字节级断言要求)。
- `gemini/native/corpus.json`:Gemini wire 双向语料单源(gemini-native-protocol 决策 1),
  被 `internal/protocol` 两个方向反向消费(入站翻译产 canonical / 出站适配器消费 canonical
  产 wire),回环对拍(出站∘入站=恒等,可翻译子集域)作漂移探测器;含「HTTP 200 + 流内
  错误帧」负面形态(百炼实证的上游失真样式)。

## 贡献规范

1. fixture 来自**官方文档与抓包脱敏**,不入真实 key、不含用户数据;
2. `source_url` 必填,引用官方文档具体页面;
3. 改 fixture 必须同步该 provider 的 quirk 测试与 `docs/design/upstream-adapters.md` 矩阵;
4. 供应商行为存疑时以官方文档为准,实测差异登记为 ⚠ 项另证。
