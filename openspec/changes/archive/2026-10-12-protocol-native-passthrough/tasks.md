# Tasks

## 1. 协议层:入站原始体保留(测试先行域)

- [x] 1.1 先写失败测试:`TranslateMessagesRequest` 返回值保留原始请求体与入站协议标签;不可翻译内容块(image 等)产出失格标记(含原因)而非 error(现行 fail-fast 400 应先红);断言含 image/thinking 回传/cache_control/stop_sequences/top_k/metadata 的原始体逐字节保留;`go test ./internal/protocol/` 可运行
- [x] 1.2 实现 DTO 扩展(原始体字段 + 协议标签枚举 + 失格标记),可翻译部分照旧产出 canonical;messages handler 过渡期将失格按现状即时 400 处理(对外行为逐字节不变);`go test ./internal/protocol/ ./internal/proxy/ -race` 全绿
- [x] 1.3 先写失败测试再实现 `TranslateResponsesRequest` 原始体保留(协议标签扩展 responses),现行翻译行为与拒绝清单零改动;`go test ./internal/protocol/ -race` 全绿

## 2. adapter 直通能力接口 + bridge 实现(测试先行域)

- [x] 2.1 先写失败测试:直通 Prepare(原始体 + 上游 model + key → Outbound)——URL/`x-api-key`/`bearer`/`anthropic-version` 头、model 字段 JSON 手术后其余逐字节相等;meta-only 流读侧——anthropic 事件序列产出正确 `EventMeta`(HasContent/IsUsage/IsDone)且 passthrough 返回原始字节;`go test ./internal/adapter/...` 可运行
- [x] 2.2 实现 `NativePreparer` 可选能力接口(proxy 类型断言探测,与 `EmbeddingPreparer` 同款)与 bridge 包实现;`Adapter` 五方法契约与 `Types()` 值域不动(`types_test` 既有断言通过);`go test ./internal/adapter/... -race` 全绿
- [x] 2.3 直通保真 fixture 冻结进 `conformance/anthropic/`(原始体含多模态/扩展字段的出站 wire 逐字节断言 + 事件序列 meta 对照),`conformance/README.md` 注记用途;fixture 被 2.1 测试消费

## 3. proxy:路径判定与响应 writer 模式位(测试先行域)

- [x] 3.1 先写失败测试:每 attempt 路径判定——messages 入站 × anthropic 候选=直通;responses 入站 × `native_responses: true` 声明候选=直通、未声明候选=翻译;换道 openai 系候选=翻译;治理干预、插件改写 canonical 两回落信号=翻译;失格分流:别名无直通候选=即时 400(现状),有直通候选=先直通、换道落翻译候选时出站前升格 400(messages `content_block_unsupported` / responses `input_item_unsupported`);判定为纯函数可表驱动;`go test ./internal/proxy/ -run TestNativePath` 可运行
- [x] 3.2 实现:messages/responses handler 保留原始体并按失格分流(无直通候选即时 400 / 放行直通 / 换道升格既有 400 信封);`handleChat`/`attempt` 按 3.1 判定接通 `NativePreparer` 出站(responses 直通 URL=`<base>/v1/responses`,仅 model 改写 + 鉴权注入,取 key/计量/错误分类链路复用);`go test ./internal/proxy/ -race` 全绿
- [x] 3.3 先写失败测试再实现 writer 模式位:非流式缓冲恒等写、流式逐帧恒等透传(含 `message_start` 真实 usage、上游错误 JSON 原样)、首字节提交前允许模式切换、已提交绝不二次写出;流式 meta 提取走 meta-only 读侧(计量/首 token 判定与翻译路径同口径);`go test ./internal/proxy/ -race` 全绿
- [x] 3.4 先写失败测试再实现配置声明面:`providers[].native_responses`(布尔,缺省 false)进 config schema 校验与装配,未声明行为与本变更前逐字节一致;`go test ./internal/config/ ./internal/proxy/ -race` 全绿

## 4. bridgegate 收窄(测试先行域)

- [x] 4.1 先写失败测试:messages 入站 × anthropic 候选携带工具多轮(role=tool / assistant.tool_calls)直通正常服务;chat 入站同流量边界行为逐字节不变(豁免换道/独占 400 `bridge_tool_roundtrip_unsupported`);豁免/直通均零 EMA/冷却副作用;`go test ./internal/proxy/ -run TestBridgeGate` 可运行
- [x] 4.2 实现豁免条件收窄为「该 attempt 实际走翻译路径」;`go test ./internal/proxy/ -race` 全绿

