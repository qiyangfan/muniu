// Package config 负责 muniu.yaml 的定位、${VAR} 环境展开与启动期校验(ADR-0004:
// 静态拓扑走 YAML,密钥只存环境变量引用名)。配置启动后只读;热加载在 backlog,v0 不做。
package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/solvela/muniu/internal/metering"
)

// 适配器形态枚举(upstream-adapters「适配器分类学」;bridge 两家 Phase 2+ 落地)。
var adapterNames = map[string]bool{"openai": true, "deepseek": true, "moonshot": true, "glm": true, "qwen": true, "doubao": true, "anthropic": true, "gemini": true}

// logLevel / logFormat / currency 枚举。
var (
	logLevels     = map[string]bool{"debug": true, "info": true, "warn": true, "error": true}
	logForms      = map[string]bool{"json": true, "text": true}
	legalCurrency = map[string]bool{"CNY": true, "USD": true}
)

// Timeouts 三层超时(proxy-and-streaming「超时模型」:相互独立,不以总超时兜底)。
// YAML 以秒为单位;Duration 方法供运行态消费。
type Timeouts struct {
	Dial       int64 `yaml:"dial"`        // 秒
	FirstToken int64 `yaml:"first_token"` // 秒
	ChunkIdle  int64 `yaml:"chunk_idle"`  // 秒
}

// DialDuration 建连超时。
func (t Timeouts) DialDuration() time.Duration { return time.Duration(t.Dial) * time.Second }

// FirstTokenDuration 首 token 超时。
func (t Timeouts) FirstTokenDuration() time.Duration {
	return time.Duration(t.FirstToken) * time.Second
}

// ChunkIdleDuration 帧间空闲超时。
func (t Timeouts) ChunkIdleDuration() time.Duration { return time.Duration(t.ChunkIdle) * time.Second }

// Provider 上游供应商静态拓扑。
type Provider struct {
	Name     string   `yaml:"name"`
	Adapter  string   `yaml:"adapter"`
	BaseURL  string   `yaml:"base_url"`
	Auth     string   `yaml:"auth"` // 鉴权形态声明:空=适配器缺省(anthropic=x-api-key);bearer=Authorization Bearer(国内分支口径)。仅 anthropic 适配器消费
	Keys     []KeyRef `yaml:"keys"`
	Weight   int      `yaml:"weight"`
	Priority int      `yaml:"priority"`
}

// KeyRef 环境变量引用名——绝不存明文(data-and-config「分工边界」);weight 参与 key 池 WRR。
type KeyRef struct {
	Env    string `yaml:"env"`
	Weight int    `yaml:"weight"`
}

// Candidate 模型路由候选:priority 小者先,同 priority 加权随机(Phase 3 完整语义)。
type Candidate struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Priority int    `yaml:"priority"`
	Weight   int    `yaml:"weight"`
}

// MaxTopologyInt 拓扑数值域上界:weight/priority 的契约语义是数据面 int——failover
// 加权随机的 total 累加是 int 运算,超域巨值会溢出为负使加权随机失效。YAML validate
// 与 ReplaceTopology 守门(控制面下发路径)两个入口共用同一域。
const MaxTopologyInt = int64(1<<31 - 1)

// PriceWindow 分时段倍率窗(metering.md「计价」契约):整张价目卡按倍率缩放。
// 多窗**按声明顺序先匹配优先**;支持跨午夜(如 16:30-00:30)。
type PriceWindow struct {
	Window     string `yaml:"window"`     // "HH:MM-HH:MM";终点 24:00=当日末;start>end 为跨午夜
	Multiplier string `yaml:"multiplier"` // 人类可读倍率("0.5" 表半价);≤4 位小数,取值 (0,10]
}

// Price 人类可读价目(百万 Token);数值换算在装配期经 metering.ParsePrice 完成。
// CachedInput 缺省=Input(metering「缓存价」契约)。
// TimeWindows 缺省无 → 恒缺省价;配置后按时区判定窗口,先声明先匹配,无命中回落缺省价。
type Price struct {
	Currency    string        `yaml:"currency"`
	Input       string        `yaml:"input"`
	CachedInput string        `yaml:"cached_input"`
	Output      string        `yaml:"output"`
	Timezone    string        `yaml:"timezone"` // "UTC" 或 ±HH:MM 固定偏移;缺省 UTC(不引 tzdata,DST 时区请换算为 UTC 表达)
	TimeWindows []PriceWindow `yaml:"time_windows"`
}

