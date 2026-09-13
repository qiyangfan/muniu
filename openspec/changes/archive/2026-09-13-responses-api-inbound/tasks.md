# 任务清单:Responses API 入站 v1

> 前置:`upstream-egress-batch` 已完成(桥接工具边界契约在位,工具多轮错误语义可援引)。
> 纪律:协议适配为测试先行域——翻译映射表先冻结、fixture 先行,先红后绿;每任务收尾 `make check` 全绿。

## 1. 契约与测试先行

- [x] 1.1 映射表冻结:`docs/design/proxy-and-streaming.md` 增「Responses 入站翻译」节(请求映射表/显式拒绝清单/事件集与顺序不变式/忽略字段白名单)
- [x] 1.2 表驱动翻译测试先红:合法映射逐格断言/`previous_response_id` 400/`background` 400/不支持 item 类型 400/非法 items 结构 400
- [x] 1.3 conformance fixtures:请求翻译对照、流式事件全序列(顺序不变式)、非流式对象形态

## 2. 实现

- [x] 2.1 `internal/protocol`:Responses DTO + 双向翻译纯函数(请求翻译/响应对象构造/流式事件映射,流式复用既有 SSE 解析与usage 抽取)
- [x] 2.2 handler + 路由 + 装配:`POST /v1/responses` 汇入管道同一入口(鉴权→配额→限流→翻译→管道);`GET /v1/responses/{id}` 恒 404 not_found 信封;openapi.yaml + api-types 同步(漂移门禁)

## 3. 端到端与收尾

- [x] 3.1 钉子测试:非流式/流式/函数调用往返/usage 计量与等价 chat 一致/跨协议缓存命中(等价 chat 先写、Responses 命中)/换道重治理等价
- [x] 3.2 真上游冒烟实录(任一国产上游,流式 + 非流式)+ README 功能表 + make check 全绿(vet + lint 0 issues + web + 全包 race),证据贴 HANDOFF
