# Spec Delta

## MODIFIED Requirements

### Requirement: 无状态边界

翻译路径必须 (MUST) NOT 存储 response 对象(别名无原生 responses 候选时,携带 `previous_response_id` 的请求必须 (MUST) 返回 400 `previous_response_id_unsupported` 自解释拒绝——chat 上游无服务端状态,翻译路径无可代理的状态语义)。原生 lane 上:成功响应的 id 必须 (MUST) 记入亲和表(id→provider,进程内 TTL + LRU 上限,集群形态复用共享后端);携带 `previous_response_id` 的请求必须 (MUST) 经粘性路由由亲和 provider 服务,亲和未知时必须 (MUST) 返回自解释错误,禁止 (MUST NOT) 盲发上游;状态请求必须 (MUST) 旁路语义缓存。`GET /v1/responses/{id}` 必须在亲和已知时代理转发亲和 provider 并原样返回,亲和未知时必须 (MUST) 恒返回 404 自解释信封。

#### Scenario: 查询已「创建」的 response

- **WHEN** 完成一次原生 lane 请求后,以其返回的 id 调用 GET
- **THEN** 代理转发亲和 provider,原样返回上游存储的 response 对象(粘性语义见 protocol-native-passthrough 能力)

#### Scenario: 查询未知 id 恒 404

- **WHEN** 以网关从未创建或亲和已丢失的 id 调用 GET
- **THEN** 恒返回 404 not_found 自解释信封(不因曾经代理过而可查)

#### Scenario: 翻译路径维持拒绝

- **WHEN** 携带 `previous_response_id` 的请求发往不含原生 responses 候选的别名
- **THEN** 返回 400 `previous_response_id_unsupported` 自解释信封,不触达上游
