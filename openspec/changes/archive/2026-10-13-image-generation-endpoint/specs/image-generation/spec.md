# Spec Delta

## Purpose

images 生成入站通道的行为契约:OpenAI 兼容同步 `/v1/images/generations` 经多候选出站,per-image 计价与计量行口径,付费非幂等生成的换道计费纪律。audio 生成(二进制流/multipart)不在本能力域。

## ADDED Requirements

### Requirement: 生成通道与候选循环

系统必须 (MUST) 提供 `POST /v1/images/generations` 入站端点(OpenAI 兼容同步形态),经数据面全链(key 鉴权、配额、限流)后按模型别名路由候选;候选失败按既有 failover 判定表(非流式语义)换道。请求必须 (MUST) 校验 OpenAI images 形态(model/prompt 必填),不合法请求以 400 拒绝且上游零调用。出站适配器不具备 images 能力时必须 (MUST) 返回 400 `images_not_supported` 且不换道(参数级问题,换道无解)。响应必须 (MUST) 为上游 2xx 整包回写,不做流式变形。

#### Scenario: 正常生成全链

- **WHEN** 以合法 key 对已路由别名发起合法 images 请求,首个候选返回 2xx
- **THEN** 客户端收到该 2xx 响应体,计量行落账(口径见 per-image 计量行要求)

#### Scenario: 适配器无 images 能力

- **WHEN** 候选供应商的适配器未实现 images 出站能力
- **THEN** 返回 400 `images_not_supported`,不触发候选换道

#### Scenario: 上游 5xx 换道

- **WHEN** 首个候选返回 5xx,次候选返回 2xx
- **THEN** 客户端收到次候选的 2xx 响应,仅按交付结果计量(见换道计费纪律要求)

#### Scenario: 别名不存在

- **WHEN** 请求的模型别名未配置路由
- **THEN** 返回 400 `model_not_found`,上游零调用

### Requirement: per-image 计量行口径

images 请求的计量行必须 (MUST) 以张数为计量维度:token 四列(prompt/completion/reasoning/cached)全为 0,`cost = n × 有效档位价`(credit 整数),`estimated=0`(网关价目表是 credit 单源,成本由配置价与张数精确决定,非对上游 usage 的估算)。档位价必须 (MUST) 按「size|quality 组合 > 兜底单价」解析;无任何价目时成本记 0(与 token 族回落语义同款,可观测不阻断)。上游返回的 usage 字段(如存在)必须 (MUST) NOT 参与计费换算。语义缓存必须 (MUST) NOT 作用于本通道(付费非确定性生成,命中即错误)。

#### Scenario: 档位计费

- **WHEN** n=2、size/quality 命中档位价 100 credit/张
- **THEN** 计量行 cost=200,token 四列为 0,estimated=0

#### Scenario: 档位未命中走兜底

- **WHEN** 请求的 size/quality 组合在价目档位表外,兜底单价 80 credit
- **THEN** 计量行按 80 credit/张计费

#### Scenario: 无价目记零不阻断

- **WHEN** 别名无任何 images 价目
- **THEN** 请求正常转发,计量行 cost=0

### Requirement: 付费生成的换道计费纪律

非幂等付费生成必须 (MUST) 只对最终交付的 2xx 结果计费:失败候选(非 2xx、构造失败、断流)的计量行 cost 必须 (MUST) 为 0——可观测(status=error 观测行,与既有通道同款)但零 credit 扣减、零配额扣减;全部候选失败时按既有耗尽出口回错误信封,整请求零计费。响应中途断流(上游计费歧义)必须 (MUST) 不向客户交付半包,该歧义侧登记为限制(断流候选不冒认成本)。

#### Scenario: 失败候选零计费

- **WHEN** 候选 A 返回 500、候选 B 返回 2xx
- **THEN** 候选 A 落 status=error 且 cost=0 的观测行,交付行(候选 B)cost=n×档位价,key 配额只扣交付成本

#### Scenario: 全候选失败零计费

- **WHEN** 全部候选均失败
- **THEN** 返回既有 `all_candidates_failed` 信封,所有计量行 cost=0,配额零扣减

#### Scenario: 响应中途断流不交付半包

- **WHEN** 上游 2xx 后响应体读取中途断流
- **THEN** 客户端不收到不完整响应体(换道重试或耗尽出口),该断流候选计量行 cost=0
