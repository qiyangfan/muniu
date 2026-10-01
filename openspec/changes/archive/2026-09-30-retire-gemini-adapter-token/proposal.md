# retire-gemini-adapter-token 提案:适配器值域单源化,退役 gemini 占位

## Why

`internal/config/config.go` 的 `adapterNames` 白名单收录 `gemini`,同文件校验报错还把 `openai|deepseek|moonshot|glm|qwen|doubao|anthropic|gemini` 整串硬编码印给用户;而 `internal/adapter/adapter.go` 的 `For()` switch **没有 gemini 分支**,落到 `default` 返回「适配器类型 "gemini" 未实现或未知」。

失败链路:用户照白名单与报错提示写出 `adapter: gemini` → `config.Load` 校验通过 → 直到 `cmd/muniu/main.go:181` 构造适配器才炸。两份清单手抄,一份是「允许写什么」,一份是「能构造出什么」,二者已经失步,而报错文案会主动把用户推向那个写不了的取值。

Gemini 的接入方向**早已有裁决**,本变更不引入任何新方向:

- `docs/design/upstream-adapters.md`「Gemini 接入方向(upstream-egress-batch 裁决)」:走 Gemini 官方 OpenAI 兼容端点(openai 基座 + 实测偏差 quirk),**不新建原生 adapter**;
- `internal/presets/presets.go` 的 Gemini 预设条目已按该裁决落地:`Adapter: "openai"` + `BaseURL: https://generativelanguage.googleapis.com/v1beta/openai`——即 **Gemini 今天就能接入**,走的是兼容层;
- ADR-0013 已裁 Gemini `generateContent` 入站不立项;HANDOFF 2026-09-13 记录了用户对 Gemini 出站三项的「无限搁置」裁决(兼容层成熟 + 中国大陆不可达,零消费者)。

所以白名单里这个 token 是裁决之后没扫干净的残留,不是待补功能;`internal/presets/presets_test.go` 的注释甚至已经把「白名单里的 gemini 无实现,配置后会启动失败」写成了一条比 config 更严的自检规则——用测试绕开问题,而不是把问题源头掐掉。

## What Changes

1. **值域单源**:`For()` 的实现集合成为唯一权威。`internal/adapter` 新增 `Types() []string`(排序稳定,注释标明它即配置面 `adapter` 字段的值域),`internal/config` 的校验值域由 `adapter.Types()` 派生;报错文案的值域部分由同一来源渲染,删掉硬编码字符串。
2. **退役 gemini token**:白名单不再含 gemini。`adapter: gemini` 在**配置加载期**即拒(与既有 providers 校验同一「加载期拒绝启动」纪律),错误指向具体条目并列出真实值域。
3. **防漂移钉子(两条,替代 presets_test 那条绕行自检)**:
   - `Types()` 的每个值都必须真能被 `For()` 构造出来(bridge 经 blank import 接线)——防「进了清单但没实现」;
   - 报错文案列出的值域与 `Types()` 逐项相等——防「提示与实际不符」。
4. **文档清扫**:ADR-0003 补状态更正注记(桥接入驻方由「Anthropic 与 Gemini」更正为「仅 Anthropic」,指向 egress-batch 裁决);`docs/design/upstream-adapters.md` 的包树与协议矩阵行去掉 gemini bridge 旧计划形态;`docs/design/architecture.md` 包树同处更正;`muniu.example.yaml` 适配器注释同步;`docs/plans/phase-4-cache-console.md` T4.8 标注撤销及理由;`internal/presets` 两处「config 白名单里的 gemini 未实现」注释改为单源口径。

## Non-goals

- **不实现 Gemini 原生 `generateContent` 桥接**。本仓协议适配是测试先行域,纪律要求「quirk 只覆盖实测项、fixture 从冒烟实录脱敏装配、零内置隐式知识」;无可用的 GEMINI_API_KEY 即无实录,照第三方项目(proxycli 类)的偏差清单写适配器等于把二手知识伪装成实证资产,直接违反 `docs/articles/03-conformance-as-asset.md` 对 conformance 的定义。外部项目的参考价值仅在「证明可行」,不在「可作为 fixture 来源」。
- **不动 Gemini 经官方 OpenAI 兼容层的接入路径**:presets 条目、`docs/provider-presets.md` 的 Gemini 行、`internal/adapter/smoke_test.go` 的 `TestSmoke_gemini` 冒烟入口全部保留——它们描述的是兼容层形态,与原生桥接无关(核实过:`TestSmoke_Env` 是环境驱动的真上游冒烟入口,与 adapter 类型无耦合)。
- 不改 `providers[].auth` 声明制语义,不改 Anthropic 桥接行为,不动 openapi 与 web 控制台(前端 `ADAPTER_LABELS` 已不含 gemini,核实于 `web/src/pages/Providers.tsx:38-46`)。

## Capabilities

### New Capabilities

- `adapter-taxonomy`:出站适配器值域的单一来源,以及「不可实现形态在加载期拒绝」的配置面契约(ADR-0003 三分法的执法面)。

### Modified Capabilities

(无——`anthropic-bridge` 与 `model-capability-governance` 的行为契约不动;后者正文里「与 providers 校验同一纪律」的表述在值域单源后依然成立。)

## Impact

- **代码**:`internal/adapter/adapter.go`(+`Types()`,约 10 行)、`internal/config/config.go`(`adapterNames` 改派生 + 报错文案改渲染);新增/修改测试各 1 处。
- **配置兼容性**:纯收紧「写了也起不来的取值」。任何现存可启动的配置都不受影响(`adapter: gemini` 此前无法启动,现改为加载期更早报错并给出真实值域),无迁移成本。
- **文档**:ADR-0003 注记 + `docs/design/{upstream-adapters,architecture}.md` + `docs/plans/phase-4-cache-console.md` + `muniu.example.yaml` + presets 两处注释。
- **跨仓**:锦官 `ProviderPresetCatalog` 的 Gemini 条目备注写着「原生 gemini 桥接 muniu 未实现」,措辞与「已裁撤、非待实现」不符,由锦官侧另行随行修正(其契约测试钉字段结构,不钉该文案)。
