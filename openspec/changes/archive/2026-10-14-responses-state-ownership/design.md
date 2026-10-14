# Design: responses-state-ownership

- **根因与修法**：亲和条目 `{id, provider, at}` 缺归属维度。`affEntry` 增 `apiKeyID string`；`Put(id, provider, apiKeyID)`——唯一调用点在 handleChat 成功回写处（ctx 恒有鉴权信息，auth 中间件保证）；`Get` 签名扩为返回归属或直接带 key 参数判定（选后者：`Get(id, apiKeyID string) (string, bool)`，匹配在表内单点完成，三处消费点零重复逻辑——并发安全判定原子化，避免 Get 后二次比对的窗口）。`apiKeyID == ""`（历史/异常）条目视为永不匹配 fail-closed。
- **不可区分原则**：归属不匹配在三个消费点全部复用「未知」的既有出站形态——prevID → `response_affinity_lost` 自解释降级；GET/DELETE → 404 `response_not_found`。不新增错误码、不区分文案：区分即泄露「该 respID 存在且属于别人」。DELETE 在归属判定失败时**先拒后出站**，上游零调用（不可逆操作唯一安全序）。
- **粒度裁决 per-key**：kongming 单租户多用户、每用户一把 key（锦官绑定记录）——per-tenant 绑定在此形态下无隔离意义；per-key 是最小可信隔离单元。代价：key 轮换后 24h TTL 内旧状态不可续用——安全默认优先，且粘性丢失本就有自解释降级路径（改发完整 input）。
- **指标口径**：粘性 hit/lost 计数不变（归属不匹配计入 lost——对运维它就是丢失）；GET/DELETE 无独立计数（现状亦无）。
- **mk2- 平台 key**：`verifyPlatform` 的 KeyInfo.ID = claims.KeyID，天然有归属标识，同规生效。
- **测试先行域（铁律 2）**：跨 key 三拒绝（GET/DELETE 断言上游零调用、prevID 断言自解释降级）+ 同 key 全链零变化回放（既有 e2e 链式用例回放）+ nil KeyInfo fail-closed + 空 apiKeyID 条目永不匹配。先红后绿。
- **范围外登记**：集群共享后端形态（ADR-0008）下归属字段随共享视图同步演进，本 change 只立进程内语义；DELETE 幂等重复调用（第二次已不可见 → 404）与现状一致。
