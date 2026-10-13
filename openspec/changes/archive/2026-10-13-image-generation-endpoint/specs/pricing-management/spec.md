# Spec Delta

## ADDED Requirements

### Requirement: per-image 价目维度与回落语义

价目管理域必须 (MUST) 增设 per-image 价目形态(与 token 价目平行的第二维度):键空间同为模型别名,值形态为「币种 + 兜底单价 + size|quality 档位表」。来源与回落必须 (MUST) 与 token 价目同款——DB 运行态覆盖整体取代该别名,YAML 静态兜底开箱可用,列表标注来源。价目数值必须 (MUST) 全程零浮点(人类可读十进制 ≤9 位小数,加载期换算整数 credit);档位表必须 (MUST) 以 JSON 存储、读取期严格解析,解析失败整目录重建拒绝(宁拒不放行)。token 价目与 per-image 价目必须 (MUST) 互不接管。

#### Scenario: 覆盖与回落

- **WHEN** 别名 X 写入 images 运行态覆盖(兜底 0.35/张)后删除该覆盖
- **THEN** 生效单价先为 0.35、删除后立即回落 YAML 兜底价(无 YAML 行则无价目,计量记 0)

#### Scenario: 档位命中与兜底

- **WHEN** 档位表含 `"1024x1024|standard": 0.30`,请求 quality=hd
- **THEN** 该请求按兜底单价计费(未命中档位不报错、不拒请求)

#### Scenario: 校验失败整单拒绝

- **WHEN** 写入请求含非法字段(币种非 CNY/USD、单价非十进制或 ≤0、档位键非 `size|quality` 形态、档位 JSON 非法)
- **THEN** 整单拒绝返回 400,不落库不刷新,目录保持原状

### Requirement: per-image 价目管理 API 契约

管理 API 必须 (MUST) 提供 `GET /admin/v1/image-pricing`(按别名升序)、`PUT /admin/v1/image-pricing/{alias}`(幂等覆盖)、`DELETE /admin/v1/image-pricing/{alias}`(撤销运行态覆盖,无覆盖返回 404),经管理面鉴权;在线改价必须 (MUST) 先校验后持久化再原子替换转发侧目录,下一请求生效零重启;脏数据必须 (MUST) 不进热路径(目录重建失败保持原状)。

#### Scenario: 改价下一请求生效

- **WHEN** 管理面成功写入别名 X 的新 images 价目
- **THEN** 后续 images 请求的计量行即按新价计费,服务全程无重启

#### Scenario: API 全流程

- **WHEN** 依次执行 GET → PUT → GET → DELETE → DELETE
- **THEN** 分别得到:yaml 来源条目(或空)→ 200 ok → db 来源条目与新价 → 200 ok → 404
