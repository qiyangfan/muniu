# 本地未提交代码审查报告（2026-10-13）

补记：本报告当时范围外的旧计费溢出与严格规格占位，已由后续用户点名授权修复并通过验收；见 [后续记录](2026-10-13-cost-overflow-fix.md)。以下保留评审时刻的原始结果。

本轮冻结范围为本地全部未提交变更，共 110 个文件（含已有 key 启停/请求预算、images、管理员审计与配套文档）。范围见 [文件哈希清单](2026-10-13-review-evidence/scope.json)，启动时 tracked diff 见 [快照](2026-10-13-review-evidence/start.diff)。新增内容仅为已证实缺陷的回归测试、报告与验收证据。未执行 add/commit/push/tag。

已修复 17 项（15 项缺陷、2 项风险）。复核覆盖正确性、并发状态、资源异常、安全、契约、性能与测试；修复后再次检查调用链及 diff，当前冻结变更集中无尚未处理的已证实缺陷。主执行者完成本轮评审与复审；不冒认独立代理复审。本轮不注册长期目标、不监听后续变更。

## 已证实并修复

| 编号/严重度 | 位置 | 问题 | 修法与证明 |
|---|---|---|---|
| 1 缺陷 | internal/protocol/images.go · ParseImageRequest | Go 结构体反序列化把未知大写 N/Size/Quality 当计价字段，转发体与扣费口径分离 | 仅读取精确标准键，保留 Raw；TestImages_计价只消费标准字段先红后绿 |
| 2 缺陷 | internal/proxy/metering.go · preflightBudgetExceeded/budgetCost | max_tokens 极值使乘积回绕，绕过预算并调用上游 | bits 宽乘积、逐段 ceil、检查最终可表示范围；极大上界用例及 255 组 big.Int 对拍通过 |
| 3 缺陷 | internal/proxy/metering.go · record | 中断前只有输入 usage 时，已生成输出漏账且未标估算 | 字段级补齐缺失输出、保留输入真值；TestCostBudget_输入usage不能吞掉中断输出通过 |
| 4 风险 | internal/auth/admin.go · Login | 未认证随机用户名永久积累失败状态，外部输入驱动无界内存增长 | 不存在账号仍计算 dummy 哈希，但不保存用户名状态；真实账号五次失败锁定契约不变，随机用户名与原生命周期测试通过 |
| 5 缺陷 | internal/admin/operators.go · auditOperators | handler panic 被外层转为 500，但内层审计记成功 | 收尾识别 panic、记失败、继续抛给既有 Recover；TestAdminOperators_panic不得审计为成功通过 |
| 6 缺陷 | cmd/muniu/admins.go · adminsMain/passwd | 空 stdin/空行改密实际不改，却报告成功 | passwd 明确拒绝空密码，原密码与 API Patch 语义不变；空输入两形态通过 |
| 7 缺陷 | cmd/muniu/main.go · assemble；internal/proxy/images.go · recordImages | USD 本位币且无图片价目时，零成本行错误标 CNY | 装配传递网关本位币，零成本计量使用该币种；实际装配/鉴权/SQLite 用例通过，同类 record 路径复用同一字段 |
| 8 缺陷 | web/src/lib/api.ts · call/setToken | 旧请求的延迟 401 清除新登录会话 | 请求捕获认证版本，仅当前版本发失效事件；延迟响应测试通过 |
| 9 缺陷 | web/src/pages/Audit.tsx · detailText | JSON.parse 把 9007199254740993 展示成 9007199254740992 | 用 reviver source 保存数字词法值；不支持 source 的旧引擎对不安全数字明确提示，不显示伪精确值；组件和浏览器实测通过 |
| 10 缺陷 | web/src/pages/Audit.tsx · load/useEffect | StrictMode 首次迟到响应覆盖后续分页列表 | 请求序号控制成功/失败/收尾，卸载时作废；分页竞态回归通过 |
| 11 缺陷 | web/src/pages/ImagePricing.tsx · refresh/useEffect | 初次迟到列表覆盖刚保存的新价目 | 同一读取序号收敛，旧响应不发布；保存后新列表回归通过 |
| 12 风险 | internal/proxy/cost_budget_test.go · newBudgetServer | 测试创建的计量管道未关闭，遗留 goroutine/ticker | 按 server→pipeline→DB 顺序清理；聚焦及 race 全量通过 |
| 13 缺陷 | internal/proxy/metering.go · budgetObserveUsage/budgetExceeded | 正数输出 usage 到达后冻结预算，后续内容不再累计 | 以 usage 到达时的估算位置为检查点，累计之后增量；usage 后 20 帧预算中断回归通过 |
| 14 缺陷 | internal/proxy/proxy.go · handleChat；metering.go · record | 入口预检与在途落账跨价格，预算与金额不一致 | 经 owner“你自己决定”授权后统一入口有效价；启用预算/零预算/入口无价目三形态通过，计量设计及主规格同步 |
| 15 缺陷 | cmd/muniu/admins.go · readAdminPassword/adminTTYReader | 密码提示处 SIGINT/SIGTERM 留下关闭回显的终端 | 先接管信号再禁回显，同步 Poll 可取消读入、defer 恢复，不用阻塞 Close 等待；并限制读入 4096 字节；真实 PTY 四路径通过 |
| 16 缺陷 | internal/auth/admin.go · Create；admin_password.go · HashAdminPassword | CLI 接受无法由 JSON 无损表达的非 UTF-8 凭据，创建后无法网页登录 | 共用认证层拒绝非法 UTF-8；合法 Unicode 不变；CLI 输入回归先红后绿 |
| 17 缺陷 | internal/proxy/metering.go · record | 启用预算时正数 usage 后上游断流，仅记录旧累计值，漏掉之后生成的输出 | 中断估算路径复用 usage 检查点补齐后续输出；TestCostBudget_usage之后断流不得漏记先红后绿 |

