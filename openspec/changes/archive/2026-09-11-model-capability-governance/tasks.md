## 1. 契约与测试先行(测试先行域:协议适配)

- [x] 1.1 先写失败测试:治理纯函数表驱动用例,覆盖 specs 五类负面场景(黑名单 drop、clamp 收敛/界内不变、思考锁宽松归一、换道重治理、未声明零干预字节不变);运行确认红
- [x] 1.2 先写失败测试:config 能力段校验负面用例(负数上限、缺 provider/model 键、参数名空白/协议结构字段);运行确认红

## 2. 能力目录(配置层)

- [x] 2.1 YAML 能力段解析与校验((provider, model) 二元组必填、drop_params/clamp 上限/思考锁约束、strict_governance 全局开关),非法条目加载期报错指向具体条目;1.2 用例转绿
- [x] 2.2 muniu.example.yaml 增注释样例(deepseek-reasoner 思考锁、qwen-vl-max 输出上限),config 端到端加载用例;`make check` 相关包绿

## 3. 治理核心(proxy 层)

- [x] 3.1 实现治理纯函数(幂等原地收敛 + 变更记录返回),attempt 循环内 adapter.Prepare 前挂接,按「目标 provider+model」解析约束;1.1 用例转绿
- [x] 3.2 换道重治理集成钉子:首选模型 clamp 后 failover 到备选,断言按备选声明重新治理;strict 分支单测(400 + 统一错误信封指明参数)
- [x] 3.3 留痕双通道:结构化日志字段(request_id/provider/model/param/action/原值)+ 响应头 `X-Muniu-Normalized`(流式首字节前可写);钉子断言响应头与日志字段

## 4. 收尾门禁

- [x] 4.1 `make check` 全绿(vet + lint + race test),新增回归钉子随包通过
- [x] 4.2 HANDOFF 验收记录追加本变更条目(判据+证据+日期),开放决策项表核对无新增悬置
