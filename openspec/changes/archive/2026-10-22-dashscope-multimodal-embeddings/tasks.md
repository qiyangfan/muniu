# Tasks

- [x] 1.1 失败测试先行：native request DTO/路由别名解析、`input.contents` 必填、未知字段保留、顶层 model 原位替换；fixture 覆盖 `qwen3-vl-embedding` 独立/融合与 `tongyi-embedding-vision-flash` 多模态输入。
- [x] 1.2 失败测试先行：配置 `native_multimodal_embeddings_url` 的 URL/adapter 校验；文件配置、SIGHUP 与控制面 topology 的序列化/解析/原子替换一致性。
- [x] 1.3 实现可选 `MultimodalEmbeddingPreparer` 能力与 Qwen/DashScope 出站准备：显式 URL、Bearer key、原生 API 路径；未声明能力的候选不能误发到 `/embeddings`。
- [x] 1.4 实现 native 入站 handler 与 mux/OpenAPI：`dataPlaneMW`、key 模型白名单、插件单次执行及改写后复校验、支持候选筛选、现有 failover 与 native 上游错误回传；测试上游失败换道及全候选失败。
- [x] 1.5 失败测试先行并实现 native usage 解析：Qwen 文本/视觉/总 token、Tongyi 总 token与视觉明细、缺失/非法/不自洽 usage 的 estimated 行；上游响应 body 始终逐字节透传。
- [x] 1.6 追加 SQLite 迁移扩展 `usage_logs.vision_tokens` 与 token 价目 `vision_input`；贯通 Usage、UsageRow、批量写入、缓冲续传、所有计量汇及管理查询，旧行默认 0。
- [x] 1.7 扩展 token 计价：文本/视觉分段整数计价与分别向上取整；`vision_input` 缺省回落 `input`；更新 YAML/DB/管理 API/OpenAPI/控制台价目编辑，证明既有纯文本价目行为零回归。
- [x] 1.8 更新 `muniu.example.yaml`、`docs/design/data-and-config.md`、`docs/design/upstream-adapters.md`、`docs/design/metering.md` 与提供商 conformance 文档；登记 16 MiB 请求上限、模型范围和上游参数限制。
- [x] 1.9 运行 OpenSpec strict 校验、`make check` 与 web 门禁；fake 上游验证本地完整 wire/计量链。DashScope 真上游冒烟只按手动 smoke 纪律执行，不作为 CI 测试。
