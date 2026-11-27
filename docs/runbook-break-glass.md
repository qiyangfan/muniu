# 木牛 break-glass 运行时修复 runbook

> 适用:控制面/网关不可用但必须修复运行时 SQLite 的受控场景。独立凭证、限时窗口、原因、
> 二次确认与审计;不是日常写路径,不嵌入控制台,不提供任意 SQL。

## 前置

1. 先尝试常规路径:管理 API / 本地控制台命令(`cache_purge`/`topology_reload`)/ 锦官命令队列。
2. **备份优先**:`./bin/muniu backup`(在线一致快照)或停实例后复制 `muniu.db`。
3. 构建独立 CLI:`make breakglass`(或 `make build`,同时产出 `bin/muniu` 与 `bin/muniu-breakglass`)。
4. 独立凭证文件(`0600`),内容:

```json
{
  "token_sha256": "<SHA-256(token) 的 hex>",
  "not_before": "2026-11-27T00:00:00Z",
  "not_after": "2026-11-27T02:00:00Z"
}
```

1. token 经环境变量注入,禁止写入命令行/文件:

```bash
export MUNIU_BREAK_GLASS_TOKEN='<一次性令牌>'
```

## 文档化操作

| 操作 | 用途 | 参数 | 行数硬顶 |
| --- | --- | --- | --- |
| `purge-expired-semantic-cache` | 清理过期语义缓存行 | 无 | 1000 |
| `purge-oidc-states` | 清理过期 OIDC state | `older_than_hours`(1..8760,缺省 24) | 1000 |
| `drop-buffered-usage` | 删除阻塞续传的计量缓冲毒丸行(**会丢失该批计量**) | `seq`(正整数) | 1 |
| `clear-key-cooldowns` | 清除指定 provider 本地 key 冷却 | `provider`(≤128) | 1000 |

查看清单:

```bash
./bin/muniu-breakglass -list
```

## 执行

```bash
./bin/muniu-breakglass \
  -db data/muniu.db \
  -credential /run/secrets/muniu-breakglass.json \
  -op purge-expired-semantic-cache \
  -reason "锦官不可用,清理过期缓存" \
  -max-rows 100
# 交互输入 yes 确认;自动化环境可显式 -yes(仍等同二次确认)
```

成功输出含影响行数与审计 ID:

```
已完成 purge-expired-semantic-cache：影响 12 行；审计 019...
```

## 回滚

- break-glass 修复与审计同事务,**审计写失败会自动回滚**。
- 误操作恢复:停实例 → 用前置备份覆盖 `muniu.db`(含 `-wal`/`-shm`)→ 重启。
- `drop-buffered-usage` 丢弃的计量行无法从备份以外恢复;执行前必须确认该批已被锦官接收或可接受丢失。

## 停止条件

- 凭证无效/过期/权限过宽:立即停止,重新签发。
- 查询存储失败:不要改用任意 SQL 客户端;先确认实例已停、文件权限与 WAL 状态。
- 影响行数为 0:核对时间窗口/参数,不要放宽时间窗重试掩盖问题。
