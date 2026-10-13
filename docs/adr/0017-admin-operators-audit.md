# ADR-0017:管理面本地管理员与操作审计

- 状态:已接受(2026-10-13;二次裁决把「控制台多用户」从 SSO 捆绑中拆出改判引入——共享令牌零归因在 standalone 一等形态下是真实完整度缺口;SSO/OIDC 本身维持不引入,论证入 HANDOFF 开放决策项)

## 决策

管理面身份从「单一共享 `MUNIU_ADMIN_TOKEN`」升级为「本地管理员账号 + 会话」双轨:

1. **本地管理员**:`admin_users` 表(username 唯一、argon2id PHC 串口令、软停用),单角色(管理员),不做角色矩阵/组织架构;管理经 API 与 `muniu admins` CLI 双形态,引导流 = token 轨或 CLI 创建首个账号。
2. **会话**:服务端表 `admin_sessions`(SHA-256 令牌哈希,12h 固定期),HttpOnly cookie;停用即删会话、改密全注销——服务端表而非无状态签名 cookie,因为签名 cookie 无法主动失效,「可追责」叙事要求即时踢出。
3. **双轨兼容**:现行 Bearer token 路径原样保留(CLI/脚本契约零破坏,401 信封形态不变),两轨均识别为具名 actor;挂载条件放宽为「token 已配置或存在活跃管理员」。
4. **登录硬化**:argon2id(m=19MiB/t=2/p=1,PHC 串参数可迁移)+ 恒时比较 + 同名连续失败 5 次锁 15 分钟(进程内存态,重启清零为登记边界)。
5. **操作审计**:`admin_audit_log` 只增不改,全量留痕管理面变更动作与登录成败(actor/action/target/outcome/脱敏 detail/request_id),`GET /admin/v1/audit` 游标分页;审计同步写,失败告警可观测。

## 理由

- **ADR-0005 自洽**:standalone 是一等公民形态,该形态下网关管理面即最终形态;one-api/Gitea/Grafana 同类全部具备本地管理账号与审计——「谁吊销了这把 key」必须可回答。
- **安全工程肌肉与既有纪律同缝**:恒时比较已是现行纪律,argon2id/会话硬化/审计链是其自然延伸;口令哈希不做自研构造,用标准 PHC 串。
- **依赖代价最小**:仅增 `golang.org/x/crypto`(argon2,纯 Go 叶子模块,内部仅依赖同模块 blake2b),与已入列的 `x/time` 同属 golang.org 半标准库,本轮 x/crypto v0.58.0 要求已有 x/sys 升至 v0.49.0，无新增传递模块——**白名单登记即此**(铁律 3 程序性动作)。

## 备选与否决

- 无状态签名 cookie(JWT 形态):无法服务端失效,与「停用即踢」冲突,且自研 JWT 解析是密码学危险动作,否决。
- 锁定计数入 DB:为多副本准备的过度设计(单进程即真相),否决;集群形态归 ADR-0008 域。
- 「最后一个管理员停用保护」:恢复路径恒存在(CLI 直连库/token 轨),伪保护徒增状态,否决。
- SSO/OIDC:组合冗余(锦官主场)+ standalone 零外联纪律 + 依赖白名单三重否决,维持不引入。

## 后果

- 迁移 0012(三表);`internal/admin` 双轨中间件与 auth/admins/audit 路由;`muniu admins` CLI;控制台登录页(双轨)+ 审计页;openapi 同步。
- `MUNIU_ADMIN_TOKEN` 语义降级为「引导/兜底轨」,长期保留不废弃(破坏性移除无对价)。
- openspec change `admin-operators-audit`(spec 基线:admin-operators 新能力)。
