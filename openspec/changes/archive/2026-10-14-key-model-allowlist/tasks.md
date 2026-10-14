# Tasks: key-model-allowlist

- [x] 1.1 失败测试先行：auth 白名单加载（含损坏 JSON 拒绝）与 PATCH 后缓存失效生效；admin 三态 PATCH/空条目 400/未知 key 404/回显（四包编译红已确认后转绿）
- [x] 1.2 迁移 0013（`api_keys.allowed_models TEXT NULL`）+ store 读侧（`APIKey.AllowedModels`、`Parse/MarshalAllowlist` 单源、JSON 解析失败按查询错误）；verify 链与 `KeyInfo.AllowedModels` 贯通
- [x] 1.3 `auth.CreateKey`/`LimitsPatch` 扩展（`normalizeAllowlist` trim 非空/去重/整单拒绝）；`protocol.TypeForbidden` + `statusByType` 403 映射 + conventions.md 错误表同步 + StatusForType 钉子
- [x] 1.4 admin create/PATCH/list 全链（RawMessage 三态、写后 Invalidate、响应回显）；openapi.yaml 契约同步 + api-types 重生成（双向契约测试钉死 required 覆盖）
- [x] 1.5 proxy 执行：handleChat 两点（主解析后 fail fast + 插件改写重解析后再校验）+ embeddings 两点/images 一点（插件后单点解析）；先于语义缓存检索；testkit 新增 FixtureModifyModel；六 lane 失败测试转绿（假上游断言零调用、白名单外不吃缓存命中、gemini writer 增 PERMISSION_DENIED 映射、空白名单字节级零变化回放）
- [x] 1.6 模型发现：`HandlerModels` 列表按 key 过滤 + 单查 403/404 分支；modelcatalog 失败测试转绿
- [x] 1.7 CLI：`muniu keys create --allowed-models`（逗号分隔）+ `keys list` 回显（models 列,不限显式文案）;keys_test 补用例
- [x] 1.8 控制台密钥页：签发表单/行内编辑/列表列全链（ui/Textarea 每行一个,空态「不限」,保存即整体设定）；web 四件套 + vite build(/tmp) + 裸元素自检全绿
- [x] 1.9 `make check` 全绿（coverage gate PASS 70.2%）+ `openspec validate --all` 24/24；金串记录（无白名单请求 wire 逐字节不变=既有全量零改动回放+空白名单恒放行专项）贴验收证据
