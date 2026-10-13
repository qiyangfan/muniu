# Tasks

- [x] 2.1 依赖白名单落地:`go get golang.org/x/crypto`(argon2)+ `make tidy` + ADR-0017 白名单行登记
- [x] 2.2 失败测试先行:argon2id PHC 往返(哈希/校验/参数迁移解析)+ 登录锁定状态机(阈值/锁定期/重启清零)+ 会话签发/校验/注销/过期惰性清理
- [x] 2.3 迁移 0012(三表)+ store 读写(账号/会话/审计)+ 停用改密的会话即时清理
- [x] 2.4 失败测试先行:双轨中间件——会话轨通过/令牌轨零回归(既有管理面测试零改动)/双 actor 注入/401 信封形态不变/挂载条件放宽(token 或活跃管理员)
- [x] 2.5 失败测试先行:审计中间件——变更动作归因/敏感字段脱敏/GET 不留痕/登录成败与锁定拒绝入审计/游标分页稳定
- [x] 2.6 管理面路由:auth 三件套(login/logout/whoami)+ admins CRUD + audit 查询;路由表与漂移门禁同步
- [x] 2.7 CLI:`muniu admins` 子命令族(add tty 双次口令/stdin、list、disable、enable、passwd)
- [x] 2.8 控制台:登录页双轨 + whoami 探测路由 + 顶栏身份/退出 + 审计页(openapi 同步 + generate:api;标准件,移动范式)
- [x] 2.9 文档:data-and-config 三表 schema + 管理面身份节(ADR-0017 为纲)+ muniu.example.yaml 注记
- [x] 2.10 `make check` 全绿 + web 四门禁 + 独立 review;archive + 主 specs 同步