// SemanticCfg 语义缓存配置(T4.5;enabled=true 时 embedding_model 必填,缺省值见 validate)。
type SemanticCfg struct {
	Enabled        bool    `yaml:"enabled"`
	EmbeddingModel string  `yaml:"embedding_model"` // 模型别名(走 model_routes)
	Dim            int     `yaml:"dim"`             // 缺省 1536
	Threshold      float32 `yaml:"threshold"`       // 缺省 0.97
	TTLHours       int     `yaml:"ttl_hours"`       // 缺省 24
	Capacity       int     `yaml:"capacity"`        // 缺省 10000
}

// AuthCfg 平台 key 配置(ADR-0006;全部可选,Secrets 空即平台 key 未启用,standalone 零回归)。
type AuthCfg struct {
	PlatformKeySecrets     []string `yaml:"platform_key_secrets"`     // base64 编码的 HMAC 密钥(解码后 ≥16 字节),支持多把轮换
	RevocationsURL         string   `yaml:"revocations_url"`          // 吊销名单端点(可选)
	RevocationsIntervalSec int      `yaml:"revocations_interval_sec"` // 吊销收敛窗口,缺省 60s
}

// ModelCapability 模型能力声明(ADR-0010):(provider, model) 二元组必填、零通配、零内置。
// 声明了约束才干预,未声明 = 零干预透传;治理语义见 upstream-adapters「请求参数治理」。
type ModelCapability struct {
	Provider        string   `yaml:"provider" json:"provider"` // json tag:拓扑控制面下发契约(3.2)显式化
	Model           string   `yaml:"model" json:"model"`
	DropParams      []string `yaml:"drop_params" json:"drop_params"`             // 不支持参数黑名单(OpenAI 形态参数名)
	ThinkingLock    bool     `yaml:"thinking_lock" json:"thinking_lock"`         // 思考不可关:关闭思考指令按策略处置
	MaxOutputTokens int64    `yaml:"max_output_tokens" json:"max_output_tokens"` // 输出上限(0=不限制);越界 clamp max_tokens
}

// Config muniu.yaml 的运行态形态(启动后只读)。
type Config struct {
	Server struct {
		Addr     string   `yaml:"addr"`
		Timeouts Timeouts `yaml:"timeouts"`
	} `yaml:"server"`
	Storage struct {
		SQLite struct {
			Path string `yaml:"path"`
		} `yaml:"sqlite"`
	} `yaml:"storage"`
	Log struct {
		Level  string `yaml:"level"`
		Format string `yaml:"format"`
	} `yaml:"log"`
	Auth    AuthCfg `yaml:"auth"`
	Metrics struct {
		Enabled bool `yaml:"enabled"` // 缺省 false;启用后 GET /metrics(Prometheus 文本协议,ADR-0009)
	} `yaml:"metrics"`
	Metering struct {
		FlushIntervalMs int    `yaml:"flush_interval_ms"`
		FlushBatch      int    `yaml:"flush_batch"`
		Sink            string `yaml:"sink"`             // local(缺省) | ingest
		IngestURL       string `yaml:"ingest_url"`       // sink=ingest 必填
		IngestToken     string `yaml:"ingest_token"`     // 可选 Bearer(支持 ${VAR} 展开)
		BufferCapacity  int    `yaml:"buffer_capacity"`  // ingest 失败的本地缓冲上限(行),缺省 1_000_000
		Settlement      string `yaml:"settlement"`       // 本位币:CNY(缺省)|USD;credit 锚定本位币(1 credit = 1e-9 本位币)
		FXUSDCNYMilli   int64  `yaml:"fx_usd_cny_milli"` // 汇率:1 USD = fx/1000 CNY(7.200=7200);仅异币种价目需要
	} `yaml:"metering"`
	ControlPlane struct {
		PricingURL      string `yaml:"pricing_url"`       // 控制面价目列表端点;空 = 不拉取(standalone)
		TopologyURL     string `yaml:"topology_url"`      // 控制面拓扑端点(3.2);空 = 文件源(现状等价)
		PollIntervalSec int    `yaml:"poll_interval_sec"` // 拉取间隔,缺省 60s
	} `yaml:"control_plane"`
	RateLimit struct {
		Backend   string `yaml:"backend"`    // inmem(缺省,实例自治)| redis(共享视图,cluster 形态;ADR-0008 修订)
		RedisAddr string `yaml:"redis_addr"` // backend=redis 必填(host:port)
	} `yaml:"ratelimit"`
	Providers        []Provider             `yaml:"providers"`
	ModelRoutes      map[string][]Candidate `yaml:"model_routes"`
	Pricing          map[string]Price       `yaml:"pricing"`
	Semantic         SemanticCfg            `yaml:"semantic"`
	Models           []ModelCapability      `yaml:"models"`            // 模型能力目录(ADR-0010)
	StrictGovernance bool                   `yaml:"strict_governance"` // true = 命中约束即 400(缺省宽松放行+留痕)
	Plugins          []PluginCfg            `yaml:"plugins"`           // WASM 插件静态注册(ADR-0011;运行态增改走管理面)
}

