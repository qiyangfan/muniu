# Tasks

> 测试先行域:余额源解析器(2.2–2.6,httptest 假上游 fixture 先红)、燃速估算与整数运算(3.1)、告警状态机(3.2,表驱动)。每任务收尾 `go test ./internal/...` 该包全绿;阶段收尾 `make check`。

## 1. 基座:迁移与配置

- [x] 1.1 迁移 `0015_balance.sql`(provider_balances / balance_alert_states 两表,见 design D3)+ store 方法骨架;验证:`go test ./internal/store/ -run Balance` 真库(t.TempDir)打开即迁移,断言两表存在且列齐全
- [x] 1.2 config:`balance` 顶层节 + `providers[].balance`(source/below/凭证引用)与全部校验(kind 值域、档位降序正值、签名源缺凭证引用拒绝、below 十进制合法、错误攒齐一次报全);**失败测试先红后绿**;验证:`go test ./internal/config/ -run Balance` 全绿
- [x] 1.3 `muniu.example.yaml` 同步 balance 节与 provider 示例(含 `${VAR}` 凭证引用注释);验证:example 全文经 `config.Load` 的契约测试通过(`go test ./internal/config/`),值域注释与代码一致

## 2. 余额源查询器(internal/balance)

- [x] 2.1 kind 注册表 + `Querier` 接口 + `Balance{Credits, Currency, Unavailable}` 类型;非法 kind 构造期报错;验证:`go test ./internal/balance/ -run Registry`
- [x] 2.2 `deepseek`/`moonshot`/`siliconflow` 解析器(httptest 假上游,官方响应 fixture 先红):deepseek 多币种数组取目标币种 total_balance、moonshot 数值型 49.58894 无浮点渗入、siliconflow 取 totalBalance 弃 balance;失败路径(非 2xx / 字段缺失 / 类型不符)不产生部分余额;验证:`go test ./internal/balance/ -run 'DeepSeek|Moonshot|Siliconflow'`
- [x] 2.3 `openrouter`:limit−usage 求差;`limit: null` → Unavailable(余额不可知、不发告警);验证:`go test ./internal/balance/ -run Openrouter`
- [x] 2.4 `billing-compat`:subscription+usage 两端点串联求差(hard_limit_usd − total_usage/100);单端点失败整体失败;金额 > 1e7 USD 量级护栏判失败;验证:`go test ./internal/balance/ -run BillingCompat`
- [x] 2.5 `aliyun-bss` RPC HMAC-SHA1 签名器:固定 AK/SK 下 httptest 收端断言 form 参数(Signature 可重算对拍、Timestamp UTC、Nonce 唯一);成功解析 AvailableAmount;错误 Code 分类为凭证/权限类;凭证值不出现在错误串;验证:`go test ./internal/balance/ -run Aliyun`
- [x] 2.6 `volc-bss` Signature V4 签名器:收端断言 Authorization 头五要素与 payload 哈希;解析容错(找到余额数值字段,字段形态登记待真机实证);验证:`go test ./internal/balance/ -run Volc`
- [x] 2.7 keypool 只读枚举出口(keys + 禁用标志,不触 Pick/Report 不扰动 WRR/冷却);验证:`go test ./internal/keypool/ -run Snapshot` + 既有全量零改动回放(`go test ./internal/keypool/`)

## 3. 估算与告警状态机(纯函数,测试先行)

- [x] 3.1 燃速估算器:窗口日耗聚合(store 查询,estimated=0 过滤)+ 均值/最小样本(<2 天=数据不足)/零消耗=无限;剩余小时纯整数运算与溢出钳制(余额上界直判高于全部档位);币种≠结算币种禁估算;**表驱动失败测试先红**;验证:`go test ./internal/balance/ -run Estimate && go test ./internal/store/ -run BurnWindow`
- [x] 3.2 告警状态机 `evaluate`:档位穿越单次发射、迟滞 1.2 rearm、多档直落逐档发射、below 独立于档位与估算、重启状态恢复不重复;severity 缺省映射;**表驱动失败测试先红**;验证:`go test ./internal/balance/ -run Evaluate`

## 4. 评估循环与装配

