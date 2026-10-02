# 提案评审

- 状态：提案已编写，尚未更新 API 声明、生成类型或 UI；实施任务全部未勾选。
- 契约核对：真实 ID 已存在；签发响应的 `tenant` 与列表 `tenant_id` 是既有字段差异，本轮不重命名。
- 兼容性核对：平台字符串 ID 与本地 UUID 并存；secret 前缀不能作为 ID 校验或替换依据。
- 范围判断：复用现有展示、复制与绑定链路；不新增 secret 反查或跨控制台通信，不接真实支付渠道。
- 提案验证：结果在 HANDOFF 中记录；实现阶段须完成契约、组件及跨仓归账验收。

## 本轮验证（2026-10-02）

- 本件及本轮全部 7 件 change 的 OpenSpec 严格校验通过；对应 api-key-lifecycle 的临时预合并规格严格校验通过，未写入主规格。
- MODIFIED 原 Requirement/Scenario 名称保留检查、局部文档链接、任务未勾选、文档变更范围及 `git diff --check` 均通过。
- 只读方案复核已完成，必要修正写回设计、规格和实施回归；收款继续 Mock。
- 本轮没有业务代码、依赖或迁移变更，未运行编译、业务测试、UI 实机或打包验收，不把提案通过记成实现完成。

## 实施轮验证(2026-10-02)

- 契约(1.1):`contracts/openapi.yaml` 新增 `KeyCreateResponse`(8 字段全 required);`internal/admin/admin_test.go` 新增双向契约回归——handler 实际输出键集 ≡ schema 声明属性集,并钉死 id≠secret≠prefix、创建响应租户字段保持既有名 `tenant`(不重命名,与列表 `tenant_id` 差异如实保留)。
- 类型(1.2):`pnpm generate:api` 再生成,`api.ts` createKey 改用生成类型,手写 `{key,prefix}` 形状退役。
- 管理面(2.1/2.2):新共享组件 `components/copy-button.tsx`(剪贴板+execCommand 兜底,成功/失败反馈,disabled 语义;Providers 复制收编同源,宪法 Article 4);Keys 列表新增 Key ID 列(截短+title 全值、复制取完整原值,缺 ID 显式「ID 不可用」+禁用复制)、签发横幅附 Key ID 复制与绑定提示;引入 vitest+jsdom+@testing-library/react(白名单外,已登记 HANDOFF 开放项),组件测试 6 例全绿并接入 Makefile/CI 门禁。
- 跨仓验收(3.1):活实例实录——UUID key 与直插自定义 ID key(platform-key-1)经锦官 `/admin/key-bindings` 绑定、请求经网关、用量归账 keyId 与签发/插入 ID 逐字一致;CLI `keys list` 已展示真实 ID,无需新入口。注意:锦官侧同名 change(表单文案)未实施,本验收基于其已提交的绑定链路行为。
- 门禁(3.2):`make check` 全绿(20 包 race,coverage 68.4%);`openspec validate --all --strict` 通过;独立子代理代码审查:1 高(.gitkeep 被构建清空,已恢复)+3 低(加载期空态闪现已修、act 告警登记、依赖登记)全处置;浏览器走查 375/768/1280×亮暗+键盘:走查返工三项——签发表单列梯 6→4 列(datetime 控件 165px 被挤爆)、Key ID/租户列桌面截短(表格 min-content 1393→1104 恰合容器,1280 零滚动)、ui/input 日期族 display flex→block(Chromium 下原生 日历指示器随内容排布不贴右缘,像素级验证图标列簇 239-250 紧贴右内缘);总览图表补空数据态(禁静默空白,文案仅用户事实)。
- 归档(3.3):muniu 侧 7/7 完成,HANDOFF 已记录;锦官侧表单文案归其同名 change。
