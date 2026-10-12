# protocol-native-passthrough 变更(responses-state-delete-proxy)

## ADDED Requirements

### Requirement: 状态 lane 读删代理

`GET /v1/responses/{id}` 与 `DELETE /v1/responses/{id}` 必须 (MUST) 按亲和分域:亲和已知 → 以对应 HTTP 动词代理转发亲和 provider,状态码/响应头/体零改写原样返回;亲和未知 → 恒 404 `response_not_found` 自解释信封(网关不存储 response,不因曾经代理过而可查)。出站构造按原生 responses 端点声明(鉴权头注入同创建路径),取 key 失败返回 429 类自解释信封,不得盲发。

#### Scenario: 亲和已知 GET 代理转发

- **WHEN** 亲和表中存在该 response id 且客户端 GET
- **THEN** 以 GET 代理亲和 provider 原样返回上游响应

#### Scenario: 亲和已知 DELETE 代理转发

- **WHEN** 亲和表中存在该 response id 且客户端 DELETE
- **THEN** 以 DELETE 代理亲和 provider 原样返回上游响应(上游执行状态删除)

#### Scenario: 亲和未知恒 404

- **WHEN** 该 response id 无亲和记录(重启丢失/非本网关创建),GET 或 DELETE 到达
- **THEN** 返回 404 `response_not_found` 自解释信封,不触达上游
