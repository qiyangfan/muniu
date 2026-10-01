# 2026-09-30-state-backup-restore 提案:standalone 状态备份与恢复

## Why

standalone 形态的全量运营态住在单个文件里:`data/muniu.db`(内嵌 SQLite,WAL 模式)。已核实的丢失面:

- `mk-` key 仅 SHA-256 落库,明文「仅此一次展示」(README 快速开始原话)——库丢了没有任何途径恢复已签发 key,全部作废重签;所有下游客户端(kongming `.env` 的 `KM_AGENT_LLM_API_KEY`、Claude Code 的 `ANTHROPIC_AUTH_TOKEN` 等)同时失效。
- `usage_logs` 是计费与对账账本(credit 计量、分时段倍率、动态改价的落点)——丢了即历史清零,v1 收口判据里的「月末上游账单量级核对」失去本地锚点(T5.3 灰度判据修订原文即依赖此账本)。
- pricing 是运营态(`PUT /admin/v1/pricing` 在线改,零重启)——丢了回出厂缺省,用了一路的价目表要重录。

当前备份手段为零:HANDOFF 与本仓文档无任何备份登记;`cmd/muniu` 仅有 `keys`/`pricing` 两个管理子命令。单二进制哲学下「一条命令起服」,状态恢复却要先读懂 SQLite 再手工拷文件,运维叙事不闭环。v1.0 收口判据是生产承载观察 + 对账——账本不可备份,是收口前最后一个运维缺口。

## What Changes

1. **`muniu backup` 子命令**:在线一致快照 + 打包。对运行中实例亦可执行(WAL 模式下 `VACUUM INTO` 语义取一致快照,不阻塞读写,design D2);产物为单目录:快照 db + 备份时刻的 muniu.yaml 副本 + `manifest.json`(muniu 版本、schema_migrations 版本、备份时刻 UTC、api_keys 行数、usage_logs 行数、快照文件 sha256)。
2. **完整性自检**:快照后立即 `PRAGMA integrity_check`,失败即删档报错,不产出坏备份;manifest 记录内容哈希备恢复侧校验。
3. **`muniu restore` 子命令**:恢复到指定路径(缺省同配置定位的 `storage.sqlite.path`)。前置硬闸三条——目标实例 MUST NOT 在运行(探测目标库文件占用,运行中拒恢复,design D3);目标已存在且未传 `--force` MUST 拒覆盖(防误操作);恢复后跑 integrity_check 并与 manifest 核对行数,不一致拒收并保留现场。
4. **配置定位复用**:backup/restore 常驻配置路径解析与 `keys` 子命令同机制(`flagConfOf`),从配置取库路径;`VACUUM INTO` 是纯 SQL 语句,零新增依赖(不动依赖白名单,不引 backup API,design D1)。

## Non-goals

- **定时/自动备份**:muniu 无常驻之外的调度器,cron/launchd/Time Machine 是 OS 层既有答案;单人本地项目内置调度器是无移动件的移动件。
- **云端同步/多版本历史**:单目录快照,历史保管与保留策略归调用方(扔进任何同步盘即可)。
- **加密**:备份的敏感面与 db 本身同等(含 key 哈希与 usage,不含明文 key);给本地文件加口令加密,收益是防同步盘泄露,属威胁模型驱动的独立议题。
- **cluster 形态**:无状态副本没有本地状态;控制面在 jinguan(PG 权威),PG 备份属 jinguan 既有运维面——本变更只服务 standalone(ADR-0005 边界)。
- **管理 API/控制台备份入口**:CLI 优先;管理面触发是纯增量,按需再排。
- **增量/差异备份**:全量快照在单人规模以秒计,复杂度与收益不成比例。

## Capabilities

### New Capabilities

- `state-backup`: standalone 状态备份的内容边界(快照/配置/manifest)、在线一致性语义、完整性校验契约、恢复的前置闸(实例未运行/覆盖确认/行数核对)与失败原子性(坏档不残留、恢复失败保留现场)。

### Modified Capabilities

(无——新能力域独立成 spec。)

## Impact

- 代码:`cmd/muniu`(新增 backup 子命令,沿用 `keysSubArgs` 同款分流与 `flagConfOf` 配置定位)/`internal/store`(新增 backup.go:快照、integrity_check、行数统计、manifest 组装的纯函数)/`docs/design/data-and-config.md`(状态持久化段补备份口径)。
- 文档:README 快速开始补「备份与恢复」两行;HANDOFF 验收记录随 apply 落。
- 风险面:纯加法;不配既不影响起服与转发,既有测试零改动通过为闸门(与 gateway-parity 同款口径)。
