# 架构总览(architecture)

> 本文定进程/线程模型与包边界;行为语义的深水区见同目录其余设计文档。

## 进程模型

单进程单二进制。内部只有三类长生命周期 goroutine,其余全部「每请求一 goroutine」:

| goroutine | 数量 | 职责 | 生命周期 |
|---|---|---|---|
| HTTP server(main) | 1 | Accept + 中间件链,每请求派生 goroutine | 进程全程 |
| metering flusher | 1 | 从计量 channel 批量落库 usage_logs(500ms 或满 256 条) | 进程全程 |
| signal watcher | 1 | 监听 SIGINT/SIGTERM 触发优雅退出 | 进程全程 |

key 池状态、限流窗口、apikey 缓存全部进程内内存态,由 mutex 守卫,不引入后台刷新协程(惰性更新:请求路径上顺手更新,杜绝后台任务与请求路径的时序 bug 面)。

## 请求生命周期(12 步,契约)

以 `POST /v1/chat/completions` 为例,顺序即实现顺序,任何一步失败跳「错误出口」:

1. **request_id 签发**:UUIDv7(`google/uuid`,与 DB 主键同源,不另引 ULID 库),写入 context + slog;响应头 `X-Muniu-Request-Id` 回带。
2. **recover 中间件**:兜底 panic → 500 internal(日志含堆栈),绝不裸崩进程。
3. **访问日志中间件**:进入/退出各一条 slog(退出条含状态/耗时/token 数,字段表见 conventions)。
4. **鉴权**:解析 `Authorization: Bearer mk-…` → sha256 → api_keys 表查缓存(进程内 LRU,10s TTL)→ 挂载 tenant 与限额上下文;失败 `auth_failed`。
5. **配额预检**:tenant/key 日配额(厘)已用量 ≥ 上限 → `quota_exceeded`(已用量读 metering 当日聚合,进程内缓存 30s)。
6. **限流判定**:RPM 令牌桶(`x/time/rate`,per apikey);Phase 3 加 TPM 滑动窗口;拒绝 → `rate_limited`(带 `Retry-After`)。
7. **请求解析与校验**:入站 body 按 OpenAI Chat Completions DTO 解析(`internal/protocol`);model 别名不存在、messages 非法 → `bad_request`;**解析后立即持有完整字节**(适配器转换基于此,Body 不可重读问题在此消灭)。
8. **路由解析**:model_routes 查候选有序列表 → 构造候选迭代器(同 priority 加权随机,priority 递增降级,见 traffic-governance)。
9. **逐候选执行**(failover 循环):取 key(key 池平滑加权轮询+冷却过滤)→ 适配器翻译请求(adapter)→ 出站调用(三层超时)→ 按首 token 边界语义处理成败。
10. **响应转发**:非流式=整包改写回写;流式=SSE 逐事件透传(归一化 quirk 字段,见 proxy-and-streaming)。
11. **计量**:归一 usage(上游 usage 帧 > 本地估算,estimated 标志)→ 计价(pricing 表)→ 投递计量 channel(异步落库);失败请求也计量(记 error_class,成本 0)。
12. **退出**:defer 链保证上游 Body/连接关闭、限流器归还、span 收尾;**客户端断连在中途任何一步发生 → cancel 沿 context 传导至上游请求**,转第 11 步如实计量已发生部分。

错误出口:构造错误信封(状态映射表见 conventions)→ 日志 `error_detail` → 计量(若已过第 7 步)→ 返回。

## 中间件链(顺序即契约)

```
request_id → recover → access_log → auth → quota → ratelimit → handler(解析→路由→failover 循环→转发→计量)
```

实现:标准库 `http.ServeMux`(Go 1.22+ method+pattern)+ 约 30 行 chain 辅助函数,不引第三方路由/中间件库(ADR-0002)。

## 包结构与依赖方向

```
cmd/muniu/            装配:配置→store→adapter 注册表→keypool→限流器→server
cmd/tokenctl/         Phase 0:学习用 CLI(价目换算/粗估)
internal/
  protocol/           入站/出站共享 DTO:Chat 请求响应、SSE 帧、usage、错误信封(零内部依赖)
  config/             YAML 加载、${VAR} 环境展开、启动期校验(依赖 protocol)
  adapter/            适配器:openai.go(基座)+ quirks/ + bridge/{anthropic,gemini}.go(依赖 protocol)
  keypool/            key 选择/冷却/健康度(依赖 config 的拓扑)
  failover/           候选迭代器与错误分类→动作判定(纯函数,零 IO,依赖 protocol)
  ratelimit/          RPM 令牌桶 + TPM 滑动窗口 + 配额判定
  proxy/              编排:9-12 步实现,唯一允许「认识所有包」的组装层
  auth/ tenant/       apikey 校验/签发、租户与配额读侧
  metering/           usage 归一、估算(tiktoken 封装)、计价、异步批量落库
  store/              modernc/sqlite、migrations(embed)、repo;依赖方向只出不进
  semantic/           Phase 4 语义缓存
  server/             HTTP 入口装配:ServeMux+中间件链+优雅退出(Phase 1 起)
  admin/              管理面 /admin/v1/*(MUNIU_ADMIN_TOKEN 鉴权,Phase 4 起)
  logging/            slog 装配与公共字段
  testkit/            fake 上游/测试辅助(仅测试与 smoke 构建)
  version/            版本号(ldflags 注入)
  web/(go:embed)     控制台构建产物,由 /console/* 提供(Phase 4 起)
conformance/          协议 fixture(数据,不参与编译;adapter 测试消费)
```

依赖铁则:`protocol` 不 import 任何内部包;`store` 不被 `protocol`/`adapter` 感知;`proxy` 是唯一组装点;**出现循环依赖 = 架构错误,回到本文改边界,不许用接口糊弄过去**。

## 优雅退出(signal → 顺序即契约)

1. `http.Server.Shutdown(ctx, 15s)`:停止收新请求,在途请求跑完/超时;
2. 关闭计量入口(channel),flusher 排干存量后落库;
3. 关闭 SQLite;4. 记退出日志。全程预算 20s,超时强制退(退出码 1)。

## 构建与版本

`CGO_ENABLED=0 go build`(纯静态,交叉编译 darwin/linux × amd64/arm64);版本经 `-ldflags -X …/internal/version.Version=$(git describe --tags --always)` 注入,`/healthz` 暴露。