- [x] 4.1 评估循环(revocationLoop 范式):启动即评一轮、interval tick、渠道错峰、单渠道 panic recover 隔离、refresh 信号与 tick 合流、Close 优雅停止(在途取消、状态落库);验证:`go test ./internal/balance/ -run Loop`(注入 clock + httptest)
- [x] 4.2 失败容忍:连续失败达阈值单次发 `balance_source_error`、不重复、成功清零并解除;快照失败保持旧快照;验证:`go test ./internal/balance/ -run SourceError`
- [x] 4.3 通知载荷:事件键 `balance_below:<provider>` / `balance_tier:<provider>:<h>h`、payload 携带渠道名+余额/估算上下文、经 Notifier 发射与去重;webhook-alerting 增量场景对号;验证:`go test ./internal/balance/ -run Emit` + `go test ./internal/alerting/`
- [x] 4.4 cmd/muniu 装配(构建评估器+挂 cleanup)+ 数据面零回归:全部缺省 `none` 时既有 e2e/冒烟全量零改动回放;验证:`go test ./cmd/muniu/ && go test ./internal/proxy/ -race`

## 5. 管理 API 与契约

- [x] 5.1 `GET /admin/v1/balances` 总览(每 key 余额与币种、有效余额、窗口日耗、剩余估算或禁用原因、告警态、最近检查/错误)+ handler 测试(真 SQLite);验证:`go test ./internal/admin/ -run Balance`
- [x] 5.2 `POST .../manual`(金额/币种校验 400 路径、录入即生效)+ `POST .../refresh`(单渠道立即评估信号);验证:`go test ./internal/admin/ -run 'BalanceManual|BalanceRefresh'`
- [x] 5.3 `adminRouteTable` 注册三条 + `Routes()` 派生一致 + `contracts/openapi.yaml` 三端点与 schema + `pnpm generate:api` 再生;验证:`go test ./internal/admin/ -run Routes` + 漂移门禁绿 + `git diff` 确认 api-types 已再生

## 6. web 控制台「余额」页

- [x] 6.1 `lib/api.ts` 方法 + `Balances.tsx`(TABS 6→7、底部导航 `grid-cols-7` 同步;桌面 Card+DataList 表格/窄屏实体卡;来源徽章、金额列 numeric 右对齐、估算降级文案四种;页头手动录入表单 FormRow+select;行内刷新按钮);验证:`pnpm typecheck && pnpm lint`
- [x] 6.2 组件测试(告警态徽章渲染、估算降级文案、录入提交载荷、空态 EmptyState);验证:`pnpm test`
- [x] 6.3 web 全门禁:`pnpm lint:css && pnpm format:check` + `pnpm exec vite build --outDir /tmp/dist-check` + 裸元素自检 grep + 375/768/1280 × 亮暗走查(移动范式逐页、暗色 token);验证:五件套 exit 0 + 走查截图记录

## 7. 文档、ADR 与收尾

- [x] 7.1 ADR-0018「渠道余额观测定位」:网关运营自保 vs ADR-0005 控制面边界的裁决记录(含 GCP 不纳入论证指针);验证:文档落盘 `docs/adr/0018-provider-balance-observability.md`
- [x] 7.2 `docs/design/balance-alerting.md` 行为契约摘要(八 kind 对照表、币种纪律、签名安全清单、估算口径「≈」);README 功能行与 example 指针;验证:文档与 specs 交叉引用无冲突
- [x] 7.3 全量门禁收口:`make check`(vet+lint+race+coverage)全绿 + `openspec validate --all` 通过;验证:命令输出贴验收记录
- [x] 7.4 真上游冒烟(手动档,需外部凭证,任一即可):Bearer 族(DeepSeek/Kimi/SiliconFlow 现有 key)或 aliyun-bss(AK/SK 只读子账号)单渠道实证一轮「查询→落库→评估→告警态」;凭证缺失则如实登记开放项,不阻塞归档判定

## Workflow follow-up

- 代码评审迭代循环(独立子代理,覆盖 OWASP Top 10 + 密钥泄露重点:凭证 ${VAR} 注入面、SK 不落日志/告警/存储)
- 归档:`openspec archive provider-balance-alerting`(主 specs 同步 + webhook-alerting 事件覆盖增量合入),归档后 `openspec validate --all` 复核
- HANDOFF 更新(状态/验收记录,≤30 行)
