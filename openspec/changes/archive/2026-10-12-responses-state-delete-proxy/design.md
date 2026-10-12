# Design: responses-state-delete-proxy

- `NativeFetcher.PrepareNativeFetch(respID, apiKey)` → `PrepareNativeFetch(verb, respID, apiKey)`(GET/DELETE 共用出站构造,接口内部、两处调用点同批);openai 实现按动词出站,无请求体。
- `HandlerResponsesStateless` 泛化:按 `r.Method` 取动词,余下逻辑(亲和分域/404/代理透传)与 GET 完全共用——不新建 handler,单一入口天然保持两动词行为一致。
- 路由 `DELETE /v1/responses/{id}` 进 routeEntry 表(漂移门禁同源),openapi.yaml 同步;数据面链(dataPlaneMW)与 GET 一致。
- 非目标:上游不支持 DELETE 时原样透传其 404/405(代理语义,不做协议适配);亲和表清除时机不绑定 DELETE 成败(上游删除后 id 亲和自然失效,表项由 TTL/LRU 淘汰)。
