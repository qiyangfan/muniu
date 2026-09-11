# 木牛 Muniu

> **The model supply line for AI applications.**
> 木牛流马,蜀道粮车 —— 给 AI 应用运模型的网关。

**状态:施工中(骨架已立,核心开发按 docs/ROADMAP.md 六阶段推进)。**
在 v0.1.0 发布前,本仓库不承诺任何 API 稳定性;设计契约见 [docs/design/](docs/design/)。

## 它是什么

muniu 是一个**单二进制、OpenAI 兼容入站**的 LLM 网关,用 Go 写成。它站在你的 AI 应用与各家模型供应商之间,把「换模型、管密钥、控成本、扛故障」这些脏活收进一条粮道:

```
 你的应用(kongming / 任意 OpenAI SDK)
        │  Authorization: Bearer mk-xxxx   (muniu 签发的 key)
        ▼
┌─────────────────────────────────────────────┐
│                  muniu 网关                  │
│  鉴权 → 租户/配额 → 限流 → 路由 → 流式代理     │
│         │            │           │          │
│    key 池调度   failover 语义   usage 计量计费 │
└───────┬─────────────┬─────────────┬──────────┘
        ▼             ▼             ▼
   DeepSeek       OpenAI/官方     Anthropic …
   Moonshot       兼容端点        Gemini(协议桥接)
   GLM / Qwen / Doubao
```

一句话:**接入一次,后面随便换粮仓。**

## 为什么再造一个网关

这个赛道不缺玩家(one-api / LiteLLM / Bifrost / agentgateway 各有山头),muniu 不做功能面军备竞赛,只押三个现有网关都没做扎实(或对中文生态是二等公民)的切角:

1. **国产模型协议兼容性测试集(conformance)**:DeepSeek / Moonshot / GLM / Qwen / Doubao 的流式形态、usage 字段、reasoning token、错误码差异,以可回放的 fixture 测试集形式固化——测试集本身是独立资产,网关是它的第一个消费者。
2. **Agent 流式语义**:failover 的「首 token 边界」语义(首 token 前透明换道、首 token 后如实断言)、流式下的 usage 归一与估算、客户端断连向上下游的取消传导——为 Agent 工作流而设计,不是聊天补全的赠品。
3. **单二进制 sidecar 形态**:`CGO_ENABLED=0` 纯静态编译,嵌入式 SQLite(WAL),`muniu` 一条命令起服,零 compose、零外部数据库依赖。静态拓扑走 YAML(git 可审计),运行态走内嵌库。

## 功能路线

| 阶段 | 交付 | 版本 | 状态 |
|---|---|---|---|
| Phase 0 | 工具链与仓库初始化 | — | ✅ |
| Phase 1 | 单上游端到端流式透传(纵切面) | — | ✅ |
| Phase 2 | MVP:多上游适配器 + apikey + 计量计费 + RPM 限流 | v0.1.0 | 🔧 进行中 |
| Phase 3 | 流量治理:key 池 / failover / TPM / 配额 / 多租户 | v0.2.0 | 🔧 进行中(代码与验证完成) |
| Phase 4 | 语义缓存 + 管理 API + React 控制台 | v0.3.0 | ⏳ |
| Phase 5 | 生产集成 + 压测报告 + 设计文档系列 | v1.0.0 | ⏳ |

详细判据与任务分解见 [docs/ROADMAP.md](docs/ROADMAP.md) 与 [docs/plans/](docs/plans/)。

## 快速开始

```bash
# 1. 配置:复制示例,按需改 provider 与价目(key 走环境变量,绝不写进 YAML)
cp muniu.example.yaml muniu.yaml
export DEEPSEEK_API_KEY_1=sk-...

# 2. 签一把访问 key(明文仅此一次展示)
make build
./bin/muniu keys create --name my-app        # → mk-xxxx

# 3. 起服(默认 :8787)并调用
./bin/muniu
curl http://127.0.0.1:8787/v1/chat/completions \
  -H "Authorization: Bearer mk-xxxx" \
  -d '{"model":"deepseek-chat","stream":true,"messages":[{"role":"user","content":"你好"}]}'
```

