# Spec Delta

## MODIFIED Requirements

### Requirement: 适配器值域只有一个来源

出站适配器类型集合 SHALL 由 `adapter` 包对外提供(`Types()`,排序稳定),其内容 MUST 等于 `adapter.For` 可成功构造的适配器类型集合;`internal/config` MUST NOT 维护独立的适配器字面量清单,配置校验的值域、以及错误提示中列举的合法取值,均 SHALL 由该来源派生/渲染。新增适配器实现时,配置面接受该取值 MUST 无需修改配置层代码。

#### Scenario: 值域来自实现集合

- GIVEN `adapter.For` 支持 openai、deepseek、moonshot、glm、qwen、doubao、anthropic、gemini
- WHEN 读取配置校验使用的适配器值域,或读取非法取值错误提示中列举的取值
- THEN 两处内容均为上述集合(排序一致),且与实现分支逐项相等

#### Scenario: 未实现的形态不在值域内

- GIVEN 某适配器类型名(如 `cohere`)在文档或讨论中出现,但 `adapter.For` 无对应构造分支
- WHEN 读取 `Types()`
- THEN 该类型名不在返回值中

### Requirement: 不可实现的适配器取值在加载期拒绝

`providers[].adapter` 取值不在值域内时,`config.Load` MUST 在配置加载期拒绝(拒绝启动),错误信息 MUST 指出具体条目(下标与 provider name)、MUST 原样回显非法取值、MUST 列出由值域单源渲染的真实取值集合;系统 MUST NOT 进入就绪状态,MUST NOT 推迟到适配器装配期才暴露。

#### Scenario: 配置写了无实现的形态

- GIVEN 某 provider 配置 `adapter: cohere`(该类型名在文档中出现,但 `For` 无实现)
- WHEN 执行配置加载
- THEN 加载失败并报出该条目的下标与 name,错误串中不把 `cohere` 列为合法取值

#### Scenario: 合法取值不受影响

- GIVEN provider 声明 `adapter: deepseek`
- WHEN 执行配置加载
- THEN 校验通过,后续装配构造出 deepseek 适配器,行为与本变更前逐字节一致

#### Scenario: 提示与实际能力一致(防漂移)

- GIVEN 有人向 `Types()` 加入一个 `For` 尚不能构造的类型名
- WHEN 运行适配器值域一致性测试
- THEN 测试失败(每个列出的类型都必须真能被 `For` 构造)