## 技术裁决与兼容性

单一结论：chat 编排路径使用请求入口有效价目，预算与落账共用不可变快照。旧实现的完成时刻重读会让相同请求随运行期间改价而重新定价；这不是保持外部契约的充分理由，但改动必须获得授权。本轮已明确说明影响，owner 回复“你自己决定”后实施。

| 维度 | 原完成时刻读取 | 请求入口快照（采用） |
|---|---|---|
| 复杂度 | 预检/attempt/record 分别读取，跨阶段口径分离 | 在既有 meterState 保存 Pricing 值及是否已选定，候选共享；常数复杂度 |
| 性能 | 同一请求重复 Load/At；逐帧预算本身已有计数 | 一次 Load/At，无锁或数据库查询增加，复制既有小值对象 |
| 兼容性 | 运行期间改价或跨窗时按结束价 | API/schema/credit 精度不变；仅这些在途 chat 请求按入口价，新请求仍立即使用新价 |
| 测试 | 普通请求无法暴露时点差异 | fake upstream 在执行期间改价，验证预算非零/零/入口无价目；窗口选择算法复用既有测试 |
| 迁移成本 | 无迁移 | 无迁库、不重算历史账，恢复旧代码即可回到旧时点；设计与主规格同步 |
| 长期维护 | 预算与落账可再次各取一价 | 单份不可变快照，新增候选不增加选价点 |

chat、messages、responses、gemini 共用 handleChat；images 已有独立入口快照。embeddings 保持原完成时刻路径，真实业务边界保留。旧时刻方案不保留开关或额外双轨框架。

其余修复均落在责任层：images 精确解析、认证状态、审计收尾、客户端异步版本、TTY 输入；既有 failover/价格目录/计量管道/Radix 标准件继续复用，无新依赖、无存量 schema 变更。

## 验证证据

原始日志含失败与修复后输出，位于 [证据目录](2026-10-13-review-evidence/)。红绿对应：r1-red/r1-green；r2-red/usage-green；web-red/images-ui-red/web-green；utf8-red/r2-all-green；usage-eof-red/usage-green；tty-red/tty-final。

