# Tasks

## 1. 存储与配置地基

- [x] 1.1 store:`provider_balances` 迁移追加 prev 三列(前值余额/币种/时刻),`RecordBalanceSuccess` 写入时平移、失败路径不动前值;迁移升级用例 + 平移语义单测(成功转移/失败不转移/首写无前值)通过 `go test ./internal/store`
- [x] 1.2 store:燃速窗口区间聚合查询(现 BurnWindow 仅 fromUTC,补上一等长窗口 [now−2W, now−W) 所需形态),边界单测(空窗/跨日/样本天数)通过 `go test ./internal/store`
- [x] 1.3 config:`balance` 顶层新增 `burn_spike_ratio_milli`(缺省 5000)与 `drop_ratio_milli`(缺省 500),校验非负毫单位、0=禁用、负值拒绝并入攒齐报全;config_test 表驱动用例 + `muniu.example.yaml` 同步,验证 `go test ./internal/config`

## 2. 燃速突变规则(测试先行)

- [x] 2.1 evaluator:先写失败测试(达标单次触发/基线窗口零消耗或样本不足不判定/回落解除线=倍数÷迟滞/已触发不重复/重启恢复不重复/旋钮 0 禁用),再实现 `burn_spike` 判定(整数毫比值 + 迟滞解除),验证 `go test ./internal/balance`
- [x] 2.2 突变验证:临时破坏比值判据与解除线实现确认新测试变红,还原后复绿(测试与实现同源自洽盲区纪律)

## 3. 余额骤降规则(测试先行)

- [x] 3.1 evaluator:先写失败测试(跌幅达标触发/自然小跌幅不触发/平稳观测后重新武装/已触发不重复/币种不一致或缺前值不判定/manual 与云账务不参与),再实现 `cliff:<key_id>` 逐 key 判定,验证 `go test ./internal/balance`
- [x] 3.2 突变验证:临时破坏跌幅判据与重武装条件确认测试变红,还原后复绿

## 4. 服务装配

- [x] 4.1 service:评估输入装配(prev 三列、上一窗口聚合、两旋钮注入),失效 key 清理时同步删除其 cliff 规则状态行;集成用例(成功查询→平移→下一评估触发骤降)通过 `go test ./internal/balance`
- [x] 4.2 cmd/muniu/main.go 旋钮接线,`go build ./...` 通过

## 5. 管理面与文档收尾

- [x] 5.1 web:`Balances.tsx` 规则键→中文文案映射补 `burn_spike`/`cliff:` 前缀形态,验证 `pnpm typecheck && pnpm lint && pnpm lint:css` + 相关 vitest 用例
- [x] 5.2 `docs/design/balance-alerting.md`「核心算法与纪律」「事件与通知」两节同步两条规则语义与旋钮
- [x] 5.3 全量门禁 `make check` 全绿 + `openspec validate consumption-anomaly-alerting --strict` 通过
