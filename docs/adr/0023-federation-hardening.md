# ADR-0023:木牛联邦登录加固——白名单、浏览器绑定与状态落库

- 状态:已接受(2026-11-21);补充 ADR-0022,不改变「虎符 sub 为唯一管理员主体键」
- 背景:去账号化后默认行为是「任何通过虎符认证的 sub 首次登录即建 active 主体」,等于把虎符全体用户变成木牛管理员;state 仅存进程内且未绑定浏览器,重启丢登录、回调 URL 可被转交完成登录 CSRF;登出只删本地会话,虎符会话仍有效。

## 决策

1. **sub 白名单**:`admin_oidc.admin_subjects` 为必填列表,与 issuer/client_id/redirect_url 成套声明;名单外 sub 在 `LoginFederated` 入口即拒绝,不进入建号路径。空列表视为配置不完整,加载期拒绝。
2. **state 浏览器绑定**:发起授权时下发 `muniu_oidc_state`(HttpOnly、SameSite=Lax、Path=/admin/v1/auth/oidc、10 分钟);回调以恒定时间比较校验,无论成败即刻清除。
3. **state 落库**:新增 `oidc_pending_states(state_hash PK, verifier, nonce, created_at)`,state 只存 SHA-256;单次取出即删除,启动与每次回调顺带清理过期行。RelyingParty 通过 `StateStore` 接口注入,单测仍可用进程内实现。
4. **统一登出**:新增 `POST /admin/v1/auth/logout/sso`,删除本地会话并返回虎符 `end_session` 地址(client_id + 已登记回跳);权威不可达时退回 `/console/`,不留「本地已退、上游仍活」的静默状态。
5. **Cookie Secure 由部署表达**:管理面 cookie 的 `Secure` 不再看 `r.TLS`(反代终止 TLS 时恒为 nil),改读顶层 `admin_cookie_secure`;缺省按 `admin_oidc.issuer` 是否 https 推导,显式配置优先。
6. **登录结果落审计**:回调是 GET、不经过管理面变更审计,因此成功与拒绝路径都显式写 `auth.oidc.login`(actor_kind=login,target=sub,detail 只记 provider 与固定原因码)。统一登出审计按完整子路径记 `auth.logout.sso`,有效会话归因 federated/实名,无会话记 login/anonymous。
7. **停用主体重新启用**:保留 PATCH 启用语义——启用本身是管理员显式动作,但旧会话不恢复,重新登录仍须经虎符认证。
8. **登录入口有界**:`start` 与 `callback` 按客户端 IP 限流(进程内 30 次/分钟,多副本为下限而非全局配额);state 写入时顺带删除超过 TTL 的存量,连续发起但不回调也不会让表无限增长。

## 后果

- 部署必须新增 `admin_oidc.admin_subjects`;`post_logout_redirect_url` 可选,缺省按 redirect_url 同源推导 `/console/`,且须在虎符 client 登记。反代终止 TLS 时须置 `admin_cookie_secure: true`(issuer 为 https 时缺省已开启)。
- 迁移 0022;控制台退出按钮改走 SSO 登出并整页跳转。
- 回滚:旧二进制不识别 state cookie 与新路由,需要重新登录;白名单与 state 表只增不改,回滚不丢数据。
