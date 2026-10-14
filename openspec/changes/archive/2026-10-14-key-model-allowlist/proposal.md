# Proposal: key 级模型白名单

## Why

key 现有治理粒度只覆盖频次与额度（RPM/TPM/日配额/过期），不能约束**模型面**：同一租户下所有 key 天然通用全部模型别名，平台侧（kongming）无法表达「新模型只对灰度 key 开放」「廉价套餐 key 不得调用旗舰模型」这类治理诉求。HANDOFF 开放决策项 2026-10-12 已裁决「key 级模型白名单=值得做，下一批（配 kongming 灰度治理故事单独成批）」——本 change 即该批：补齐 key 的第四个治理维度，闭合 kongming 平台按 key 下发模型边界的治理故事。

## What Changes

- **存储**：`api_keys` 新增 `allowed_models` 列（JSON 数组 TEXT，迁移 0013）；NULL/空数组 = 不限（现状等价，零回归）。
- **数据面执行**：鉴权 `KeyInfo` 携带白名单；四条入站 lane（chat / anthropic messages / responses / gemini，经 `handleChat` 单点）与 embeddings lane 在模型别名解析后、语义缓存检索**前**执行校验；插件钩子改写 `model` 后重解析路径同样再校验。白名单外别名 → 403 `forbidden` / `model_not_allowed`，上游零调用。
- **错误契约**：新增错误类型 `forbidden`（HTTP 403）——现有信封 8 类无 403 档，访问拒绝与请求畸形（400）必须可区分；`docs/conventions.md` 错误表与 `protocol.statusByType` 同步。
- **模型发现**：`GET /v1/models` 列表按请求 key 过滤（无白名单 = 全量，现状等价）；单查别名在全局目录但不在 key 白名单 → 403 `model_not_allowed`，不在全局目录 → 404（现状不变）。
- **管理面**：`POST /admin/v1/keys` 增 `allowed_models`（缺省不限）；`PATCH /admin/v1/keys/{id}` 三态语义（省略=不动 / `null`=清除恢复不限 / 数组=设定），写后失效鉴权缓存（同限额 PATCH 协议）；创建/PATCH/列表响应回显。**不校验别名存在性**——拓扑热替换下白名单可先于别名签发（预配置语义）。
- **CLI**：`muniu keys create` 增 `--allowed-models`（逗号分隔）；`keys list` 回显（截短）。
- **控制台**：密钥页签发表单、行内编辑、列表列全链补 `allowed_models`。
- **范围裁决**：`mk-` 查库 key 全链覆盖；`mk2-` 平台 key **范围外**——pkey.v1 载体格式冻结，平台 key 恒不限，扩展归未来 pkey.v2 立项（当前活消费方锦官实际走 mk- + admin API）。

## Capabilities

### New Capabilities

（无——模型访问治理是 key 生命周期与模型发现既有能力边界的自然延伸，不另立能力。）

### Modified Capabilities

- `api-key-lifecycle`：新增三条 requirement——①签发粒度扩展（`allowed_models` 字段与缺省不限语义）；②数据面模型访问执行（四 lane 覆盖、先于语义缓存与上游、插件重写再校验、403 错误语义）；③白名单管理面维护（create/PATCH 三态/列表回显/CLI/控制台，写后缓存失效）。
- `model-discovery`：修改「模型列表端点契约」（目录按请求 key 白名单过滤）与「单模型查询与 404 契约」（增 403 `model_not_allowed` 分支）。

## Impact

- **代码**：`internal/store`（迁移 0013 + `APIKey` 读侧）、`internal/auth`（`KeyInfo`/`CreateKey`/`LimitsPatch`）、`internal/admin`（create/patch/list）、`internal/proxy`（`handleChat` 两点 + embeddings 两点 + `HandlerModels`）、`internal/protocol`（errors.go 新常量）、`cmd/muniu/keys.go`、`web/src/pages/Keys.tsx`。
- **契约同步**：`docs/conventions.md` 错误类型表、`docs/design/api/openapi.yaml` + `web/src/api/api-types` 重新生成（路由漂移门禁覆盖 schema 变更）、`muniu.example.yaml` 注释样例、`docs/design/auth-and-tenants.md`（如存在 key 粒度章节）。
- **零依赖新增**；`make check` + `openspec validate --all` 为完成门禁；测试先行域（治理/鉴权语义）按铁律 2 先写失败测试。
