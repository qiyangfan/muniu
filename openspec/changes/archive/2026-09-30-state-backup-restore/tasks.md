# 2026-09-30-state-backup-restore 实施任务

## 1. 测试先行(铁律 2:存储一致性域先红)

- [x] 1.1 失败测试:backup→restore roundtrip——api_keys/pricing/usage_logs 三处行数与内容一致(临时目录 fixture)
- [x] 1.2 失败测试:WAL 未 checkpoint(写入后不合并)时备份,恢复后数据不丢不旧(快照一致性)
- [x] 1.3 失败测试:manifest 行数与快照实际不一致 → restore 拒收,目标保持原样
- [x] 1.4 失败测试:目标已存在未传 `--force` → 拒覆盖,源文件不动
- [x] 1.5 失败测试:目标库被另一连接占用(锁探测)→ 拒恢复
- [x] 1.6 失败测试:integrity_check 失败路径(坏档 fixture)→ backup 删档报错、非零退出、无残留产物

## 2. 实现

- [x] 2.1 `internal/store/backup.go`:快照(VACUUM INTO,独立连接 + 目标预检)、integrity_check、行数统计、manifest 组装纯函数(design D1/D2/D4)
      **验证**:1.1、1.2、1.6 转绿
- [x] 2.2 `cmd/muniu/backup.go`:子命令分流(沿用 keysSubArgs 模式)、`--output`/`--force` 参数、配置定位复用 flagConfOf、退出码语义(0 成功 / 非零分类)
      **验证**:1.3、1.4 转绿
- [x] 2.3 restore 实例占用探测:开目标文件排他锁,占用即拒(design D3)
      **验证**:1.5 转绿;1.3、1.4 复验

## 3. 收尾

- [x] 3.1 `make check` 全绿(vet + lint + 全包 race)
- [x] 3.2 手工冒烟:对运行中的 muniu(:8787 在服,`data/muniu.db` 有真实行)执行 backup → restore 至临时路径 → 行数与 manifest 一致;原始输出贴 HANDOFF 验收记录区
- [x] 3.3 文档:README 快速开始补「备份与恢复」两行;`docs/design/data-and-config.md` 状态持久化段补备份口径;HANDOFF 更新(状态 + 验收记录,≤30 行纪律)
