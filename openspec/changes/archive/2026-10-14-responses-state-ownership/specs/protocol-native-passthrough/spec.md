# Spec Delta: protocol-native-passthrough — 状态 lane 归属隔离

## MODIFIED Requirements

### Requirement: 有状态粘性路由

携带 `previous_response_id` 的请求必须 (MUST) 仅由原生 lane 服务:别名不含原生 responses 候选时必须 (MUST) 返回 400 `previous_response_id_unsupported` 自解释信封(翻译路径维持既有拒绝,chat 上游无服务端状态)。原生 lane 上必须 (MUST) 经亲和表路由:成功响应的 id 记录 id→provider→**归属 key**(创建请求的鉴权 key 标识,`mk-` 与 `mk2-` 同规;进程内 TTL + LRU 容量上限,集群形态复用共享后端);状态请求的候选域必须 (MUST) 收缩至亲和 provider——域内 key/候选 failover 照常,跨 provider 换道不发生(服务端状态定向单上游的显式语义)。亲和未知(网关重启丢失/非本网关创建)**或归属不匹配(记录的归属 key ≠ 当前请求的鉴权 key)**时必须 (MUST) 返回自解释错误,两类必须 (MUST) 不可区分(同码同文案族,不泄露存在性),禁止 (MUST NOT) 盲发上游;鉴权信息缺失按归属不匹配同语义 fail-closed。状态请求必须 (MUST) 旁路语义缓存检索与入库(上下文在服务端,指纹无意义)。粘性请求数与亲和命中率必须 (MUST) 进指标可观测。

#### Scenario: 粘性路由与域内故障转移

- **WHEN** 携带 `previous_response_id` 的请求命中亲和 provider,其首选 key 处于冷却
- **THEN** 在该 provider 域内取可用 key/候选服务成功,不跨 provider 换道

#### Scenario: 亲和未知自解释

- **WHEN** `previous_response_id` 无亲和记录(网关重启后或非本网关创建)
- **THEN** 返回自解释错误(说明粘性状态丢失,建议改发全量条目),不盲发上游引发 404 换道级联

#### Scenario: 归属不匹配按未知处理

- **WHEN** key A 创建的 response 的 `previous_response_id` 被 key B 携带(记录归属 ≠ 请求 key)
- **THEN** 返回与「亲和未知」不可区分的自解释错误,不触达上游(key B 不得续写 key A 的服务端上下文)

#### Scenario: 状态请求旁路语义缓存

- **WHEN** 携带 `previous_response_id` 的请求到达
- **THEN** 零缓存检索、零缓存入库(无论命中与否),计量与限流照常

### Requirement: 状态 lane 读删代理

`GET /v1/responses/{id}` 与 `DELETE /v1/responses/{id}` 必须 (MUST) 按亲和分域:亲和已知**且归属匹配(记录的归属 key = 当前请求的鉴权 key;鉴权信息缺失按不匹配 fail-closed)**→ 以对应 HTTP 动词代理转发亲和 provider,状态码/响应头/体零改写原样返回;亲和未知**或归属不匹配**→ 恒 404 `response_not_found` 自解释信封(两类不可区分,网关不存储 response、也不存在「对你可见的 response」,不因曾经代理过而可查),**归属不匹配时必须 (MUST) 上游零调用**——DELETE 为不可逆操作,绝不出站删除他 key 状态。出站构造按原生 responses 端点声明(鉴权头注入同创建路径),取 key 失败返回 429 类自解释信封,不得盲发。

#### Scenario: 亲和已知 GET 代理转发

- **WHEN** 亲和表中存在该 response id 且客户端 GET
- **THEN** 以 GET 代理亲和 provider 原样返回上游响应

#### Scenario: 亲和已知 DELETE 代理转发

- **WHEN** 亲和表中存在该 response id 且客户端 DELETE
- **THEN** 以 DELETE 代理亲和 provider 原样返回上游响应(上游执行状态删除)

#### Scenario: 亲和未知恒 404

- **WHEN** 该 response id 无亲和记录(重启丢失/非本网关创建),GET 或 DELETE 到达
- **THEN** 返回 404 `response_not_found` 自解释信封,不触达上游

#### Scenario: 归属不匹配恒 404 且上游零调用

- **WHEN** key A 创建的 response id 被 key B GET 或 DELETE(记录归属 ≠ 请求 key)
- **THEN** 返回与「亲和未知」不可区分的 404 `response_not_found`,上游零调用(B 不得读回 A 的内容,更不得触发 A 状态的不可逆删除)
