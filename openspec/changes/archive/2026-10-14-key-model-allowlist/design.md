# Design: key-model-allowlist

- **粒度裁决——按模型别名（路由键），不按上游模型名**：客户端契约面即别名；别名→多候选换道是内部实现，治理语义面向调用方；`/v1/models` 发现面同键，过滤视图与转发同源自洽。
- **存储形态——单列 JSON TEXT（迁移 0013）**：`api_keys.allowed_models TEXT NULL`；NULL 与空数组读侧等价 = 不限。不建关联表——数组基数个位数、无独立查询/聚合诉求，JSON 列免 JOIN 且与运营态 PATCH 语义天然对齐；读侧 `json.Unmarshal` 失败按查询错误返回（宁可拒绝该 key，不放行语义不明的凭据，对齐 expires_at 损坏先例）。
- **执行位置——解析后、缓存前，每入口双点**：
  - `handleChat` 单点覆盖 chat/messages/responses/gemini 三委托 lane：①主解析 + 首次 `topo.routes` 解析后立即校验（fail fast：被拒请求不进插件钩子、不产生插件副作用）；②插件改写 `model` 后的重解析路径再校验（最终出站的别名必须合法——只查一次会被插件改写绕过）。
  - embeddings 与 images 各自有管道，同序两点（主解析 + 插件重解析；images 为并行会话 2026-10-13 新增 lane，白名单天然同覆盖）。
  - 校验放语义缓存检索**前**：白名单外的别名不得吃缓存命中回放（回放即事实放行）。
  - 判定实现：`len(allow)==0 → 放行`；否则线性扫描（基数个位，零分配，不建 map）。
- **错误契约——新增 `TypeForbidden="forbidden"` → HTTP 403**：现有 8 类信封无 403 档。不复用 400：访问拒绝与请求畸形必须可区分（kongming 客户端需程序化识别「该 key 无权此模型」以引导升级/申请，与改请求参数是两种处置）；`protocol/statusByType` + `docs/conventions.md` 错误类型表同步，code=`model_not_allowed`。发现面单查复用同码同形，客户端一套处置逻辑。
- **发现面单查 403 vs 404 裁决**：在全局目录但白名单外 → 403（可诊断性优先，且与转发路径同码）；不在全局目录 → 404 现状不变。代价是向持有效 key 的租户泄露别名存在于全局目录——别名清单非机密（无白名单 key 本就可全量列出），换取可诊断性成立。
- **KeyInfo 扩展与缓存协议**：`KeyInfo` 增 `AllowedModels []string`，随 verify 查询一次加载；缓存命中路径携带同值（TTL 内可见性与限额字段同缝）；PATCH 写后 `Verifier.Invalidate()`（既有协议零新增机制）。`mk2-` 平台 key 走 PlatformClaims → `AllowedModels=nil` = 不限，**pkey.v1 载体冻结不动**：白名单定位为运营态（可 PATCH 随时调整），与 mk2-「签发时定、换发生效」语义相抵；且活消费方锦官实际走 mk- + admin API。扩展归未来 pkey.v2 立项，不在本 change 猜。
- **admin/CLI 面**：create 增 `allowed_models`（缺省不限）；PATCH 用 `json.RawMessage` 三态（省略不动 / `null` 清除 / 数组设定），对齐 `expires_at` 既有模式；条目 trim 非空、去重落库、整单拒绝校验失败；**不校验别名存在性**（拓扑热替换，预配置语义——校验存在性会把白名单维护与拓扑顺序耦合）。CLI `--allowed-models` 逗号分隔；`keys list` 回显（截短）。
- **控制台**：密钥页签发卡、行内编辑、DataList 列全链补字段；输入用 ui/Textarea 每行一个别名（别名理论可含逗号，按行分隔无歧义）；列表列截短 + title 全值（对齐 Key ID 列范式）；空态显示「不限」。门禁按前端宪法四件套 + vite build /tmp。
- **契约同步清单**：`openapi.yaml`（KeyCreateRequest/KeyPatchRequest/KeyRecord 增字段 + 403 响应）→ `pnpm api-types` 重新生成；`docs/conventions.md` 错误类型表 +1 行；`muniu.example.yaml` 不动（白名单是 key 运营态非网关配置）。
- **测试先行域（铁律 2）**：①auth——白名单加载/损坏 JSON 拒绝/PATCH 后缓存失效生效；②admin——三态 PATCH/空条目 400/未知 key 404/回显；③proxy——四 lane 白名单外 403（假上游断言**零调用**）/白名单外不吃缓存命中/插件改写再校验/空白名单字节级零变化回放；④embeddings 两点；⑤modelcatalog——列表过滤/单查 403 与 404 分支；⑥protocol——`StatusForType(forbidden)==403` 钉子。
- **失败模式与边界**：语义缓存命中路径不重新鉴权模型（校验已前置）；粘性 lane 拒绝发生在 stickyRoute 域收缩后同点；白名单校验不产生计量行、不留痕头（无参数干预，与 ADR-0010 治理正交）；`/v1/models` 过滤不消耗额度（既有只读面不变）。
- **评审边界处置（2026-10-14 owner 委任裁决，双边界均修）**：①**状态 lane 归属隔离**升格为独立 change `responses-state-ownership`（亲和条目绑 apiKeyID，三消费点不匹配≡未知；根因是亲和缺归属维度而非白名单缺口）——白名单与归属隔离合围后状态 lane 不再是数据面旁路；②**messages 流式管道错误信封**按类型映射 Anthropic 原生词汇表（`forbidden→permission_error` 等，原「api_error 硬编码」v1 登记条款废止）——随本批顺带根修，登记见 proxy-and-streaming.md「Messages 入站」错误形态行。