// PluginCfg 单个 WASM 插件的静态声明(ADR-0011)。资源缺省值与硬顶见 plugin 包
// ResourceDefaults;启用态、版本替换的运行态操作走管理面,重启后回到本 YAML 声明。
type PluginCfg struct {
	Name           string            `yaml:"name"`
	Path           string            `yaml:"path"`             // wasm 二进制路径(支持 ${VAR} 展开)
	Enabled        *bool             `yaml:"enabled"`          // 缺省 true;nil 区分「未配置」与「显式停用」
	FailOpen       bool              `yaml:"fail_open"`        // true = 调用失败放行原请求;安全类插件(如 PII 脱敏)务必保持 false
	TimeoutMs      int64             `yaml:"timeout_ms"`       // 单次调用超时;0 = 缺省 50
	MemoryLimitMiB int               `yaml:"memory_limit_mib"` // 实例内存上限;0 = 缺省 64
	PoolSize       int               `yaml:"pool_size"`        // 实例池;0 = 缺省 8
	Config         map[string]string `yaml:"config"`           // 透传插件的键值配置(get_config)
}

// ProviderByName 按名称取 provider(路由解析用)。
func (c *Config) ProviderByName(name string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.Name == name {
			return p, true
		}
	}
	return Provider{}, false
}

