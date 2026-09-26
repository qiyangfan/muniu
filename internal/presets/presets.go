// Package presets 内置「官方端点预设目录」——一手官方厂商的接入参考数据,零运行时行为。
// 收录标准(docs/design/upstream-adapters.md「官方端点预设目录」):
//  1. 一手官方:模型厂商自营端点或其官方推理云,第三方中转/聚合站一律不收;
//  2. 协议可服务:OpenAI 兼容(openai 基座或 quirk 适配器),或已有桥接(anthropic);
//  3. 可溯源:端点与文档 URL 以官方文档为准(2026-09-26 逐家核对),厂商改端点只改本表。
//
// 消费方仅两处:GET /admin/v1/provider-presets 与控制台「供应商」页;用户复制 YAML 片段
// 填入 muniu.yaml 即完成接入。目录不参与任何转发判定,也不做内置隐式行为(ADR-0010 同哲学)。
//
// Anthropic 分支预设:国内主流厂商普遍设有 Anthropic 协议分支端点(Claude Code 生态),
// 以 adapter=anthropic + auth=bearer 收录(桥接声明制,按各家官方文档 AUTH_TOKEN 口径发
// Bearer;官方 Anthropic 缺省 x-api-key)。各分支实际接受头未逐家真上游冒烟,首次接入
// 前按 HANDOFF 开放项核验;工具多轮中途请求的桥接边界见 upstream-adapters。
package presets

// 分组码(admin API 契约,openapi enum 同轴):国内官方 / 国际官方 / 本地运行时。
const (
	GroupDomestic = "domestic"
	GroupGlobal   = "global"
	GroupLocal    = "local"
)

// Preset 单条官方端点预设。adapter/base_url/env 与 config.Provider 同轴,可逐字抄进 YAML。
type Preset struct {
	Alias   string   `json:"alias"`          // 目录内唯一别名,建议直接用作 provider name
	Display string   `json:"display"`        // 展示名(中文)
	Group   string   `json:"group"`          // domestic|global|local
	Adapter string   `json:"adapter"`        // 适配器类型;仅收 adapter.For 已实现集合(config 白名单里的 gemini 未实现,不收)
	Auth    string   `json:"auth,omitempty"` // 鉴权形态声明(透传 config.Provider.Auth):bearer=Authorization Bearer(国内分支口径);空=适配器缺省
	BaseURL string   `json:"base_url"`
	Env     string   `json:"env"`    // 建议的 key 环境变量名(约定俗成,非强制);本地运行时 = 任意非空占位
	Docs    string   `json:"docs"`   // 官方接入文档入口
	Models  []string `json:"models"` // 典型模型(示意,以官方模型列表为准;本地运行时留空)
	Note    string   `json:"note"`   // 接入注意事项(鉴权形态/兼容层状态等),可空
}