```text
PATH=/Users/solvela/.local/share/mise/installs/aqua-golangci-golangci-lint/2.14.0/golangci-lint-2.14.0-darwin-arm64:$PATH make check
0 issues.
Test Files  6 passed (6)
Tests  15 passed (15)
coverage gate PASS: 69.9%
make build：exit 0
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o /tmp/muniu-review-linux-arm64 ./cmd/muniu：exit 0
pnpm exec vite build --outDir /tmp/muniu-review-dist
✓ built in 3.62s
Some chunks are larger than 500 kB after minification.
openspec validate --all --strict
Totals: 22 passed, 1 failed (23 items)
```

首轮全量门禁在 TypeScript 检查失败：修复图片读取竞态时误把序号判断带入删除失败回调，引用不存在变量。已恢复删除错误展示；check.txt 保留原错误，final-check.txt 为最终全绿证据。未把一次失败隐藏为首次通过。

前端构建输出只写 /tmp，未覆盖 web/dist。体积警告（770.45 kB）保留，不扩为打包重构。业务裸 button/input/select/textarea 自检零命中，git diff --check 通过。

真实 PTY（tty-final.txt）：SIGINT、SIGTERM 各退出 1；一致双输退出 0、不一致退出 1；四路径恢复回显且无密码回显。长输入非 TTY 边界（4096/4097/100000 字节）单测通过。验证脚本只用独立临时配置/库，退出后删除。

独立 Vite 预览 :5175 使用 API 契约夹具完成登录/审计/图片价目 × 375/768/1280 × 亮暗，共 18 组；scrollWidth == innerWidth，pageerror 零条，大整数逐字准确。截图、browser-layout.json 保存。此轮验证的是组件布局和展示；上一轮真实 scratch 网关的登录/改价链路证据仍有效，不冒认真实厂商验收。既有 :8787 实例未操作。

## 排除的候选问题

- **非问题**：images_not_supported 返回 400、不换到另一个 provider；这是已冻结的端点边界，不能借评审改为新路由行为。
- **非问题**：native 流已开闸后不做预算中断；主规格明确登记这一保真边界，本轮不新增原生协议错误帧。
- **非问题**：ImagePriceDirectory.Get 的档位 map 供只读访问；Replace 深复制输入，当前所有消费点没有写入，未发现共享写竞态，不增加每请求深复制。
- **非问题/偏好**：登录页“用户名/密码”已按用户要求统一；本轮 CLI 内部“口令”用词不影响登录页，无额外全仓改名。

## 范围外发现与既有搁置

1. **已证实/缺陷，范围外待办**：internal/metering/price.go:95 · CostWithCached 的 int64 乘积仍会回绕。只读 go test -overlay 验证：输入 100000000 tokens、价 1000000000000 credit/M，得到 7766279631453，应为 100000000000000。原始输出见 outside-scope-cost.txt；源文件不在 110 文件冻结集合，未修改。后续应在计费责任层用宽乘积，明确最终金额超界的契约，并覆盖三段缓存价；本轮预算新算法已安全，但不能声称旧通用计费同样安全。
2. **既有严格规格失败**：openspec/specs/platform-data-plane/spec.md Purpose 占位，未在冻结变更集中；本轮仍 22/23，详细原文见 openspec-final.txt。受本轮更新影响的 pricing-management 验证通过。
3. **延续前轮搁置，未重试**：T5.3/T5.6 观察证据因容器/卷丢失中断；Gemini/MiniMax/images 真实厂商缺凭据。API key 模型白名单缺行为契约，仍待确认。详见前轮冻结报告，不以这些项目要求本轮继续巡检。
4. **Git 待指令**：所有变更保持未提交；未执行任何 Git 写操作。

本轮契约决策已获授权，无新的待确认事项。范围外问题已登记，冻结评审与验收结束。
