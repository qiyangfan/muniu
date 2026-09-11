// Package config 负责 muniu.yaml 的定位、${VAR} 环境展开与启动期校验(ADR-0004:
// 静态拓扑走 YAML,密钥只存环境变量引用名)。配置启动后只读;热加载在 backlog,v0 不做。
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
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

// Price 人类可读价目(百万 Token);数值换算在装配期经 metering.ParsePrice 完成。
// CachedInput 缺省=Input(metering「缓存价」契约)。
type Price struct {
	Currency    string `yaml:"currency"`
	Input       string `yaml:"input"`
	CachedInput string `yaml:"cached_input"`
	Output      string `yaml:"output"`
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
	Metering struct {
		FlushIntervalMs int `yaml:"flush_interval_ms"`
		FlushBatch      int `yaml:"flush_batch"`
	} `yaml:"metering"`
	Providers   []Provider             `yaml:"providers"`
	ModelRoutes map[string][]Candidate `yaml:"model_routes"`
	Pricing     map[string]Price       `yaml:"pricing"`
	Semantic    SemanticCfg            `yaml:"semantic"`
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
		if len(p.Keys) == 0 {
			add("providers[%d](%s) keys 不能为空", i, p.Name)
		}
		for j, k := range p.Keys {
			if k.Env == "" {
				add("providers[%d](%s).keys[%d] 缺 env 引用名", i, p.Name, j)
			}
		}
		if p.Weight <= 0 {
			add("providers[%d](%s) weight 必须 >0, 实际 %d", i, p.Name, p.Weight)
		}
		if p.Priority < 0 {
			add("providers[%d](%s) priority 必须 ≥0, 实际 %d", i, p.Name, p.Priority)
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
			if cd.Priority < 0 {
				add("model_routes[%s].candidates[%d] priority 必须 ≥0", alias, j)
			}
		}
	}

	for alias, pr := range c.Pricing {
		if !legalCurrency[strings.ToUpper(pr.Currency)] {
			add("pricing[%s] currency 非法 %q(允许 CNY|USD)", alias, pr.Currency)
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
