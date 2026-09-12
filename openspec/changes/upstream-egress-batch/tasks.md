# 任务清单:出站面批次一

> 纪律:测试先行域(failover 判定/协议 quirk)先红后绿;每任务收尾 = 实现 + 测试 + `make check` 全绿 + 勾选。
> 顺序:批次内先桥接边界后 Gemini(互不依赖,可并行推进但各自独立收尾)。

## 1. 桥接工具多轮显式边界

- [ ] 1.1 多轮标记检测纯函数(含 `role=tool` 或 assistant 带 `tool_calls`;tools 声明不触发)+ 桥接候选项资格豁免接线(failover 决策路径),结构化日志留痕;表驱动先红后绿(首轮 tools 放行/两类中途标记检出/非桥接候选不受影响)
- [ ] 1.2 仅桥接可达时终态 400 `bridge_tool_roundtrip_unsupported` 信封(中文文案 + request_id);EMA 豁免钉子测试(豁免不计失败/不触发冷却);traffic-governance.md 判定表扩「候选适配性豁免」格
- [ ] 1.3 e2e 钉子:多候选别名工具流量经非桥接候选服务成功(客户端无感);桥接独占别名得明确信封而非 upstream_error

## 2. Gemini 轻形态

- [ ] 2.1 真上游冒烟:OpenAI 兼容端点直连(脚本/实录),记录偏差清单(参数拒绝面/流式形态/usage 键名/响应头)
- [ ] 2.2 quirk 实现(仅覆盖实测偏差项)+ conformance fixture(冒烟实录脱敏装配)+ 能力目录声明样例(冒烟发现的参数个性)
- [ ] 2.3 价目录入(官方定价页核对,USD/百万 token,运营态)+ `muniu.example.yaml` 注释样例 + README/upstream-adapters.md 适配矩阵更新

## 3. 收尾

- [ ] 3.1 upstream-adapters.md 桥接节改写:「登记限制」→「显式边界契约」(检测条件/豁免语义/信封格式);HANDOFF 验收记录贴证据
- [ ] 3.2 终验:`make check` 全绿(vet + lint 0 issues + web 门禁 + 全包 race);smoke 实录(Gemini 直连 + 桥接边界两场景)
