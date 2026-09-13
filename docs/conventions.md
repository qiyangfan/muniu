# 工程约定(conventions)

> 本文是代码评审的基线;与 AGENTS.md 铁律冲突时以 AGENTS.md 为准。

## 语言与注释

- 文档、注释、commit message 正文、日志文案:**简体中文**;标识符、错误码、配置键:英文。
- 注释只写代码说不出的东西:为什么这么选、边界与陷阱、与上游协议的差异来源。禁止逐行复述。
- 导出符号必须有注释(revive `exported` 规则已启)。

## Go 代码规范

- 包名小写单词,不用 `util`/`common`/`base` 这类垃圾抽屉名;包按领域切(config/protocol/proxy/adapter/...),不按层切 utils。
- 接口定义在**消费方**包内(Go 惯例),不搞 Java 式 interface 集中营;接口方法 ≤4 个,超了就拆。
- 错误:哨兵用 `errors.New` 定义在领域包;包装一律 `%w`;判定用 `errors.Is/As`,禁止字符串比对。
- `context.Context` 永远是第一参;任何出站 HTTP 调用必须携带请求级 context(断连传导的生命线)。
- 时间:业务一律 `time.Time` + RFC3339(UTC)序列化;禁止在业务代码里摸本地时区。
- 金额与 token 数:**整数**(分/美分、个数),禁止 float 参与任何金额运算。
- 并发原语:优先 channel + `context`;共享状态用 `sync.Mutex` 并把锁守卫范围写进注释;禁止 `map` 裸并发读写。

## 日志(slog)

- 结构化字段 snake_case;每请求必有 `request_id`。
- 字段命名表:`request_id / tenant_id / api_key_id / provider / model_alias / upstream_model / prompt_tokens / completion_tokens / reasoning_tokens / cached_tokens / cost_credits / currency / latency_first_token_ms / latency_total_ms / error_class / estimated`。
- 用户可读错误与内部技术详情分离:HTTP 响应只给归一化 message,技术细节(err 链、上游 body)进日志 `error_detail` 字段(借鉴 kongming 范式)。

## 错误信封(对外契约,改此格式=契约级)

```json
{
  "error": {
    "message": "上游供应商暂时不可用,已尝试全部候选",   // 用户可读,中文优先
    "type": "upstream_error",       // bad_request|auth_failed|rate_limited|quota_exceeded|not_found|timeout|upstream_error|internal
    "code": "all_candidates_failed", // 机器码,snake_case
    "request_id": "01J..."
  }
}
```

HTTP 状态映射:`bad_request→400`,`auth_failed→401`,`quota_exceeded→402`,`rate_limited→429`,`not_found→404`,`timeout→504`,`upstream_error→502`,`internal→500`。

## 测试范式

- 表驱动为主:`TestXxx_场景` 命名;断言失败信息必须能定位到场景。
- **假上游是核心测试设施**:`internal/testkit` 提供可编程 SSE 序列的 fake 上游(正常/慢 chunk/中途断/半帧/429/5xx/usage 缺失七种剧本),所有 proxy/adapter 测试只打 fake,CI 永不打真上游。
- 协议 fixture:golden 文件放 `conformance/<provider>/`,测试用 `goldie` 式比对或手写等价断言;fixture 变更必须连同 provider 官方文档链接一起提交。
- 竞态:`make test` 带 `-race`,并发代码必须有并发测试。
- 基准:关键路径(SSE 转发、key 池选择、限流判定)各至少 1 个 `Benchmark`,随 Phase 2 起。

## 存储与迁移

- SQLite(modernc 纯 Go 驱动),WAL 模式,`busy_timeout=5s`,`foreign_keys=ON`。
- 迁移:SQL 文件嵌 `internal/store/migrations/`(embed),文件名 `0001_init.sql` 顺序编号,`schema_migrations` 表记录;**禁止修改已应用的迁移,只追加新文件**。
- Schema 纪律(沿 kongming):状态列 `TEXT + CHECK`,禁魔数;主键 UUIDv7 字符串;时间列 TEXT RFC3339;金额列 INTEGER(**credit** 单位,1 credit = 本位币十亿分之一,见 metering)+ currency 列;配额列 `daily_quota_credits` 同单位。
- JSON 出站纪律:身份字段(ID)一律 string(UUIDv7 主键,禁 int64 主键——JS float64 在 2⁵³ 失真,Snowflake 事故);64 位数量字段(token/credit/限额)出站 JSON number(消费方需要比较/聚合/图表数值),整数精确边界 2⁵³ ≈ 9.0e15 credit(≈900 万本币);仅当聚合结果可能越界的接口,该字段须改为 string 出站(OpenRouter 惯例),存储与计算层不动。

## 配置

- 键名 snake_case;新键必须同步更新 `muniu.example.yaml` 带注释示例,否则视为未完成。
- 密钥明文只允许存在于环境变量;YAML 与 SQLite 一律存环境变量**引用名**。

## 版本与发版

- SemVer;`v0.x` 阶段 minor 可含破坏性变更(README 顶部已声明)。
- 发版 = 打 tag 推送触发 goreleaser(需用户指令);变更说明以 GitHub Releases 为唯一事实源,**不维护 CHANGELOG.md**(避免双记)。
- commit message:Conventional Commits(`feat: / fix: / docs: / chore: / test: / refactor:`),中文正文;goreleaser 分组依赖 `feat/fix` 前缀,格式即契约。
