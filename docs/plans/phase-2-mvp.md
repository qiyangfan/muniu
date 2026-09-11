# Phase 2:MVP(v0.1.0)

> 状态:代码与验证完成(2026-09-10;tag v0.1.0 待用户指令) · 前置:Phase 1 闸门全绿 · 版本:**v0.1.0** · 允许新增依赖:`modernc.org/sqlite`、`golang.org/x/time/rate`、`github.com/pkoukk/tiktoken-go` + `github.com/pkoukk/tiktoken-go-loader`(离线词表 embed)

## 目标

网关「可用」:多供应商(OpenAI 兼容基座 + 三家国产 quirk)、apikey 签发与鉴权、usage 计量落库与计价、RPM 限流、统一错误信封;可在本机替 kongming 扛灰度流量。

## 任务清单

- [x] **T2.1 store**:`internal/store`:打开 SQLite(`modernc`,DSN 带 `_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)`,目录自动创建)→ embed 迁移执行器(`migrations/0001_init.sql` **逐字按 data-and-config 契约 DDL**,含 default 租户种子,id 用 UUIDv7);repo 方法:`GetAPIKeyByHash`、`InsertUsageBatch(tx)`、`SumCostToday(tenant/key)`。测试:临时文件库上迁移幂等(跑两次不报错)、种子存在。
- [x] **T2.2 apikey 签发与鉴权(测试先行)**:`internal/auth`:生成 `mk-` + 32 字节 `crypto/rand` 的 base62 → sha256 hex 存 `key_hash`,`prefix`=明文前 12 字符;校验中间件:解析 Bearer → hash → 查缓存(进程内 `map+mutex+过期 10s`,容量上限 10k 随机淘汰,**不引 LRU 库**)→ 未命中查库 → 吊销/禁用 → `auth_failed`。管理走**单二进制子命令**(Phase 4 才有 API):`muniu keys create --tenant default --name kongming [--rpm 120]` 打印明文一次、`muniu keys list`、`muniu keys revoke <prefix>`。负面测试:无头/坏格式/吊销 key/不存在。
- [x] **T2.3 metering 落库链(测试先行)**:`internal/metering`:`Usage` 归一结构 + `Record` 行构造 → channel(cap 4096)→ flusher(500ms 或 256 条批量事务 INSERT OR IGNORE)→ 优雅排干;`tiktoken` 封装(词表 embed,模型前缀→编码映射,未知→`o200k_base`);估算路径(usage 缺失→输入 messages 序列化计数、输出累计 delta,`estimated=1`);计价集成 Phase 0 `price.go`。测试:背压(满 channel 阻塞不丢)、排干、估算置位、计价表驱动已有则补边界。
- [x] **T2.4 RPM 限流(测试先行)**:`internal/ratelimit`:每 apikey 一个 `rate.Limiter`(桶容=rpm,速率 rpm/60 每秒),惰性创建,idle 1h 淘汰;拒绝 → 429 信封 + `Retry-After` 秒;测试边界 N-1/N/N+1 与并发 `-race`。
- [x] **T2.5 conformance 目录与首批 fixture**:`conformance/README.md`(数据集说明/贡献规范)、`schema.json`、加载器 `internal/adapter/conformance_test.go`;首批 fixture:`openai/chat-basic.json`、`openai/stream-basic.json`、`deepseek/reasoning-stream.json`、`deepseek/usage-cache-tokens.json`、`moonshot/stream-basic.json`、`glm/stream-basic.json`——**每个 `source_url` 必填**,写 fixture 前逐家读官方文档核对矩阵 ⚠ 项,并把核对结论回填 `upstream-adapters.md` 矩阵(去掉 ⚠ 或改正)。
- [x] **T2.6 quirks 三家(测试先行,fixture 驱动)**:`adapter/quirks/deepseek.go`(`reasoning_content` 读侧识别为实质内容、`prompt_cache_hit_tokens→cached_tokens`、`completion_tokens_details.reasoning_tokens`)、`moonshot.go`、`glm.go`(按 T2.5 核对结论落实差异,无差异则显式空 quirk + 说明);passthrough 逐字节等价断言不变。
- [x] **T2.7 错误信封统一出口**:proxy 所有错误路径经单一 `writeError`(状态映射表);日志 `error_detail` 承载技术细节;访问日志退出条补齐 token/成本字段。
- [x] **T2.8 中间件链与生命周期集成**:链 = `request_id→recover→access_log→auth→quota(占位,永放行)→ratelimit→handler`;计量接入第 11 步(成功/失败/断连三态都记);key 仍取每 provider 第一把(池在 Phase 3)。集成测试:一条流式请求 → `usage_logs` 落一行且字段正确(临时库断言)。
- [x] **T2.9 config 价目与示例同步**:pricing 加载经 `ParsePrice` 换算 μ;`muniu.example.yaml` 牌价**按官方页逐条核对回填**(核对表:模型/币种/输入价/输出价/官方链接/核对日期 → 进 HANDOFF 验收记录);新增配置键同步示例注释。
- [x] **T2.10 端到端与基准**:真上游 smoke(`make smoke PROVIDER=deepseek`,tag `smoke`,读环境变量 key):流式+非流式各一,`sqlite3 data/muniu.db 'select ... from usage_logs order by created_at desc limit 2'` 贴行;基准:`BenchmarkSSEForward`(已有)、`BenchmarkRateLimitAllow`、`BenchmarkCost`。
- [x] **T2.11 文档与发版准备**:README「快速开始」回填真实命令(含 `muniu keys create`);HANDOFF 更新;**tag `v0.1.0` 与 push 由用户指令执行**(goreleaser 首次发版前 `brew install goreleaser && goreleaser check`)。

## 设计指针

`data-and-config.md`(DDL/迁移/配置)· `metering.md` 全文 · `traffic-governance.md`「RPM」 · `upstream-adapters.md`「归一化」「conformance」 · `conventions.md`「错误信封」「日志」。

## 验收闸门

1. conformance fixture ≥6 个、≥3 家国产全绿;2. 牌价核对表;3. `usage_logs` 实测行(流式+非流式);4. 鉴权负面测试与限流边界测试列表;5. 三个基准数字;6. `make check` 全绿;7. 矩阵 ⚠ 项已按核对结论回填。

## Non-goals

多 key 池 · failover 多候选 · TPM/日配额硬闸 · 桥接(Anthropic/Gemini)· 管理 API/控制台 · 语义缓存。