// catalog 目录本体:声明顺序即 API 输出顺序(国内官方 → 国际官方 → 本地运行时)。
// 鉴权一律 Bearer anthropic 除外(x-api-key,桥接内部处理);星火为「APIKey:APISecret」拼串(见 note)。
var catalog = []Preset{
	{
		Alias: "deepseek", Display: "DeepSeek", Group: GroupDomestic,
		Adapter: "deepseek", BaseURL: "https://api.deepseek.com/v1", Env: "DEEPSEEK_API_KEY",
		Docs: "https://api-docs.deepseek.com", Models: []string{"deepseek-chat", "deepseek-reasoner"},
	},
	{
		Alias: "deepseek-anthropic", Auth: "bearer", Display: "DeepSeek(Anthropic 分支)", Group: GroupDomestic,
		Adapter: "anthropic", BaseURL: "https://api.deepseek.com/anthropic", Env: "DEEPSEEK_API_KEY",
		Docs: "https://api-docs.deepseek.com", Models: []string{"deepseek-chat", "deepseek-reasoner"},
		Note: "Anthropic 协议分支(Claude Code 生态同款端点),经内置桥接接入",
	},
	{
		Alias: "moonshot", Display: "Moonshot(Kimi)", Group: GroupDomestic,
		Adapter: "moonshot", BaseURL: "https://api.moonshot.cn/v1", Env: "MOONSHOT_API_KEY",
		Docs: "https://platform.kimi.com", Models: []string{"kimi-k2.7-code"},
		Note: "开放平台已迁 platform.kimi.com,端点不变",
	},
	{
		Alias: "moonshot-anthropic", Auth: "bearer", Display: "Moonshot Kimi(Anthropic 分支)", Group: GroupDomestic,
		Adapter: "anthropic", BaseURL: "https://api.moonshot.cn/anthropic", Env: "MOONSHOT_API_KEY",
		Docs: "https://platform.kimi.com", Models: []string{"kimi-k2.7-code"},
		Note: "Anthropic 协议分支;国际站为 api.moonshot.ai/anthropic",
	},
	{
		Alias: "zhipu", Display: "智谱 GLM", Group: GroupDomestic,
		Adapter: "glm", BaseURL: "https://open.bigmodel.cn/api/paas/v4", Env: "ZHIPUAI_API_KEY",
		Docs: "https://docs.bigmodel.cn", Models: []string{"glm-5.1", "glm-4-flash"},
		Note: "Coding Plan 专用网关:https://open.bigmodel.cn/api/coding/paas/v4",
	},
	{
		Alias: "zhipu-anthropic", Auth: "bearer", Display: "智谱 GLM(Anthropic 分支)", Group: GroupDomestic,
		Adapter: "anthropic", BaseURL: "https://open.bigmodel.cn/api/anthropic", Env: "ZHIPUAI_API_KEY",
		Docs: "https://docs.bigmodel.cn", Models: []string{"glm-5.1"},
		Note: "Anthropic 协议分支(Claude Code 生态同款端点),经内置桥接接入",
	},
	{
		Alias: "qwen", Display: "阿里云百炼(通义千问)", Group: GroupDomestic,
		Adapter: "qwen", BaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", Env: "DASHSCOPE_API_KEY",
		Docs: "https://help.aliyun.com/zh/model-studio/", Models: []string{"qwen-plus", "qwen-max"},
	},
	{
		Alias: "qwen-anthropic", Auth: "bearer", Display: "阿里云百炼(Anthropic 分支)", Group: GroupDomestic,
		Adapter: "anthropic", BaseURL: "https://dashscope.aliyuncs.com/apps/anthropic", Env: "DASHSCOPE_API_KEY",
		Docs: "https://help.aliyun.com/zh/model-studio/", Models: []string{"qwen3.8-max"},
		Note: "Anthropic 协议分支;Coding Plan 为 coding.dashscope.aliyuncs.com/apps/anthropic",
	},
	{
		Alias: "doubao", Display: "火山方舟(豆包)", Group: GroupDomestic,
		Adapter: "doubao", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Env: "ARK_API_KEY",
		Docs: "https://www.volcengine.com/docs/82379", Models: []string{"doubao-seed-2-1-pro"},
		Note: "model 填模型名或推理接入点 ID(ep-xxx)",
	},
	{
		Alias: "doubao-anthropic", Auth: "bearer", Display: "火山方舟(Anthropic 分支)", Group: GroupDomestic,
		Adapter: "anthropic", BaseURL: "https://ark.cn-beijing.volces.com/api/compatible", Env: "ARK_API_KEY",
		Docs: "https://www.volcengine.com/docs/82379", Models: []string{"doubao-seed-2-1-pro"},
		Note: "Anthropic 协议分支(按量);订阅线为 /api/coding、/api/plan,与按量分账户计费",
	},
	{
		Alias: "minimax", Display: "MiniMax", Group: GroupDomestic,
		Adapter: "openai", BaseURL: "https://api.minimaxi.com/v1", Env: "MINIMAX_API_KEY",
		Docs: "https://platform.minimax.io/docs/api-reference/text-openai-api", Models: []string{"MiniMax-M2"},
		Note: "国内站 api.minimaxi.com;国际站为 api.minimax.io",
	},
	{
		Alias: "minimax-anthropic", Auth: "bearer", Display: "MiniMax(Anthropic 分支)", Group: GroupDomestic,
		Adapter: "anthropic", BaseURL: "https://api.minimaxi.com/anthropic", Env: "MINIMAX_API_KEY",
		Docs: "https://platform.minimax.io", Models: []string{"MiniMax-M2"},
		Note: "Anthropic 协议分支;国际站为 api.minimax.io/anthropic",
	},
	{
		Alias: "stepfun", Display: "阶跃星辰 StepFun", Group: GroupDomestic,
		Adapter: "openai", BaseURL: "https://api.stepfun.com/v1", Env: "STEPFUN_API_KEY",
		Docs: "https://platform.stepfun.com", Models: []string{"step-2-16k"},
	},
	{
		Alias: "hunyuan", Display: "腾讯混元", Group: GroupDomestic,
		Adapter: "openai", BaseURL: "https://api.hunyuan.cloud.tencent.com/v1", Env: "HUNYUAN_API_KEY",
		Docs: "https://cloud.tencent.com/document/product/1772", Models: []string{"hunyuan-turbos-latest"},
	},
	{
		Alias: "qianfan", Display: "百度千帆", Group: GroupDomestic,
		Adapter: "openai", BaseURL: "https://qianfan.baidubce.com/v2", Env: "QIANFAN_API_KEY",
		Docs: "https://cloud.baidu.com/doc/qianfan/s/Hmh4suq26", Models: []string{"ernie-4.0-8k"},
	},
	{
		Alias: "spark", Display: "讯飞星火", Group: GroupDomestic,
		Adapter: "openai", BaseURL: "https://spark-api-open.xf-yun.com/v1", Env: "SPARK_API_KEY",
		Docs: "https://xinghuo.xfyun.cn", Models: []string{"generalv3.5", "4.0Ultra"},
		Note: "key 为「APIKey:APISecret」英文冒号拼串,整体作为 Bearer 传入",
	},
	{
		Alias: "siliconflow", Display: "硅基流动 SiliconFlow", Group: GroupDomestic,
		Adapter: "openai", BaseURL: "https://api.siliconflow.cn/v1", Env: "SILICONFLOW_API_KEY",
		Docs: "https://docs.siliconflow.cn", Models: []string{"deepseek-ai/DeepSeek-V3", "Qwen/Qwen2.5-72B-Instruct"},
		Note: "官方推理云托管多家开源模型,model 形如「组织/模型名」",
	},
	{
		Alias: "modelscope", Display: "魔搭 ModelScope", Group: GroupDomestic,
		Adapter: "openai", BaseURL: "https://api-inference.modelscope.cn/v1", Env: "MODELSCOPE_API_KEY",
		Docs: "https://modelscope.cn/docs/model-service/API-Inference/intro", Models: []string{"Qwen/Qwen3-235B-A22B"},
		Note: "官方推理云免费额度,model 形如「组织/模型名」",
	},
	{
		Alias: "openai", Display: "OpenAI", Group: GroupGlobal,
		Adapter: "openai", BaseURL: "https://api.openai.com/v1", Env: "OPENAI_API_KEY",
		Docs: "https://platform.openai.com/docs", Models: []string{"gpt-4o-mini", "gpt-4o"},
	},
	{
		Alias: "anthropic", Display: "Anthropic(Claude)", Group: GroupGlobal,
		Adapter: "anthropic", BaseURL: "https://api.anthropic.com", Env: "ANTHROPIC_API_KEY",
		Docs: "https://docs.anthropic.com", Models: []string{"claude-sonnet-4-5"},
		Note: "异构协议走 Anthropic 桥接;base_url 填裸域,桥接自动拼 /v1/messages;工具多轮中途请求不支持",
	},
	{
		Alias: "gemini", Display: "Google Gemini", Group: GroupGlobal,
		Adapter: "openai", BaseURL: "https://generativelanguage.googleapis.com/v1beta/openai", Env: "GEMINI_API_KEY",
		Docs: "https://ai.google.dev/gemini-api/docs/openai", Models: []string{"gemini-2.5-flash"},
		Note: "官方 OpenAI 兼容层(beta);未经真上游冒烟实证,中国大陆网络不可直连",
	},
	{
		Alias: "xai", Display: "xAI(Grok)", Group: GroupGlobal,
		Adapter: "openai", BaseURL: "https://api.x.ai/v1", Env: "XAI_API_KEY",
		Docs: "https://docs.x.ai", Models: []string{"grok-4.7"},
	},
	{
		Alias: "groq", Display: "Groq", Group: GroupGlobal,
		Adapter: "openai", BaseURL: "https://api.groq.com/openai/v1", Env: "GROQ_API_KEY",
		Docs: "https://console.groq.com/docs/openai", Models: []string{"llama-3.3-70b-versatile"},
	},
	{
		Alias: "mistral", Display: "Mistral AI", Group: GroupGlobal,
		Adapter: "openai", BaseURL: "https://api.mistral.ai/v1", Env: "MISTRAL_API_KEY",
		Docs: "https://docs.mistral.ai", Models: []string{"mistral-large-latest"},
	},
	{
		Alias: "ollama", Display: "Ollama(本机)", Group: GroupLocal,
		Adapter: "openai", BaseURL: "http://127.0.0.1:11434/v1", Env: "OLLAMA_API_KEY",
		Docs:   "https://github.com/ollama/ollama/blob/main/docs/openai.md",
		Models: []string{},
		Note:   "本地免鉴权:env 值设任意非空占位串(如 local);模型以本机 ollama list 为准",
	},
	{
		Alias: "vllm", Display: "vLLM(本机)", Group: GroupLocal,
		Adapter: "openai", BaseURL: "http://127.0.0.1:8000/v1", Env: "VLLM_API_KEY",
		Docs:   "https://docs.vllm.ai/en/latest/serving/openai_compatible_server.html",
		Models: []string{},
		Note:   "本地免鉴权:env 值设任意非空占位串;模型以 --served-model-name 为准",
	},
	{
		Alias: "lmstudio", Display: "LM Studio(本机)", Group: GroupLocal,
		Adapter: "openai", BaseURL: "http://127.0.0.1:1234/v1", Env: "LMSTUDIO_API_KEY",
		Docs:   "https://lmstudio.ai/docs/app/api",
		Models: []string{},
		Note:   "本地免鉴权:env 值设任意非空占位串",
	},
}

// Catalog 返回预设目录(只读;调用方不得修改,修改仅应发生在本表)。
func Catalog() []Preset {
	return catalog
}
