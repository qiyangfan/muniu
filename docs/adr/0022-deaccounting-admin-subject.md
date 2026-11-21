# ADR-0022:木牛管理面去账号化——虎符 sub 为唯一管理员主体键

- 状态:已接受(2026-11-21);取代 ADR-0017 的「本地管理员账密 + 会话」身份来源部分,保留其「会话表 + 操作审计 + 共享令牌兜底」结构
- 上游依据:longzhong 单一账户权威(虎符,ADR-0010 于 hufu 仓),木牛只做 RP

## 决策

1. **删除本地账密轨**:`POST /admin/v1/auth/login`、argon2id 口令哈希/校验、进程内失败锁定、`POST /admin/v1/admins`(本地建号)与 `muniu admins add|passwd` 全部移除;`password_hash` 不再是任何登录凭据。
2. **主体键为虎符 sub**:新表 `admin_principals(subject PK,username,status,created_at,last_login_at)`,subject 为 RP 验签后的 `iss`+`sub` 规范串;`admin_sessions.user_id` 改为 `subject` 外键。username 只是展示名,同名不同 sub 并存互不抢占。
3. **OIDC 回调直接建取主体**:`LoginFederated` 以 sub 建/取主体,不再生成 uuid 影子 id,不再写 `external_subject`;停用主体拒绝登录,登出/停用撤销会话。
4. **零存量重建迁移(0021)**:生产从未建过本地管理员;旧库 `admin_users` 若含本地账密行,装配期启动闸指名行并拒绝启动,不静默丢行。旧表与影子 id 列保留但不读写(迁移只增不改)。
5. **保留**:`GET /admin/v1/auth/whoami|federation`、`/admin/v1/auth/oidc/start|callback`、`POST /admin/v1/auth/logout`、`GET /admin/v1/admins`、`PATCH /admin/v1/admins/{id}`(id 即 subject)、`GET /admin/v1/audit`;`MUNIU_ADMIN_TOKEN` 轨原样保留(CLI/脚本与虎符失联时的兜底)。

## 理由

- **单一账户权威**:三仓各自维护口令 = 三份凭据、三套锁定、三次泄露面;虎符集中签发与吊销,木牛只投影 sub。
- **减少攻击面**:删除口令校验即删除撞库、锁定绕过、PHC 解析面;控制台不再承载任何凭据输入。
- **命名即契约**:主体以 sub 而不是随机影子 id 标识,停用/审计/联调可直接对齐虎符用户,无需反查映射表。

## 备选与否决

- 保留本地账密作 break-glass:控制面失联时数据面照跑不需要人员登录,`MUNIU_ADMIN_TOKEN` 已覆盖脚本兜底;再留一套账密等于保留被删的攻击面,否决。
- 保留 external_subject 映射并新增 subject 列:双键并存需要持续同步且迁移期歧义,零存量前提下直接收敛,否决。

## 后果

- 迁移 0021;`internal/store/admin_users.go`(principals/sessions/启动闸)、`internal/auth/admin.go`(不再持有口令)、`internal/admin` 路由与审计 actor(`session`/`login` 仅作历史行兼容,新行只用 `federated`/`token`/`unauthenticated`)、`cmd/muniu admins`(list/disable/enable 按 subject)、控制台登录页(只剩「统一身份登录」与管理令牌两条)、openapi 与 `admin-operators` spec 同步。
- 依赖 `golang.org/x/crypto` 只被 argon2 使用,随口令轨删除退出 go.mod。
- 回滚语义:回滚到旧二进制需重新引入账密轨;会话 cookie 在迁移期整体作废(12h 短期凭据),重新登录即可。
