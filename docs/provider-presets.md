# 官方供应商预设目录(provider-presets)

> **运营参考,不是协议契约**:协议事实以 [upstream-adapters.md](design/upstream-adapters.md) 为唯一权威;
> 本文只登记「官方端点 + 接入形态 + 验证状态」,供拓扑录入(muniu.yaml 或锦官
> `PUT /admin/topology`)时即抄即用,不必逐家去翻官方文档找 base_url。

## 定位与纪律

- **预设 ≠ 适配器矩阵扩张**:兼容型官方家零代码——OpenAI 兼容家走 openai 基座
  (quirk 家用既有 quirk),Anthropic 分支走既有 anthropic 桥接。目录不给 muniu
  增加任何 per-provider 维护面(宪法:不做 provider 军备竞赛)。
- **双目录分工**:控制台与管理 API 的内置预设目录(`GET /admin/v1/provider-presets`,
  单源 `internal/presets`,收录标准见 upstream-adapters「官方端点预设目录」)是本目录的
  经核子集(27 条,含六家 Anthropic 分支),为控制台「供应商」页与复制 YAML 的事实源;
  本文为运营全量清单(含 ⚠ 待实证与 Plan 订阅线红线)。两侧条目冲突时,以真 key 冒烟
  结果为准同批修正双侧,不许分叉。
- **中转站一律不预设**:社区切换工具(cc-switch 等)列表里的 third_party/aggregator
  中转站不进本目录——openai 基座天然兼容任意自填端点,运营要接时在
  `model_allowed_hosts` 出口白名单放行即可,无需登记。
- **核对纪律**:每行标注核对口径;端点经 cc-switch 预设单源
  (farion1231/cc-switch,其条目附各家官方文档核对日期与真 key 探针记录)交叉,
  录入核对日 **2026-09-26**。⚠ = 尚未真 key 冒烟,首次接入该家时跑
  `make smoke PROVIDER=<家>` 实证后再转正。
- **订阅端点红线**:各家的 Plan/Coding 订阅端点与按量端点**分开计费**,混用即
  按量另计费(火山 Agent Plan/Coding Plan、GLM Coding Plan、千帆 v2 系均有
  官方警告)。预设按端点单列,绝不互作候选地址。

## A. OpenAI 兼容官方端点(openai 基座直用;quirk 家标注)

任一 OpenAI SDK / OpenAI 兼容客户端(含 kongming agent 面)可直连,也可经 muniu
中转(获得 key 池/failover/计量计费/限流)。

