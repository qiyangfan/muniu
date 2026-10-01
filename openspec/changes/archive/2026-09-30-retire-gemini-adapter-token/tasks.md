# retire-gemini-adapter-token 实施任务

## 1. 测试先行(铁律 2:协议适配域先红)

- [x] 1.1 失败测试:`adapter.Types()` 的每个值都能被 `adapter.For()` 构造成功(测试文件 blank import `adapter/bridge`);当前应能编译并暴露「清单含未实现项即红」的判据
      **验证**:实现 `Types()` 后转绿
- [x] 1.2 失败测试:配置 `providers[].adapter: gemini` → `config.Load` 在加载期返回错误,错误串含该 provider 下标与 name,且不含 `gemini`
      **验证**:config 改派生值域后转绿
- [x] 1.3 失败测试:非法 adapter 报错文案列出的值域与 `adapter.Types()` 逐项相等(排序一致)
      **验证**:报错文案改由 `Types()` 渲染后转绿

## 2. 实现

- [x] 2.1 `internal/adapter/adapter.go`:新增 `Types() []string`(排序稳定,注释标明即配置面 `adapter` 字段值域;与 `For()` 分支同文件维护,新增分支必须同步入列)
      **验证**:1.1 转绿
- [x] 2.2 `internal/config/config.go`:删除 `adapterNames` 手抄字面量,改由 `adapter.Types()` 派生;`:385` 报错文案的值域部分改为渲染 `adapter.Types()`
      **验证**:1.2、1.3 转绿;`internal/config` 其余校验零回归

## 3. 文档与注释清扫

- [x] 3.1 `docs/adr/0003-adapter-taxonomy.md`:补状态更正注记——桥接型入驻方由「Anthropic 与 Gemini」更正为「仅 Anthropic」,依据 `upstream-egress-batch` 裁决与本 change;正文历史决策保留不删
- [x] 3.2 `docs/design/upstream-adapters.md`:包树的 `bridge/{anthropic,gemini}.go` 与 `gemini.go generateContent ↔ OpenAI 形态全翻译` 更正;协议矩阵 Gemini 行去掉「bridge 旧计划形态」残留表述,指向兼容层裁决段;「Gemini 接入方向」段补一句「配置白名单不含 gemini(值域单源=adapter.For 可实现集合)」
- [x] 3.3 `docs/design/architecture.md`:包树同处 `bridge/{anthropic,gemini}.go` 更正
- [x] 3.4 `muniu.example.yaml`:适配器注释 `anthropic/gemini=桥接(后续阶段)` 更正为真实值域口径,并指向 `GET /admin/v1/provider-presets` 与 Gemini 兼容层条目
- [x] 3.5 `docs/plans/phase-4-cache-console.md` T4.8:标注「撤销(2026-09-30 裁决,见本 change)」及理由,不改写其余历史勾稽
- [x] 3.6 `internal/presets/presets.go` 与 `presets_test.go` 的「config 白名单里的 gemini 未实现」注释改为单源口径(测试逻辑不动)
- [x] 3.7 `docs/ROADMAP.md` Backlog 与 `HANDOFF.md`:登记「Gemini 原生桥接不立项」终态与本 change 结论,HANDOFF 更新状态与验收记录(≤30 行纪律)

## 4. 收尾

- [x] 4.1 `make check` 全绿(vet + lint + 全包 race)
- [x] 4.2 自检:全仓 grep 确认除归档件与本文档外,无残留把 `gemini` 描述为可配置适配器形态的文字
- [x] 4.3 归档前置:`openspec validate --all --strict` 通过,delta 工件齐备
