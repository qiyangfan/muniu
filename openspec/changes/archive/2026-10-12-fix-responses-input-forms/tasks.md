# Tasks

## 1. 解析层修复(测试先行域)

- [x] 1.1 先写失败测试:①`{"role":"user","content":"你好"}`(省略 type + 字符串 content,官方 SDK 缺省形态)应翻译成功;②显式 type + 字符串 content 应翻译成功;③同语义快捷形态与显式形态 canonical 逐字节一致(对拍);④省略 type 且无 role → `input_invalid` 维持;⑤content 为数字/null → `input_invalid` 维持;⑥既有显式数组形态与危险清单(previous_response_id/background/未知 item 类型)回放零改动;`go test ./internal/protocol/` 可运行
- [x] 1.2 实现 `respItem` 解析扩展(Content 改 `json.RawMessage` 二态:字符串→单 text 部件折叠、数组→现行路径、其他→现行拒绝;type 缺省按 role 判定,空 role 维持 `input_invalid`),错误码与信封形态不变;`go test ./internal/protocol/ -race` 全绿

## 2. 回归与文档

- [x] 2.1 proxy 层全量回归:responses e2e、管道等价性、跨协议缓存既有测试零改动回放;`go test ./internal/proxy/ -race` 全绿
- [x] 2.2 文档落地:proxy-and-streaming.md「Responses 入站翻译」映射表补两种快捷形态(官方 SDK 缺省形态注记)+ 百炼多轮回放服务端缺口知识注记(含「kongming 绕行无须回滚」结论);`make check` 全绿,验收证据(命令+输出)贴 HANDOFF 验收记录区

## Workflow follow-up

- kongming 侧两文件绕行改动是否随其 12 change 批次搭车提交,由 owner 决定(跨仓,本仓不动)。
- 真机复核(可选):按 kongming e2e 复现配方以官方 SDK 缺省形态直发 muniu `/v1/responses`,确认 ReAct 两轮往返收敛;证据补录 HANDOFF。
- 归档前按 openspec 纪律同步主 specs(responses-inbound 增补),由 owner 指令触发 archive 流程。