| 家 | base_url | 形态 | 模型示例 | 备注 |
|---|---|---|---|---|
| OpenAI | `https://api.openai.com/v1` | openai | gpt-5.6-sol | yaml 既有示例 |
| DeepSeek | `https://api.deepseek.com` | deepseek(quirk) | deepseek-v4-flash / deepseek-v4-pro | `/v1` 前缀亦兼容(yaml 示例用 `/v1`);Responses 原生 |
| Kimi(Moonshot 国内) | `https://api.moonshot.cn/v1` | moonshot(quirk) | kimi-k3 | Responses 原生;usage 须 `stream_options.include_usage` |
| Kimi Global | `https://api.moonshot.ai/v1` | moonshot(quirk)⚠ | kimi-k3 | 国际站,Key 与国内不通用 |
| Kimi For Coding | `https://api.kimi.com/coding/v1` | openai ⚠ | kimi-for-coding | 订阅制;Responses 原生 |
| GLM 智谱(国内) | `https://open.bigmodel.cn/api/paas/v4` | glm(quirk) | glm-5.3 / glm-5-turbo | 三端点分立:Coding Plan chat=`/api/coding/paas/v4`、Responses=`/api/v1`,混用不消耗套餐 |
| GLM en(z.ai) | `https://api.z.ai/api/paas/v4` | glm(quirk)⚠ | glm-5.3 | 国际站 |
| Qwen 千问(百炼国内) | `https://dashscope.aliyuncs.com/compatible-mode/v1` | openai | qwen-plus / qwen3.8-max | kongming 主模型现役端点;⚠ responses 原生端点多轮回放有服务端缺口,勿声明 `native_responses`(实证见下) |
| QwenCloud(百炼国际) | `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | openai | qwen3.8-max | Key 与国内不通用 |
| Doubao 火山方舟(按量) | `https://ark.cn-beijing.volces.com/api/v3` | openai | doubao-seed-2-1-pro-260628 | 订阅红线:Coding=`/api/coding/v3`、Agent=`/api/plan/v3`,与按量互斥 |
| BytePlus ModelArk(国际) | `https://ark.ap-southeast.bytepluses.com/api/v3` | openai⚠ | ark-code-latest(订阅 `/api/coding/v3`) | |
| MiniMax(国内) | `https://api.minimaxi.com/v1` | openai⚠ | MiniMax-M2 / M3 | 域名双源:官方文档为 minimaxi.com,cc-switch 预设为 minimax.cn,以真 key 冒烟归一;国际站 `https://api.minimax.io/v1`;Responses 原生 |
| StepFun 阶跃(按量) | `https://api.stepfun.com/v1` | openai | step-3.7-flash | 订阅 Step Plan=`https://api.stepfun.com/step_plan/v1`(chat);国际站 stepfun.ai 同构 |
| 百度千帆(按量) | `https://qianfan.baidubce.com/v2` | openai | deepseek-v4-pro | 订阅:`/v2/coding`、`/v2/tokenplan/personal`,专属 Key 不互通 |
| 腾讯混元 TokenHub(按量) | `https://tokenhub.tencentmaas.com/v1` | openai⚠ | hy3 / hy4-preview | 备用域 `.tencentmaas.cn`;订阅 Token Plan=`https://api.lkeap.cloud.tencent.com/plan/v3`(intl=`tokenhub-intl.tencentcloudmaas.com/plan/v3`),两条产品线 Key 不互通用 |
| Xiaomi MiMo | `https://api.xiaomimimo.com/v1` | openai⚠ | mimo-v2.6-pro | 订阅:`https://token-plan-cn.xiaomimimo.com/v1` |
| LongCat(美团) | `https://api.longcat.chat/openai/v1` | openai⚠ | LongCat-2.0 | Responses 原生 |
| Astron(讯飞) | `https://maas-coding-api.cn-huabei-1.xf-yun.com/v1` | openai⚠ | — | 订阅制 Coding Plan |
| BaiLing(蚂蚁) | `https://api.ant-ling.com/v1` | openai⚠ | — | |
| xAI Grok | `https://api.x.ai/v1` | openai | grok-4.5 | 另有 OAuth 托管形态(网关场景不适用) |
| Gemini(官方兼容层) | `https://generativelanguage.googleapis.com/v1beta/openai` | openai⚠ | gemini-flash | 协议矩阵裁决:走兼容层而非原生桥接;冒烟待实证 |

## B. Anthropic 分支官方端点(经 anthropic 桥接组合)

背景:国内主流厂商几乎都提供**官方 Anthropic Messages 兼容分支**(Claude Code
生态带动),且大量优质编码模型装在订阅制 Coding Plan 里,价格远低于按量。muniu
的消费路径 = provider 条目 `adapter: anthropic` + 下方 base_url,入站侧 OpenAI
客户端(如 kongming)零改动。

