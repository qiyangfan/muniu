# Tasks

## 1. 存储与配置契约

- [x] 1.1 迁移 0019 `oauth_accounts` 建表 + store 账号访问器(增改查删/状态迁移/引用计数)。验证:建表迁移往返测试(写入→读回逐字段一致)+ 迁移号顺序断言
- [x] 1.2 【测试先行】config:`KeyRef.Account` 成员(与 Env 互斥)+ `auth` 值域 `""|bearer|oauth` 单源校验。负面用例:非法 auth 值文案含全值域 / oauth 渠道引用不存在账号 / 非 oauth 渠道带 account 成员 / 非 anthropic 适配器声明 oauth / 互斥成员双声明。验证:表驱动 config.Load 失败用例全绿
- [x] 1.3 example yaml 补 OAuth 声明样例与注释。验证:example 合同测试走真实 config.Load

## 2. OAuth 账号域(internal/oauth)

- [x] 2.1 【测试先行】授权流:PKCE(S256+crypto/rand)consent URL 生成 + state 一次性 + 回调换 token 落库。测试:state 重放拒绝 / code 换 token 失败透传 / PKCE verifier 形态。验证:单测全绿
- [x] 2.2 手动贴 token 调试通道(管理端点直接录入凭据对,校验过期形态)。验证:端点集成测试落库读回一致
- [x] 2.3 【测试先行】刷新器:过期前窗口定扫 + 单飞由单 goroutine 顺序 tick 构造保证(无并发入口);401/403/400 invalid_grant → `auth_failed` 终态不重试;网络/5xx 留待下 tick 重探。判定表测试(成功/401/403/400 invalid_grant/5xx/缺 RT 回退/缺 expires_in 零回写/终态跳过)。突变验证:终态判定短路须变红
- [x] 2.4 管理端点(列表/创建调试录入/删除含引用拒绝/授权发起/回调;详情由列表承担,不设独立端点)挂 admin mux + openapi/api-types 同步(漂移门禁)+ 审计接入。验证:未鉴权 401 / 引用中删除被拒列出引用方 / 漂移门禁绿

## 3. keypool 集成

- [x] 3.1 resolver 闭包装配(cmd 层把 store 账号访问器交给池装配;config 不 import store)。验证:装配集成测试 + 包依赖方向 vet(grep 禁 import 断言)
- [x] 3.2 池成员账号形态:出站前解析 token(原子缓存),解析失败按不可用收缩、空凭据零出站;刷新热替换下一请求生效。验证:httptest 假上游断言新 token 头 + 收缩场景测试
- [x] 3.3 【测试先行】调度等价回归:混编账号与静态 key 同池 WRR/冷却无特例;欠费白名单负面(订阅 usage limit 429 不进欠费桶,走 ClassRateLimited)。突变验证:白名单误收须变红
- [x] 3.4 亲和协同回归:OAuth 渠道会话粘住与 ADR-0020 语义无冲突(worth 闸门/失败即清复用)。验证:既有亲和测试套对账号池成员跑通

## 4. 出站 lane

- [x] 4.1 【测试先行】anthropic 适配器 auth 三态分支(oauth = Bearer + 官方 beta 头常量单点;UA 不覆写)。测试:三态头形态断言(oauth 头集/bearer/缺省 x-api-key)。验证:单测全绿
- [x] 4.2 【测试先行】lane 收窄:responses/gemini/images/embeddings 命中 oauth 渠道返回显式信封、上游零调用(请求期必拒;加载期静态路由尽力拒)。负面测试四 lane。突变验证:收窄判定移除须变红
- [x] 4.3 chat/messages e2e(httptest 假上游 OAuth 全链:头形态/计量 cost=0/亲和命中/欠费路径)。验证:e2e 测试绿

## 5. 控制台

- [x] 5.1 账号页(列表/状态徽章/删除 AlertDialog/手动录入/授权发起)按标准件(DataList/StatusBadge/ErrorAlert),移动端 DataList 卡片范式。验证:三档 375/768/1280 × 亮暗走查
- [x] 5.2 web 门禁五件套 + vite build(/tmp)+ 裸元素自检。验证:全绿

## 6. 门禁与诚实边界

- [x] 6.1 `make check` 全绿(vet + lint + race + coverage gate)+ `openspec validate --all --strict`。验证:EXIT=0 证据
- [x] 6.2 突变验证汇总(欠费白名单/刷新终态/lane 收窄三项红复绿记录)。验证:各突变测试红→复绿证据
- [x] 6.3 真机冒烟挂账:文档披露 refresh token 明文落库与备份含敏感凭据;真凭据到位前的验证边界如实登记(不声称已验证)。验证:文档与管理面披露文案在 diff 中可查

## Workflow follow-up

- 评审迭代循环(独立子代理双域评审)按惯例待 owner 点头后走
- openspec archive + 主 specs sync 待实施完成、owner 确认后执行
- git 提交等 owner 逐次指令