本地无供应商 key 时,任何 OpenAI 兼容端点(如 [ollama](https://ollama.com))都可直接当上游:
`adapter: openai` + `base_url: "http://127.0.0.1:11434/v1"`。

## 控制台与语义缓存(v0.3.0)

设置 `MUNIU_ADMIN_TOKEN` 后启动,浏览器打开 `http://127.0.0.1:8787/console/` 即进入内置控制台
(单二进制自带,无需额外部署):总览(用量/成本/缓存命中图表)· 密钥管理 · 用量明细 · 供应商健康。

**语义缓存**(`semantic:` 配置,租户级开关默认关):同义问题直接返回缓存的参考答案,省 token 降延迟。
采用「宁缺勿错」保守策略:temperature ≤ 0.3、`finish_reason=stop`、无工具调用、输入 ≤8K tokens 才准入;
相似度阈值 0.97、TTL 24h、LRU 容量上限;**缓存的是相似问题的一次参考答案**,不做主动失效。
命中行照记 `usage_logs`(`provider=semantic-cache`,cost 0),命中率进控制台首屏。

## 压测数字(同机假上游,SSE;对照组 one-api v0.6.10 官方 arm64 二进制;详见 docs/benchmarks/2026-09-10.md)

| 并发 | 产品 | 成功率 | 首字节 p50 | 吞吐 |
|---|---|---|---|---|
| 100 | muniu | 100% | 1.15ms | 1,444 rps |
| 100 | one-api | 100% | 8.98ms | 255 rps |
| 1000 | muniu | 100% | 17.28ms | 38,498 rps |
| 1000 | one-api | 100% | 1,748ms | 456 rps |
| 5000 | muniu | 100% | 81.96ms | 23,477 rps |
| 5000 | one-api | 99.8% | 9,760ms | 454 rps |

单流转发增量开销 6μs/chunk(BenchmarkSSEForward,1000 事件)。

## 设计复盘系列(可对外发的工程文章)

1. [首 token 边界:LLM 网关 failover 的正确语义](docs/articles/01-first-token-boundary.md)
2. [流式 usage 计量:各家差异与估算兜底](docs/articles/02-streaming-usage-metering.md)
3. [conformance:把「兼容」变成测试集](docs/articles/03-conformance-as-asset.md)
4. [单二进制取舍:纯 Go SQLite 与零外联](docs/articles/04-single-binary-tradeoffs.md)

## 核心语义

**failover 的「首 token 边界」**(docs/design/proxy-and-streaming.md):
请求对客户端产出第一个内容帧之前,任何失败都可透明换道重放(换 provider、换 key);
之后客户端已收到部分生成内容,重放必然造成内容重复——唯一正确行为是**如实断言**:
发流内错误帧 + `data: [DONE]` 干净收尾,并按已转发部分估算计量。

**key 池三态**:健康 → 冷却(429 按 Retry-After 指数退避上限 30min;5xx 连续 3 次冷却 30s)→ 到期自动恢复;
401/403 直接禁用(人工介入级)。健康度 = 最近 20 次结果 EMA 成功率,有效权重 = weight × max(0.1, 成功率),防饿死。

**配额一致性 FAQ**:为什么偶发小额超限?配额与 TPM 均为**事后记账**模型——请求进行时无法预知最终
token 数,muniu 的取舍是「宁可记账精确、偶发小额超限」,不做预留式伪精确(预留复杂度与 token
上限的不可知性不成比例)。

## 文档地图

| 文档 | 内容 |
|---|---|
| [docs/ROADMAP.md](docs/ROADMAP.md) | 六阶段路线、验收闸门、backlog |
| [docs/design/](docs/design/) | 架构、流式与 failover 语义、适配器矩阵、流量治理、计量、缓存、存储与配置 |
| [docs/adr/](docs/adr/) | 关键选型决策(为什么) |
| [docs/plans/](docs/plans/) | 各阶段可执行任务清单(执行者按此施工) |
| [docs/conventions.md](docs/conventions.md) | 工程约定(代码/测试/错误/日志/迁移) |
| [AGENTS.md](AGENTS.md) | AI 执行者工作契约(铁律在此) |

## License

[Apache-2.0](LICENSE)。

## 致谢

设计受 [one-api](https://github.com/songquanpeng/one-api)、[LiteLLM](https://github.com/BerriAI/litellm)、[Bifrost](https://github.com/maximhq/bifrost) 等先行的启发;参考其逻辑,未复制其代码。
