# retire-gemini-adapter-token 设计

## D1:值域住在 `adapter` 包,config 派生消费

`adapter.Types()` 返回 `For()` 能构造出来的类型集合(排序后)。`internal/config` 删除手抄的 `adapterNames` 字面量,改为由 `adapter.Types()` 构造的集合;报错文案里的值域同样由 `Types()` 渲染。

- **为什么这个方向**:实现集合的真实定义点在 `For()` 的 switch,不在配置层——谁实现谁拥有值域。反向(config 定义、adapter 消费)会让「新增适配器要改两处」继续存在。
- **依赖方向合法性**:`internal/adapter` 不 import `internal/config`(核实过,全仓零处),`config → adapter → protocol` 单向,不与 AGENTS.md 的「protocol 是 DTO 单源 ← adapter/proxy ← 编排层」冲突;config 已 import `internal/metering`,跨层引用有先例。
- **否决:在 config 里保留清单 + 加注释提醒同步**。注释与人工纪律正是本次失步的成因,留着两份清单就是留着下一次失步。

## D2:拒绝时机在配置加载期,不在装配期

`adapter: gemini` 由 `config.Load` 报错拒绝(与 providers 其余字段同一纪律),而不是等 `cmd/muniu/main.go` 构造时报错。

- 理由:`For()` 的 `default` 分支错误只有类型名,没有 provider 下标与 name;加载期能报出 `providers[i](name) adapter 非法 "gemini"(允许 …)` 这种可直接定位的条目级错误,而装配期报错既晚又缺定位信息。
- **否决:两处都校验**。同一个不变量两个执法点,报错文案会分叉;`For()` 的 `default` 保留为库级自保(internal 包被其他调用方使用时的兜底),不承担用户提示职责。

## D3:只导出 `Types()`,不导出 `Supported(string)`

校验需要「列举全部合法值」(报错文案),只需要一份 slice 导出即可;再加一个谓词函数是第二个 API 面、第二次失步机会。config 侧一次性 `map[string]bool` 化即可。

## D4:防漂移用两条测试,不用代码生成/反射

1. `Types()` 每个值都能被 `For()` 构造成功(测试文件 blank import `adapter/bridge`,与 `cmd/muniu/main.go:22` 同接线方式)——把「列进清单却没实现」钉死。
2. 报错文案中出现的值域与 `Types()` 逐项相等——把「提示与实际不符」钉死。

同时删除 `internal/presets/presets_test.go` 里「比 config 白名单更严:白名单里的 gemini 无实现」这条绕行自检的依据(该测试本身逻辑不变,仍要求预设 adapter 属于可实现集合;注释改述为单源口径)。

- **否决:build tag 生成清单 / 反射枚举分支**。为两个函数的同步引入构建期代码生成,复杂度买得不对等。

## D5:不引入任何 Gemini 形态的实现或 fixture

无真实端点即无冒烟实录,无实录即无 fixture。这条不是「等 key」的临时妥协,而是裁决结论:Gemini 走官方 OpenAI 兼容层已由预设目录承载,原生桥接没有待满足的消费场景。若未来出现真实需求,重新立项时必须先按 `docs/articles/03-conformance-as-asset.md` 的流程补实录,而不是先写代码。

## 后果

- 配置面「合法值清单」与「可实现清单」结构上合一,`adapter` 新增形态无需改 config。
- `adapter: gemini` 的失败点从装配期提前到加载期,错误可定位。
- 文档面 ADR-0003 的「Anthropic 与 Gemini 入驻」与 `upstream-adapters.md` 包树的 `bridge/gemini.go` 一并更正,消除「原生桥接仍在计划内」的误读。
