# Design

## D1 通道形态:embeddings 同款「非 chat 族 op 通道」,不进翻译管道

chat 四 lane 的核心资产是「边缘翻译为规范 chat 形态再进管道」;images 没有规范 chat 形态可翻(输入是 prompt 字符串,输出是 b64/url 数组),硬塞翻译管道等于为翻译而翻译。因此复制 embeddings 通道的既有架构:`internal/proxy/images.go` 独立 handler(解析→路由→候选循环→透传→计量),复用 `dataPlaneMW`(鉴权/配额/限流零成本获得)、候选循环、`trackWriter` 提交纪律、判定表(非流式语义,零扩格)。failover 对 images 的可换道性与 embeddings 完全同构:上游 5xx/429/transport 归类照旧。

## D2 出站能力接口:`adapter.ImagePreparer`

与 `EmbeddingPreparer` 同款可选能力接口(`PrepareImages(in, upstreamModel, apiKey) (*Outbound, error)`),proxy 类型断言探测;桥接型适配器(anthropic/gemini 家)不实现 → 400 `images_not_supported` 不换道(参数级问题,任何候选同价无解)。OpenAI 兼容基座实现 = 端点路径 + body 透传(model 原位替换)。零新依赖,不引厂商 SDK(宪法)。

## D3 价目 schema:平行表 `image_pricing`,不动 `model_pricing`

token 价目四列(输入/缓存/输出/时段窗)对 images 全部无意义;在 `model_pricing` 上加可空列会污染 token 通道的读取与校验。平行表追加式迁移(0011):

```sql
CREATE TABLE image_pricing (
  alias           TEXT PRIMARY KEY,   -- 与 token 价目同键空间(模型别名),但互不接管
  currency        TEXT NOT NULL,      -- CNY | USD
  default_credits INTEGER NOT NULL,   -- 兜底单价(credit/张)
  tiers           TEXT NOT NULL DEFAULT '{}', -- JSON {"<size>|<quality>": credit/张}
  updated_at      TEXT NOT NULL       -- RFC3339 UTC
);
```

- **档位键规约**:`size|quality` 完整拼接(如 `1024x1024|hd`),请求缺省字段不猜厂商缺省值——命中不了就走兜底单价,行为可预测优先(spec「档位命中与兜底」场景)。
- **YAML 兜底**:平行配置段 `image_pricing: []`(currency/default 人类可读十进制/tiers),不动既有 `Price` 结构;回落语义、列表来源标注、在线改价原子热替换全部与 `model_pricing` 同款复用管理面机制(仅目录对象平行)。
- **零浮点纪律**:单价人类可读 ≤9 位小数,加载期换算整数 credit;tiers JSON 读取期严格解析,解析失败整目录重建拒绝(宁拒不放行,与 disabled_at 同哲学)。

## D4 计量行与治理语义

- **cost 只在交付置位**:每候选 attempt 的 `meterState` 照常观测(失败候选 status=error 行,可观测性),但 `n×档位价` 只在 2xx 整包交付路径写入 cost;失败/断流候选 cost=0,配额与 credit 零扣减(付费非幂等生成的换道纪律,spec R3)。
- **tokens 四列全 0、estimated=0**:成本由「配置价 × 张数」精确决定,不依赖上游 usage;上游若返回 usage(gpt-image-1 的 token 形态)不参与换算——网关价目表是 credit 单源,与 token 族「计量不信任上游报价」同款立场。
- **TPM 天然不约束**(tokens=0,滑动窗口记 0):按请求数(RPM)与成本(配额/预算)约束 images 流量,语义自洽,无需 TPM 改造。
- **语义缓存不适用**:付费非确定性生成,缓存命中等于把「一次付费」错记为「零成本」;实现上 images handler 不查缓存(比 embeddings 的「缓存依赖通道」更简单)。
- **响应体读上限**:b64 大包,n 可到 10,独立常量 64MiB(不复用 chat 的 `maxNonStreamBody`);超限按断流归类(不交付半包)。请求体上限 4MiB(prompt 文本足够)。

## D5 预算预检:精确形态

chat 预检的诚实旁路(max_tokens 缺失)在 images 不存在——成本上界 `n×有效档位价` 调用前完全可知,预检是**精确拒绝**而非保守估算。预算>0 且 images 价目可得且 `n×价>预算` → 402 `cost_budget_exceeded`(code 复用,零膨胀);价目缺失不拒绝(与「无价目记零」语义一致:成本不可知不拦截)。实现挂候选循环前,与 chat 预检同位。

## D6 明确不做与待验证(v1 边界)

- **异步任务型端点不做**:dashscope wanx 类 submit+poll 形态是另一套会话语义,登记边界;出现真实消费再立项(与 audio 二期同法裁决)。
- **`/v1/images/edits`、`/v1/images/variations` 不做**:multipart 上传管道是独立工作量,与 audio 的 multipart 同族,二期合并考虑。
- **url 回体不做改写**:上游返回 url 形态则原样透出(url 时效归上游,不代理托管文件);`response_format` 透传不注入(注入 = 替调用方做参数决策)。
- **厂商覆盖待验证**:国内同步兼容 images 端点家数(硅基流动确定、智谱待核)按 smoke 纪律真上游实证后回填 `docs/provider-presets.md`;实证前 fixture 测试为准。
- **插件请求体钩子照挂**(与 embeddings 同链序,限流后候选循环前):prompt 同样承载业务载荷,脱敏语义成立。
