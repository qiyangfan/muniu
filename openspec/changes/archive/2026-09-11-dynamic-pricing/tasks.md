# 任务清单:动态价目

> 实现已于 2026-09-11 完成并验收(`make check` 全绿,证据见 HANDOFF 验收记录 13/14)。
> 本清单以完成态归档,供 archive 将 specs 同步进主 specs 库;未勾选项为无。

## 1. 倍率引擎(metering)

- [x] 1.1 `ParseMultiplier`(基点,≤4 位小数,(0,10],零浮点)与 `ParseWindowMinutes`(跨午夜/24:00 终点)与 `ParseZoneOffset`(UTC/±HH:MM 固定偏移),负面表驱动测试先红后绿
- [x] 1.2 `ScalePrice`(保守 ceil + 溢出防护)与 `PricingTable.At`(先声明先匹配/时区换算/缺省回落),契约测试 8 组
- [x] 1.3 `PricingDirectory`(Get/Replace/Aliases,nil 目录安全)与 `FormatPrice`/`FormatMultiplier` 恒等往返测试

## 2. 配置与装配

- [x] 2.1 config:`timezone`/`time_windows` 字段与聚合校验(一次列全;引 metering 解析器,无循环依赖)
- [x] 2.2 装配层:静态价目构造 + `buildPricingTables` 合并(YAML 兜底 + `model_pricing` 覆盖),合并语义测试(覆盖/追加/回落/脏数据拒绝)

## 3. 运行态覆盖与 API

- [x] 3.1 迁移 0004 `model_pricing` 表 + store CRUD 测试(覆盖/窗口 JSON 往返/时区缺省)
- [x] 3.2 `GET/PUT/DELETE /admin/v1/pricing`(校验整单拒绝、持久化后刷新、删除回落 404),全流程测试
- [x] 3.3 控制台「价目管理」页 + openapi.yaml 同步 + api-types 重新生成(路由漂移门禁通过)

## 4. 热路径接线与端到端

- [x] 4.1 proxy 计量接线改为价目目录(转发只读快照),`PricingDirectory.Get` nil 安全
- [x] 4.2 端到端钉子:全天半价窗 cost_micro 86μ→43μ;`make check` 全绿(vet+lint 0 issues+web+20 包 race)
