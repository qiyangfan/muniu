# ADR-0016:images 生成入站通道与 per-image 计价维度

- 状态:已接受(2026-10-13;二次裁决改判引入——原 backlog「按张计价是新计量维度,不值对价」论证在「作品集完整度优先」框架下反向成立:计量维度演进本身就是资产)

## 决策

muniu 新增 OpenAI 兼容**同步** images 生成入站端点(`POST /v1/images/generations`),作为与 embeddings 同族的「非 chat 族 op 通道」落地:独立 lane handler + `adapter.ImagePreparer` 可选能力接口 + 既有候选循环/failover 判定表(非流式语义零扩格),`dataPlaneMW` 全链复用。计价增设与 token 价目平行的第二维度 `image_pricing`(alias 键空间同源、互不接管;兜底单价 + `size|quality` 档位 JSON;YAML 静态兜底同款回落);计量行 tokens 四列全 0、`cost = n × 档位价`、`estimated=0`(网关价目表是 credit 单源,上游 usage 不参与换算);语义缓存不适用(付费非确定性生成)。预算预检对 images 是**精确拒绝**(成本上界调用前完全可知,无 chat 的 max_tokens 诚实旁路)。付费非幂等生成的换道纪律:失败候选观测行 cost=0,只对交付的 2xx 计费,断流不交付半包(计费歧义登记为限制)。

## 理由

- **同赛道对照**:one-api/LiteLLM 全支持 images 端点,入站矩阵缺它是真实完整度缺口;多模态输入四路已全链,生成侧缺席不对称。
- **架构成本极低**:embeddings 已铺平「非 chat 族 op 通道」的全部抽象(能力接口探测、非流式候选循环、观测行口径),images 是对该抽象的第二次实例化——零新依赖、判定表零扩格。
- **计量叙事资产**:per-image 维度演示「价目 schema 演进 + 追加式迁移 + 计量口径扩展(tokens=0 行、TPM 天然旁路、配额按 credit)」,与 0007 credit 化同族的简历故事。

## 备选与否决

- 在 `model_pricing` 上加可空列:token 通道读取与校验被无意义列污染,否决;平行表追加。
- gpt-image-1 的 token 计价换算:引入按厂商 usage 换算的第二套 credit 来源,与「价目表单源」立场冲突,否决(上游 usage 一律不参与)。
- 异步任务型(dashscope wanx submit+poll):另一套会话语义,登记边界不立项;`edits`/`variations`(multipart)与 audio 同族,二期合并裁决。
- SSO 式集中身份与 gRPC 入站:同日二次裁决维持不引入,论证入 HANDOFF 开放决策项(2026-10-13)。

## 后果

- 迁移 0011(`image_pricing`);`internal/protocol/images.go`、`internal/proxy/images.go`、`ImagePreparer`;管理面 `image-pricing` 三端点;路由表 +1 行进漂移门禁。
- 国内同步兼容端点家数待真上游冒烟实证(硅基流动确定,智谱待核)后回填 provider-presets;实证前以 fixture 测试为准。
- openspec change `image-generation-endpoint`(spec 基线:image-generation 新能力 + pricing-management/request-cost-budget 增补)。
