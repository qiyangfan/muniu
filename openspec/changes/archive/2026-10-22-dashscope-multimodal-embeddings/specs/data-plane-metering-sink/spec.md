## ADDED Requirements

### Requirement: 多模态 token 明细跨计量汇持久化

计量行必须 (MUST) 新增 `vision_tokens` 非负整数，含义为 `prompt_tokens` 总输入 token 中的图像/视频 token 子集。该字段必须 (MUST) 穿过 SQLite usage_logs、cluster 本地缓冲与续传、远程计量汇及其幂等重放；历史行与不产出视觉用量的请求默认 0。Schema 变更必须 (MUST) 通过追加迁移完成，禁止修改已应用迁移。

#### Scenario: 新视觉 token 行经本地汇落库

- **WHEN** 请求生成 `prompt_tokens=120`、`vision_tokens=80` 的计量行
- **THEN** SQLite 保存 120 与 80，重启后读回值一致

#### Scenario: cluster 缓冲续传保留视觉用量

- **WHEN** 远程汇暂不可用，含视觉 token 的行进入本地缓冲后恢复连接并续传
- **THEN** 权威汇收到同一行及相同 `vision_tokens`，按既有 ID 幂等去重

#### Scenario: 旧行和普通请求兼容

- **WHEN** 读取迁移前 usage 行或新建无视觉子类的普通请求行
- **THEN** `vision_tokens=0`，既有字段值和计量行为不变