## 5. 语义缓存:存储侧翻译(测试先行域)

- [x] 5.1 先写失败测试:直通成功响应入库为双存(canonical chat 形态可被等价 chat 请求命中;原生体+协议标签+表单标记可被同构请求命中);同构命中回放原生体逐字节等于上游形态、不经反向映射(流式对流式);表单不兼容/跨协议命中回退 canonical 路径;翻译失格请求零检索零入库;缓存未启用租户零翻译开销;入库翻译失败仅 canonical 侧跳过且请求正常返回;`go test ./internal/proxy/ -run TestNativeCache` 可运行
- [x] 5.2 实现双存:直通响应并储 canonical(经桥接既有翻译 RewriteResponse/流式事件机)与原生体+标签;回放按命中协议与表单标记选择原生恒等/反向映射路径;`go test ./internal/proxy/ -race` 全绿

## 6. 组合行为、文档与收尾

- [x] 6.1 组合测试冻结:换道跨协议(writer 切换后客户端获单一完整响应)、首字节前失败换道、已提交流断流不换道、直通×缓存命中回放(同构命中原生体恒等回放逐字节断言;跨协议命中反向映射)、治理回落请求行为与变更前逐字节一致、responses 直通矩阵(声明候选 image 部件透传;未声明翻译兜底;换道落翻译候选升格 `input_item_unsupported`);`go test ./internal/proxy/ -race` 全绿
- [x] 6.2 文档落地:ADR-0014(同构直通路由,含决策 1 否决记录、决策 9 声明制与有状态收编论证——含百炼多轮回放缺口实证引用与粘性语义说明)、proxy-and-streaming.md「同构直通」契约节(路径判定表/保真不变式/回落纪律/惰性拒绝两种时序/错误信封按路径分域/粘性路由语义)、upstream-adapters.md 增直通接口与读侧注记、ADR-0013 补交叉引用并把「明确不做」清单改写为翻译路径域(多模态/thinking 回传/top_k/metadata 经直通恢复原生支持)、provider-presets.md 逐家标注 responses 原生端点可用性实证(百炼:勿声明 native_responses,修复后开启即切);`docs/` 交叉引用可达
- [x] 6.3 收尾门禁:`make check` 全绿(vet + lint + race test);验收证据(命令+输出)按仓库纪律贴 HANDOFF 验收记录区

## 7. 有状态粘性路由(测试先行域)

- [x] 7.1 先写失败测试:亲和表(id→provider,进程内 TTL + LRU 上限;共享后端形态复用)、粘性候选域收缩(亲和 provider 域内 key/候选 failover 照常、跨 provider 不换道)、亲和未知自解释错误(不盲发上游)、成功响应 id 回写、状态请求缓存旁路(零检索零入库,限流计量照常);`go test ./internal/proxy/ -run TestSticky` 可运行
- [x] 7.2 先写失败测试再实现 GET /v1/responses/{id}:亲和已知代理转发亲和 provider 原样返回、未知恒 404 自解释(既有恒 404 语义对未知 id 保持);翻译路径 `previous_response_id` 维持 400 自解释;`go test ./internal/proxy/ -race` 全绿
- [x] 7.3 testkit 有状态替身(内存 response 存储 + `previous_response_id` 上下文拼接,OpenAI 公开契约 fixture 冻结)+ 状态 e2e:创建 → prev 链式 → GET 代理,双实例配方复用;`make check` 全绿,粘性请求数/亲和命中率指标随 metrics 落地

## Workflow follow-up

- 真上游冒烟(手动,需环境变量注 key):Claude Code 指向 muniu `/v1/messages` × anthropic 分支端点,携带工具多轮与 thinking 流量对拍直连上游行为;证据补录 HANDOFF 验收记录区。
- 归档前按 `openspec` 纪律同步主 specs(`protocol-native-passthrough` 新建、`anthropic-bridge`/`responses-inbound` 修订),由 owner 指令触发 archive 流程。
- 姊妹变更「协议中立干预」(约束 IR + 每协议 emitter,消除治理回落这最后一处 Role B 残余):passthrough 落地后 propose,按作品路线图排序——学习框架下不设需求触发线;插件 ABI 维持 canonical + 回落边界(design 决策 9 显式选择),由 owner 指令触发立项。