| 家 | 分支 base_url | 产品线 |
|---|---|---|
| Anthropic(官方) | `https://api.anthropic.com` | 按量(Claude 官方) |
| DeepSeek | `https://api.deepseek.com/anthropic` | 按量 |
| Kimi(Moonshot) | `https://api.moonshot.cn/anthropic` | 按量;intl=`api.moonshot.ai/anthropic` |
| Kimi For Coding | `https://api.kimi.com/coding/` | 订阅;intl=`api.kimi.ai/coding/` |
| GLM 智谱 | `https://open.bigmodel.cn/api/anthropic` | Coding Plan;intl=`api.z.ai/api/anthropic` |
| Qwen 千问 | `https://dashscope.aliyuncs.com/apps/anthropic` | 百炼;Coding=`coding.dashscope.aliyuncs.com/apps/anthropic`;intl=`dashscope-intl.aliyuncs.com/apps/anthropic` |
| Doubao 火山 | `https://ark.cn-beijing.volces.com/api/compatible` | 按量(Doubao Seed);订阅 Coding=`/api/coding`、Agent=`/api/plan` |
| BytePlus(国际) | `https://ark.ap-southeast.bytepluses.com/api/coding` | 订阅 |
| 百度千帆 | `https://qianfan.baidubce.com/anthropic/coding` | 订阅 Coding;Token Plan=`/anthropic/tokenplan/personal` |
| 腾讯 Token Plan | `https://api.lkeap.cloud.tencent.com/plan/anthropic` | 订阅;intl=`tokenhub-intl.tencentcloudmaas.com/plan/anthropic`;企业=`tokenhub.tencentmaas.com/plan/anthropic` |
| StepFun | `https://api.stepfun.com/step_plan` | 订阅 Step Plan;intl=`api.stepfun.ai/step_plan` |
| MiniMax | `https://api.minimaxi.com/anthropic` | 订阅;intl=`api.minimax.io/anthropic`;域名口径同表 A |
| LongCat | `https://api.longcat.chat/anthropic` | |
| Xiaomi MiMo | `https://api.xiaomimimo.com/anthropic` | 订阅 Token Plan=`token-plan-cn.xiaomimimo.com/anthropic` |
| BaiLing(蚂蚁) | `https://api.ant-ling.com/anthropic` | |

**鉴权形态(声明制已落地,接入前仍须冒烟)**:官方 Anthropic 用 `x-api-key`
(Bearer 仅留给 OAuth,对其发 Bearer 形态 API key 即 401);国内分支普遍走
`Authorization: Bearer`(Claude Code 的 `ANTHROPIC_AUTH_TOKEN` 形态)。muniu 桥接
现为声明制:provider 条目 `auth: bearer` 即发 Bearer,缺省发 x-api-key——B 表各分支
按其官方文档口径声明为 bearer(代码目录 `internal/presets` 同批)。⚠ 各分支实际
接受头未逐家真 key 冒烟,首次接入跑 `make smoke`(配 `MUNIU_SMOKE_AUTH=bearer`)
实证后再转正。

## C. 聚合平台与本地推理(「任意 OpenAI 兼容端点」样例)

以下非模型厂商,但属官方运营的推理平台,openai 基座直用,作端点样例收录:

| 平台 | base_url | 备注 |
|---|---|---|
| SiliconFlow 硅基流动 | `https://api.siliconflow.cn/v1` | 国际站 `api.siliconflow.com/v1` |
| ModelScope 魔搭 | `https://api-inference.modelscope.cn/v1` | 模型 id 带组织前缀(如 `ZhipuAI/GLM-5.2`) |
| NVIDIA NIM | `https://integrate.api.nvidia.com/v1` | 同上 |
| OpenRouter | `https://openrouter.ai/api/v1` | |
| Ollama(本地) | `http://127.0.0.1:11434/v1` | 冒烟已入名册(`PROVIDER=ollama`) |
| vLLM / SGLang 自托管 | `http://<host>/v1` | 同上 |

### responses 原生端点可用性实证(`native_responses` 声明依据;ADR-0014 声明制)

| 家 | responses 原生端点 | 实证状态(2026-10-11) | 声明建议 |
|---|---|---|---|
| OpenAI 官方 | `https://api.openai.com/v1`( Responses API 发源地) | 未直测(大陆不可达);官方契约完整 | 可声明(官方文档背书) |
| Qwen 百炼(dashscope) | `/api/v2/services/agents`(独立端点,非 compatible-mode) | **多轮工具回放四路径全 400**(服务端把 `function_call_output` 译成 `role:'tool'` 被自家校验器拒;kongming e2e 四路径对照实证) | **勿声明**;上游修复后翻开关即切,零代码改动 |
| 其余 openai 系 | 逐家未实证 | 「OpenAI 兼容」≠「responses 端点可用」 | 未实证前勿声明;首次接入按 `make smoke` responses 场景实证后声明 |