// varPattern 只展开 ${VAR} 引用形式;裸 $VAR 不动,避免误伤。
var varPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load 定位(-c > MUNIU_CONFIG > ./muniu.yaml)→ 展开 → 解析 → 校验。
// 任何一步失败立即返回 error;校验错误一次性列全(data-and-config「配置加载」)。
func Load(pathFlag string) (*Config, error) {
	path, err := locate(pathFlag)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	expanded, err := expandEnv(raw)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(expanded, &cfg); err != nil {
		return nil, fmt.Errorf("解析 YAML %s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// locate 配置文件定位:显式参数 > 环境变量 > 工作目录默认名。
func locate(pathFlag string) (string, error) {
	if pathFlag != "" {
		return pathFlag, nil
	}
	if p := os.Getenv("MUNIU_CONFIG"); p != "" {
		return p, nil
	}
	return "./muniu.yaml", nil
}

// expandEnv 展开 ${VAR};缺失变量收集全部后一次报错(fail-fast,kongming compose 纪律)。
func expandEnv(raw []byte) ([]byte, error) {
	var missing []string
	out := varPattern.ReplaceAllFunc(raw, func(m []byte) []byte {
		name := varPattern.FindSubmatch(m)[1]
		if v, ok := os.LookupEnv(string(name)); ok {
			return []byte(v)
		}
		missing = append(missing, string(name))
		return m
	})
	if len(missing) > 0 {
		return nil, fmt.Errorf("环境变量未定义(全部列出): %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// validate 结构校验;所有问题攒进同一错误一次性报全,不挤牙膏。
func (c *Config) validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if c.Server.Addr == "" {
		c.Server.Addr = ":8787"
	}
	if c.Server.Timeouts.Dial <= 0 || c.Server.Timeouts.FirstToken <= 0 || c.Server.Timeouts.ChunkIdle <= 0 {
		// 0 = 未配置,取设计默认;负数 = 配错,拒绝启动
		if c.Server.Timeouts.Dial < 0 || c.Server.Timeouts.FirstToken < 0 || c.Server.Timeouts.ChunkIdle < 0 {
			add("server.timeouts 不可为负: %+v", c.Server.Timeouts)
		}
	}
	if c.Server.Timeouts.Dial == 0 {
		c.Server.Timeouts.Dial = 5
	}
	if c.Server.Timeouts.FirstToken == 0 {
		c.Server.Timeouts.FirstToken = 60
	}
	if c.Server.Timeouts.ChunkIdle == 0 {
		c.Server.Timeouts.ChunkIdle = 30
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	} else if !logLevels[c.Log.Level] {
		add("log.level 非法 %q(允许 debug|info|warn|error)", c.Log.Level)
	}
	if c.Log.Format == "" {
		c.Log.Format = "json"
	} else if !logForms[c.Log.Format] {
		add("log.format 非法 %q(允许 json|text)", c.Log.Format)
	}
	if c.Storage.SQLite.Path == "" {
		c.Storage.SQLite.Path = "data/muniu.db"
	}
	if c.Metering.FlushIntervalMs == 0 {
		c.Metering.FlushIntervalMs = 500
	}
	if c.Metering.FlushBatch == 0 {
		c.Metering.FlushBatch = 256
	}
	if c.Metering.FlushIntervalMs < 0 || c.Metering.FlushBatch < 0 {
		add("metering 配置非法(flush_interval_ms/flush_batch 不可为负)")
	}
	switch c.Metering.Sink {
	case "", "local":
	case "ingest":
		if c.Metering.IngestURL == "" {
			add("metering.sink=ingest 时 ingest_url 必填")
		}
		// 缓冲容量须 ≥ 攒批量:否则单批永远装不进缓冲,续传死锁(config 防呆,ADR-0007)
		if c.Metering.BufferCapacity != 0 && c.Metering.BufferCapacity < c.Metering.FlushBatch {
			add("metering.buffer_capacity(%d) 不可小于 flush_batch(%d)", c.Metering.BufferCapacity, c.Metering.FlushBatch)
		}
	default:
		add("metering.sink 非法 %q(允许 local|ingest)", c.Metering.Sink)
	}
	if c.Metering.BufferCapacity < 0 {
		add("metering.buffer_capacity 不可为负")
	}
	for i, sec := range c.Auth.PlatformKeySecrets {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sec))
		if err != nil || len(raw) < 16 {
			add("auth.platform_key_secrets[%d] 非法(须 base64,解码后 ≥16 字节)", i)
		}
	}
	if c.Auth.RevocationsURL != "" && len(c.Auth.PlatformKeySecrets) == 0 {
		add("配置了 revocations_url 但未启用 platform_key_secrets(吊销同步无对象)")
	}
	if c.Auth.RevocationsIntervalSec < 0 {
		add("auth.revocations_interval_sec 不可为负")
	}
	if c.ControlPlane.PollIntervalSec < 0 {
		add("control_plane.poll_interval_sec 不可为负")
	}
	if c.ControlPlane.PricingURL != "" {
		if u, err := url.Parse(c.ControlPlane.PricingURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("control_plane.pricing_url 非法: %q", c.ControlPlane.PricingURL)
		}
	}
	if c.ControlPlane.TopologyURL != "" {
		if u, err := url.Parse(c.ControlPlane.TopologyURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("control_plane.topology_url 非法: %q", c.ControlPlane.TopologyURL)
		}
	}

	pluginNames := map[string]bool{}
	for i, pl := range c.Plugins {
		if pl.Enabled == nil { // 缺省启用;nil 区分「未配置」与「显式停用」
			v := true
			c.Plugins[i].Enabled = &v
		}
		if pl.Name == "" {
			add("plugins[%d] 缺 name", i)
			continue
		}
		if pluginNames[pl.Name] {
			add("plugins[%d] name %q 重名", i, pl.Name)
		}
		pluginNames[pl.Name] = true
		if pl.Path == "" {
			add("plugins[%d](%s) 缺 path", i, pl.Name)
		}
		if pl.TimeoutMs < 0 {
			add("plugins[%d](%s) timeout_ms 不可为负", i, pl.Name)
		}
		if pl.TimeoutMs > 10_000 {
			add("plugins[%d](%s) timeout_ms(%d) 超过硬顶 10000(请求体钩子超 10s 属配错)", i, pl.Name, pl.TimeoutMs)
		}
		if pl.MemoryLimitMiB < 0 || pl.MemoryLimitMiB > 256 {
			add("plugins[%d](%s) memory_limit_mib 须在 1..256,实际 %d", i, pl.Name, pl.MemoryLimitMiB)
		}
		if pl.PoolSize < 0 || pl.PoolSize > 64 {
			add("plugins[%d](%s) pool_size 须在 1..64,实际 %d", i, pl.Name, pl.PoolSize)
		}
	}

	if len(c.Providers) == 0 {
		add("providers 至少需要一个")
	}
	seen := map[string]bool{}
	for i, p := range c.Providers {
		if p.Name == "" {
			add("providers[%d] 缺 name", i)
			continue
		}
		if seen[p.Name] {
			add("providers[%d] name %q 重名", i, p.Name)
		}
		seen[p.Name] = true
		if !adapterNames[p.Adapter] {
			add("providers[%d](%s) adapter 非法 %q(允许 openai|deepseek|moonshot|glm|qwen|doubao|anthropic|gemini)", i, p.Name, p.Adapter)
		}
		if u, err := url.Parse(p.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add("providers[%d](%s) base_url 非法: %q", i, p.Name, p.BaseURL)
		}
		if p.Auth != "" {
			// 声明制鉴权形态:仅 anthropic 桥接消费(其余适配器鉴权固有,声明即配置错误);
			// 值域与 adapter.AuthBearer 同值(字面量跨包重复,行为由两端测试各自钉死)
			if p.Adapter != "anthropic" {
				add("providers[%d](%s) auth 仅 anthropic 适配器支持(其余形态鉴权固有)", i, p.Name)
			} else if p.Auth != "bearer" {
				add("providers[%d](%s) auth 非法 %q(允许 bearer)", i, p.Name, p.Auth)
			}
		}
		if len(p.Keys) == 0 {
			add("providers[%d](%s) keys 不能为空", i, p.Name)
		}
		for j, k := range p.Keys {
			if k.Env == "" {
				add("providers[%d](%s).keys[%d] 缺 env 引用名", i, p.Name, j)
			}
			if int64(k.Weight) > MaxTopologyInt {
				add("providers[%d](%s).keys[%d] weight 超出数值域(≤%d)", i, p.Name, j, MaxTopologyInt)
			}
		}
		if p.Weight <= 0 || int64(p.Weight) > MaxTopologyInt {
			add("providers[%d](%s) weight 必须 >0 且 ≤%d, 实际 %d", i, p.Name, MaxTopologyInt, p.Weight)
		}
		if p.Priority < 0 || int64(p.Priority) > MaxTopologyInt {
			add("providers[%d](%s) priority 必须在 [0,%d], 实际 %d", i, p.Name, MaxTopologyInt, p.Priority)
		}
	}

	for alias, cands := range c.ModelRoutes {
		if len(cands) == 0 {
			add("model_routes[%s] 候选列表为空", alias)
			continue
		}
		for j, cd := range cands {
			if _, ok := c.ProviderByName(cd.Provider); !ok {
				add("model_routes[%s].candidates[%d] 引用不存在的 provider %q", alias, j, cd.Provider)
			}
			if cd.Model == "" {
				add("model_routes[%s].candidates[%d] 缺 model", alias, j)
			}
			if cd.Priority < 0 || int64(cd.Priority) > MaxTopologyInt {
				add("model_routes[%s].candidates[%d] priority 必须在 [0,%d], 实际 %d", alias, j, MaxTopologyInt, cd.Priority)
			}
			if int64(cd.Weight) > MaxTopologyInt {
				add("model_routes[%s].candidates[%d] weight 超出数值域(≤%d)", alias, j, MaxTopologyInt)
			}
		}
	}

	// 本位币(settlement):credit 锚定点;异币种价目必须配置汇率,禁止静默混币种
	settlement := strings.ToUpper(c.Metering.Settlement)
	if settlement == "" {
		settlement = "CNY"
	}
	if !metering.ValidSettlement(settlement) {
		add("metering.settlement 非法 %q(允许 CNY|USD)", c.Metering.Settlement)
	}

	for alias, pr := range c.Pricing {
		if !legalCurrency[strings.ToUpper(pr.Currency)] {
			add("pricing[%s] currency 非法 %q(允许 CNY|USD)", alias, pr.Currency)
		}
		// 异币种报价必须显式配置汇率(本位币概念见 metering.md「本位币与 credit」);
		// 省略币种 = 本位币(与 SettlePrice 空 Currency 短路语义一致,域B 审查对齐)
		quoteCur := strings.ToUpper(pr.Currency)
		if quoteCur == "" {
			quoteCur = settlement
		}
		if quoteCur != settlement && c.Metering.FXUSDCNYMilli <= 0 {
			add("pricing[%s] 报价币种 %s ≠ 本位币 %s,须配置 metering.fx_usd_cny_milli(USD→CNY 毫单位)", alias, quoteCur, settlement)
		}
		// 分时段倍率窗:直接引用 metering 解析器做 fail-fast(单一事实源,消息聚合一次报全)
		if pr.Timezone != "" {
			if _, err := metering.ParseZoneOffset(pr.Timezone); err != nil {
				add("pricing[%s] %v", alias, err)
			}
		}
		for j, tw := range pr.TimeWindows {
			if _, _, err := metering.ParseWindowMinutes(tw.Window); err != nil {
				add("pricing[%s].time_windows[%d] %v", alias, j, err)
			}
			if _, err := metering.ParseMultiplier(tw.Multiplier); err != nil {
				add("pricing[%s].time_windows[%d].multiplier %q 非法(须 (0,10] 内、≤4 位小数)", alias, j, tw.Multiplier)
			}
		}
	}

	// 模型能力目录(ADR-0010):声明制零内置;引用完整性、二元组唯一性、值域在此 fail-fast
	seenCaps := map[string]bool{}
	for i, mc := range c.Models {
		where := fmt.Sprintf("models[%d](%s,%s)", i, mc.Provider, mc.Model)
		if mc.Provider == "" {
			add("models[%d] 缺 provider", i)
			continue
		}
		if _, ok := c.ProviderByName(mc.Provider); !ok {
			add("%s 引用不存在的 provider %q", where, mc.Provider)
		}
		if mc.Model == "" {
			add("models[%d] 缺 model", i)
		}
		key := mc.Provider + "\x00" + mc.Model
		if seenCaps[key] {
			add("%s 重名声明((provider, model) 二元组必须唯一)", where)
		}
		seenCaps[key] = true
		if mc.MaxOutputTokens < 0 {
			add("%s max_output_tokens 不可为负: %d", where, mc.MaxOutputTokens)
		}
		for j, dp := range mc.DropParams {
			// 空白值若放行会与运行时精确匹配永不命中,声明静默失效且零告警——必须 fail-fast
			if strings.TrimSpace(dp) == "" {
				add("%s drop_params[%d] 不可为空", where, j)
				continue
			}
			if dp != strings.TrimSpace(dp) {
				add("%s drop_params[%d] %q 含首尾空白(将导致声明静默失效)", where, j, dp)
			}
			// 协议结构字段是请求骨架而非采样参数:删除必然破坏出站请求,治理语义不覆盖
			switch dp {
			case "model", "messages", "stream":
				add("%s drop_params[%d] %q 为协议结构字段,禁止声明治理", where, j, dp)
			}
		}
	}

	if c.Semantic.Enabled {
		if c.Semantic.Dim == 0 {
			c.Semantic.Dim = 1536
		}
		if c.Semantic.Threshold == 0 {
			c.Semantic.Threshold = 0.97
		}
		if c.Semantic.TTLHours == 0 {
			c.Semantic.TTLHours = 24
		}
		if c.Semantic.Capacity == 0 {
			c.Semantic.Capacity = 10_000
		}
	}
	if c.Semantic.Enabled && strings.TrimSpace(c.Semantic.EmbeddingModel) == "" {
		add("semantic.embedding_model 必填(enabled=true 时)")
	}
	// 引用完整性:嵌入模型别名必须已在 model_routes 定义,否则语义缓存运行期每次嵌入
	// 都被 model_not_found 拒绝,表现为缓存静默全空且启动无任何告警
	if c.Semantic.Enabled && strings.TrimSpace(c.Semantic.EmbeddingModel) != "" {
		if _, ok := c.ModelRoutes[c.Semantic.EmbeddingModel]; !ok {
			add("semantic.embedding_model %q 未在 model_routes 中定义", c.Semantic.EmbeddingModel)
		}
	}
	if c.Semantic.Dim < 0 || c.Semantic.Threshold < 0 || c.Semantic.Threshold > 1 || c.Semantic.TTLHours < 0 || c.Semantic.Capacity < 0 {
		add("semantic 数值配置非法(dim/threshold/ttl_hours/capacity 不可为负,threshold ∈ (0,1])")
	}

	if len(problems) > 0 {
		return errors.New("配置校验失败(全部列出):\n  - " + strings.Join(problems, "\n  - "))
	}
	return nil
}
