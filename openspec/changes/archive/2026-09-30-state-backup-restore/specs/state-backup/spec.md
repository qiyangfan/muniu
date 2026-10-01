# state-backup spec delta(2026-09-30-state-backup-restore)

## Purpose

standalone 形态的 muniu 把全量运营态(key、用量账本、价目)存在单个 SQLite 文件里;本能力给出状态备份与恢复契约:备份是在线一致快照 + 配置副本 + 可校验的 manifest,恢复是带前置闸与失败原子性的离线动作。目标是「一条命令备份、一条命令恢复」,与单二进制运维叙事闭环。

## ADDED Requirements

### Requirement: 备份产物构成

`muniu backup` SHALL 产出单目录,包含三项:库的一致快照(在线可执行,不阻塞服务读写)、备份时刻配置文件的副本、manifest 文件。manifest SHALL 记录:muniu 版本、schema_migrations 版本、备份时刻(UTC)、api_keys 行数、usage_logs 行数、快照文件 sha256;manifest MUST NOT 含任何明文敏感值(密钥明文不在库中,备份自然不含)。

#### Scenario: 备份产物就绪

- GIVEN 一个已有 key 与用量记录的 standalone 实例
- WHEN 执行 `muniu backup`
- THEN 产出目录含快照 db、配置副本、manifest;manifest 的行数与哈希与实际快照一致

#### Scenario: 运行中备份不阻塞

- GIVEN muniu 正在服务且有并发请求
- WHEN 执行 `muniu backup`
- THEN 备份完成且服务请求不受影响(无长时阻塞、无错误率上升)

### Requirement: 完整性自检

备份 SHALL 在打包前对快照执行 `PRAGMA integrity_check`;校验失败 SHALL 删除该次产物并以非零码报错,MUST NOT 残留可用外观的坏备份。manifest 的 sha256 SHALL 与快照文件实际内容一致。

#### Scenario: 坏档不落地

- GIVEN 快照过程产出损坏文件(故障注入)
- WHEN 完整性自检失败
- THEN 该次备份产物被删除,命令非零退出,现场无残留目录

### Requirement: 恢复前置闸

`muniu restore` SHALL 依次检查三个前置条件,任一不满足 MUST 拒绝执行并保留现场:目标实例未在运行(目标库文件被占用即拒);目标路径已存在时未显式传 `--force` MUST 拒覆盖;恢复完成后 SHALL 执行 integrity_check 并与 manifest 核对行数,不一致 MUST 拒收(不替换目标)。

#### Scenario: 目标被运行中实例占用

- GIVEN muniu 正在以目标库文件运行
- WHEN 执行 `muniu restore`
- THEN 拒绝并在消息中说明实例占用,目标文件零改动

#### Scenario: 目标已存在需显式确认

- GIVEN 目标路径已有库文件且未传 `--force`
- WHEN 执行 `muniu restore`
- THEN 拒绝覆盖,提示使用 `--force`

#### Scenario: 恢复后校验不符拒收

- GIVEN 备份 manifest 记录的 usage_logs 行数与快照实际不符(坏档)
- WHEN 执行 `muniu restore --force`
- THEN 完整性或行数校验失败,恢复被拒,目标保持原样

#### Scenario: 正常恢复 roundtrip

- GIVEN 一份通过完整性自检的备份
- WHEN 对未运行实例执行 restore 至目标路径
- THEN 目标库 integrity_check 通过,且 key/用量/价目与 manifest 记录一致