声明制语义:未声明候选对 responses 入站走既有 chat 翻译兜底(行为与本注记前逐字节一致);声明仅解锁直通车道的路由资格,不改变任何翻译路径行为。

## D. 需要新协议工作的家(backlog,目录不承诺)

| 家 | 差异 | 评估 |
|---|---|---|
| Azure OpenAI | deployment 路径 + `api-key` 头 + `api-version` query | OpenAI 形态变体,可能 quirk 级可表达;出现真实消费即评估 |
| AWS Bedrock | sigv4 签名 + 自家事件流协议 | 真桥接工作量;出现真实消费再补 ADR |
| GitHub Copilot | OAuth 设备流 + 特化端点 | agent-CP 特化,网关场景弱需求 |

## E. 拓扑配置样例

```yaml
providers:
  - name: deepseek
    adapter: deepseek                  # quirk 家(表 A 标注)
    base_url: "https://api.deepseek.com/v1"
    keys:
      - env: DEEPSEEK_API_KEY_1       # 只写环境变量名,绝不写明文
    weight: 70
    priority: 0
  - name: minimax
    adapter: openai                   # 兼容家全部 openai 基座(表 A)
    base_url: "https://api.minimaxi.com/v1"
    keys:
      - env: MINIMAX_API_KEY_1
    weight: 30
    priority: 1
  # anthropic 分支(表 B):⚠ 见「鉴权头形态缺口」,未冒烟前勿上生产
  # - name: kimi-coding
  #   adapter: anthropic
  #   base_url: "https://api.kimi.com/coding/"
  #   keys:
  #     - env: KIMI_CODING_KEY_1

model_routes:                         # 出站别名 = 入站 model 字段
  deepseek-chat:
    - { provider: deepseek, model: deepseek-v4-flash, priority: 0 }
  minimax-m3:
    - { provider: minimax, model: MiniMax-M3, priority: 0 }
```

出口白名单:`model_allowed_hosts` 须包含所接各家的主机名(缺省只含主模型主机)。

## F. 真上游冒烟路径

```bash
MUNIU_SMOKE_KEY=<真实key> MUNIU_SMOKE_BASE_URL=<表A/B端点> \
  MUNIU_SMOKE_PROVIDER=<适配器形态:openai|deepseek|moonshot|glm|anthropic> \
  MUNIU_SMOKE_MODEL=<模型示例> make smoke PROVIDER=<家>
```

`PROVIDER` 只选测试入口(`TestSmoke_<家>`),适配器形态由 `MUNIU_SMOKE_PROVIDER`
决定(缺省 openai)。anthropic 桥接家的冒烟入口随鉴权头缺口修复后补齐。

## 数据来源与维护边界

- 端点核对:cc-switch 预设单源(farion1231/cc-switch `src/config/*ProviderPresets.ts`,
  条目附各家官方文档链接、核对日期与真 key 探针记录),交叉本仓协议矩阵;录入日 2026-09-26。
- 维护边界:目录只登记官方端点与验证状态,**不承诺逐家长期跟进**;端点漂移由真实
  接入(冒烟/对账告警)驱动修正,与适配矩阵的演进纪律一致。

## 同步图片生成的验证边界

网关已提供 OpenAI 兼容 `POST /v1/images/generations`，路由与价目需要显式配置。硅基流动、智谱等同步生成端点的真实 wire 与厂商计费均未在本批冒烟实证，不把 chat 预设的可用性等同于 images 已验证。当前证明集为假上游与协议保真测试；真实凭据到位后按手动 smoke 纪律验收。dashscope wanx 等异步任务型端点不在本通道。
