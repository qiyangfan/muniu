# 2026-09-30-state-backup-restore 设计要点

## D1 为什么用 `VACUUM INTO` 而不是驱动 backup API

modernc.org/sqlite(v1.58,依赖白名单内)同时暴露两条路:`VACUUM INTO '<path>'` 纯 SQL,与 `(*sqlite.Conn).Backup` 驱动级 API。选前者:

1. **零驱动特定 API**:`VACUUM INTO` 经 `database/sql` 即可执行,不 requires 从 `*sql.DB` 下沉拿 driver conn(`Raw()` 转型)——将来若换 sqlite 实现(白名单演进),备份代码不用重写。
2. **自带一致性**:SQLite 官方文档明确 `VACUUM INTO` 是 backup API 的等价替代,WAL 模式下产生一致快照。
3. **约束可管理**:不能在事务内执行、目标文件必须不存在——由实现层保证(独立连接执行 + 目标预检删拒),不构成障碍。

## D2 在线一致性论证

库以 WAL 打开(`store.Open` 的 DSN 固定 pragma)。`VACUUM INTO` 在源库上是单条语句级读事务,快照内容 = 语句开始时刻的库状态(含已提交的 WAL 帧)。运行中执行不阻塞读写;备份 weakly-consistent 边界 = 语句开始时刻,对「事后对账 + 容灾」语义足够(快照内的 usage 行自洽,不要求与备份后到达的写入一致)。busy_timeout=5s 已配,极端并发下的 SQLITE_BUSY 由重试一次吸收。

## D3 restore 为什么要求实例未运行

单文件库的「在线替换」是与自己竞态:运行中的 muniu 持有旧库句柄与连接池,替换文件后旧句柄继续写旧 inode,数据分叉且不可预期。因此 restore 的前置闸是**探测目标库是否被占用**(开目标文件排他写锁,失败即判定占用中拒恢复)。这不是能力缺失,是把竞态可能性从设计上消除——restore 是运维动作(停机→恢复→起服),不需要做成热切换。

## D4 manifest 为什么记行数与哈希

两个用途:恢复时的**完整性核对**(快照行数 vs 源 manifest 行数,不一致即坏档拒收)与对账时的**锚点**(usage_logs 行数是月末量级核对的本地分母,T5.3 判据的物质载体)。sha256 覆盖快照文件本体,防静默损坏(磁盘/拷贝)。manifest 不记任何明文敏感值。

## D5 失败原子性

- backup:integrity_check 失败 → 删档报错,目录里不留半个坏产物(半成品备份比没有备份更危险——它会被信任)。
- restore:任一步失败(占用探测/覆盖确认/校验不符)→ 目标路径保持原样,现场保留供人工排查;`--force` 覆盖前先验证通过,验证不过不覆盖。

## D6 cluster 边界

ADR-0005:standalone 是一等公民,cluster 形态的副本无状态、控制面在 jinguan(PG 权威,PG 有成熟备份生态)。本能力显式只服务 standalone;cluster 部署下执行 backup 无意义(副本本地只有计量缓冲),文档写明不拦——但 spec 契约以 standalone 为限。
