# 动态价目(运行态覆盖 + 分时段倍率)能力基线

## Why

厂商改价是运维动作,不应触碰配置中心更不应重启;峰谷计价(如 DeepSeek 错峰半价)以单列价目无法表达。本变更将「价目」定性为运营态数据(ADR-0004 分工边界:静态拓扑走 YAML、运营态走 SQLite),并已在 2026-09-11 完成实现与验收(HANDOFF 验收记录 13/14)。本提案将该能力固化为 OpenSpec 基线,补齐 specs 空库的首批能力契约。

## What Changes

- 新增 `model_pricing` 表(迁移 0004)作为运行态价目的持久层,YAML `pricing` 段降级为静态兜底(按别名回落)。
- 新增管理 API:`GET /admin/v1/pricing`(列表,标注来源 db|yaml)、`PUT /admin/v1/pricing/{alias}`(在线改价,先校验后持久化再刷新,即时生效零重启)、`DELETE /admin/v1/pricing/{alias}`(撤销覆盖,回落 YAML)。
- 新增分时段倍率:`pricing.timezone`("UTC" 或 ±HH:MM 固定偏移)+ `time_windows[{window, multiplier}]`(跨午夜窗、先声明先匹配、加载期换算保守 ceil、热路径零浮点查表)。
- 新增 `PricingDirectory` 原子热替换目录:转发只读快照,管理面改价后下一请求即新价。
- 控制台新增「价目管理」页(列表/编辑/时段窗行文本编辑/撤销覆盖);openapi.yaml 同步并重新生成前端类型。

## Capabilities

### New Capabilities

- `pricing-management`: 运行态价目的覆盖/回落语义、管理 API 契约、校验纪律与热路径隔离。
- `pricing-time-windows`: 分时段倍率窗的解析、匹配语义、换算纪律(基点/保守 ceil/固定偏移时区)。

### Modified Capabilities

(无——openspec/specs/ 当前为空库,本变更为首批能力契约。)

## Impact

- 代码:`internal/metering/pricing_window.go`、`pricing_directory.go`;`internal/store/pricing.go` + 迁移 0004;`internal/admin/pricing.go`;`cmd/muniu/pricing.go`;`internal/proxy`(Config.Pricing → 目录);`web/src/pages/Pricing.tsx`。
- 契约:`contracts/openapi.yaml`(新增 3 端点 + PricingView/PricingWindow schema,路由漂移门禁通过)。
- 实现状态:全部完成,`make check` 全绿(2026-09-11);本变更任务清单以完成态归档,供 archive 同步进主 specs。
