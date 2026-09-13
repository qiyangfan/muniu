# ADR 草案(供 kongming 仓库采用):引入 LLM 网关 muniu

> 本文是给 kongming 仓库的 ADR 草稿(T5.2 准备件)。kongming 侧执行 T5.2 时,将本文拷入其
> `docs/adr/00X-introduce-muniu.md`,编号按 kongming 当时顺序调整,并按其 ADR 模板补充上下文。
> 依据:phase-5 T5.2;信任边界引用 kongming ADR-002。

## 状态

草案(待 kongming 侧评审;灰度一周后转「已接受」)

## 背景

kongming 的 agent 面直接持有各上游供应商的 API key(DeepSeek/Moonshot/GLM/Qwen/Doubao),
存在三个持续成本:

1. **密钥分散**:key 明文散落在部署环境的 provider 配置中,轮换/吊销需要逐处操作;
2. **无统一计量**:各上游用量只能到各家控制台分别拉取,无跨供应商的统一按日/按 key/按模型视图;
3. **无统一故障语义**:单上游限流或故障直接影响 agent 可用性,没有既定的换道与冷却策略。

## 决策

在 kongming 与各上游之间引入 **muniu 网关**(github.com/solvela/muniu,单二进制,OpenAI 兼容入站):

- kongming 的 provider `base_url` 指向 muniu(`http://<muniu>:8787/v1`),key 换为 muniu 签发的
  `mk-` key(租户 `kongming` 专用);
- 各上游供应商的真实 key 全部收敛到 muniu 的环境变量,kongming 侧不再持有;
- muniu 提供:多上游路由与 failover(首 token 边界语义)、per-key RPM/TPM 限流、日配额、
  usage 计量落库与成本核算、语义缓存(租户开关,默认关)、管理 API 与控制台。

## 否决清单(备选方案为何不选)

| 备选 | 否决理由 |
|---|---|
| 维持直连(现状) | 上述三项持续成本不消失;多上游故障语义永远缺失 |
| one-api | 无 arm64 官方镜像;无首 token 边界语义;无 conformance 测试集;配额模型与我们的整数 credit 口径不一致 |
| LiteLLM proxy | Python 运行时,与单二进制零依赖运维诉求冲突;代理层性能与内存开销更高 |
| 把网关能力嵌进 kongming | 职责越界:agent 应用不应内嵌基础设施;且无法服务 kongming 之外的调用方 |

## 对信任边界的影响(引用 ADR-002)

ADR-002 确立「上游供应商凭证不出基础设施层,agent 面零凭证」。引入 muniu **反而强化**该边界:

- agent 面从「持有多家供应商 key」退化为「只持有一把可随时吊销的 muniu key」——泄露面从 N 把
  供应商 key 收敛为 1 把网关 key;
- 供应商 key 集中在 muniu 单点,轮换/吊销/审计有一致入口(management CLI/API + 环境变量引用名);
- muniu 与 kongming 同属基础设施信任域,不引入新的跨域信任。

## 灰度与回滚

1. muniu 侧建租户 `kongming` 与专用 key(`scripts/bootstrap-kongming.sh`);
2. 模型别名逐一对齐(`docs/kongming-alignment.md`);
3. 先切非关键数据源一周 → 全量;每日对账(scripts/reconcile.sql,差异 >1% 立案);
4. 回滚路径:kongming `.env` 改回原 provider base_url/key 即恢复直连(muniu 无状态参与,回滚零成本)。

## 后果

- 正面:统一密钥管理、统一计量与成本视图、统一故障语义(首 token 边界 failover)、语义缓存降本;
- 负面:新增一个需运维的常驻进程(单二进制,运维面小);多一跳网络延迟(实测首字节 p50 增量在
  ms 级,见 muniu docs/benchmarks);muniu 自身成为可用性单点(其故障语义与两周生产观察即为该
  风险的缓解证明)。
