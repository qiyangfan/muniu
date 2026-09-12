## Purpose

Gemini 以官方 OpenAI 兼容端点出站的轻形态接入:复用既有 openai 兼容 adapter,quirk 只覆盖真上游冒烟实测的偏差项(零内置隐式知识),conformance fixture 钉住流式形态与 usage 键名,价目走既有价目体系。原生 generateContent adapter 明确不做(兼容层够用就不维护第二协议)。

## ADDED Requirements

### Requirement: 接入形态

Gemini 上游必须 (MUST) 以既有 openai 兼容 adapter 接入(provider 配置指向官方 OpenAI 兼容端点),不引入新 adapter 类型;接入前必须 (MUST) 完成真上游冒烟并记录偏差清单(参数拒绝面/流式形态/usage 键名/响应头),quirk 实现 (MUST) 仅覆盖实测偏差项。

#### Scenario: 冒烟先行

- **WHEN** 执行 Gemini 直连冒烟(流式 + 非流式 + 含 usage 场景)
- **THEN** 实录留档,quirk 项与偏差清单一一对应,无凭文档臆测的覆写

#### Scenario: 零新 adapter 类型

- **WHEN** 检查 adapter 注册表
- **THEN** 无 gemini 专属 adapter 类型,Gemini 走 openai 兼容档 + quirk 覆写

### Requirement: 偏差钉住与治理

实测偏差必须 (MUST) 以 conformance fixture 钉住(冒烟实录脱敏装配);模型参数个性必须 (MUST) 经能力目录声明(ADR-0010 机制),不新增治理概念;冒烟实证兼容层挡住必需功能时,才允许评估原生 adapter(登记演进条件,默认不做)。

#### Scenario: fixture 防漂移

- **WHEN** conformance fixture 套件运行
- **THEN** Gemini 流式形态与 usage 键名的实测偏差被断言覆盖,上游行为漂移可被测试捕获

### Requirement: 计量与价目

Gemini 流量必须 (MUST) 走既有计量链路(上游 usage 落账、别名牌价三段口径),零新计量口径;价目必须 (MUST) 经官方定价页核对后录入(USD/百万 token,推荐运营态),配置样例入 `muniu.example.yaml`。

#### Scenario: 计价闭环

- **WHEN** 经 Gemini 上游完成一次带 usage 的请求
- **THEN** usage_logs 落账行按录入牌价计价,与其它 openai 兼容上游同口径无差异
