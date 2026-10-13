// muniu 主进程入口:只做装配(配置→存储→日志→适配器→计量→中间件链→代理→优雅退出),
// 禁止业务逻辑(architecture「包结构」)。
package main

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	_ "github.com/solvela/muniu/internal/adapter/bridge" // 接线 anthropic 桥接工厂
	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/alerting"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/metrics"
	"github.com/solvela/muniu/internal/plugin"
	"github.com/solvela/muniu/internal/protocol"
	"github.com/solvela/muniu/internal/proxy"
	"github.com/solvela/muniu/internal/ratelimit"
	"github.com/solvela/muniu/internal/semantic"
	"github.com/solvela/muniu/internal/server"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/version"
	"github.com/solvela/muniu/web"
)

func main() {
	// keys/backup 管理子命令与网关服务分流(同一配置定位与存储)
	if ka, ok := keysSubArgs(os.Args); ok {
		os.Exit(runKeys(flagConfOf(os.Args), ka))
	}
	if ba, ok := backupSubArgs(os.Args); ok {
		os.Exit(runBackup(flagConfOf(os.Args), ba))
	}

	cfgPath := flag.String("c", "", "配置文件路径(缺省依 MUNIU_CONFIG > ./muniu.yaml)")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "muniu: %v\n", err)
		os.Exit(1)
	}
	logger := logging.New(cfg.Log.Level, cfg.Log.Format)
	slog.SetDefault(logger)

	cfgResolved, lerr := config.Locate(*cfgPath)
	if lerr != nil {
		fmt.Fprintf(os.Stderr, "muniu: %v\n", lerr)
		os.Exit(1)
	}
	handler, _, cleanup, err := assemble(cfg, cfgResolved, logger)
	if err != nil {
		logger.Error("装配失败", "error_detail", err.Error())
		os.Exit(1)
	}
	defer cleanup()

	// MUNIU_ADDR 环境变量仍覆盖配置地址(本地调试便利,phase-1 计划约定)
	addr := cfg.Server.Addr
	if p := os.Getenv("MUNIU_ADDR"); p != "" {
		addr = p
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := server.Listen(addr)
	if err != nil {
		logger.Error("监听失败", "addr", addr, "error_detail", err.Error())
		cleanup() // 计量存量是钱,任何退出路径都必须排干(os.Exit 不执行 defer)
		os.Exit(1)
	}
	srv := server.New(addr, handler)
	logger.Info("muniu 启动", "addr", addr, "version", version.Version)

	if err := server.Run(ctx, srv, ln, logger); err != nil {
		logger.Error("服务退出", "error_detail", err.Error())
		cleanup()
		os.Exit(1)
	}
}

// flagConfOf 提取全局 -c 配置路径(keys 分流需在 flag.Parse 前手工解析)。
func flagConfOf(args []string) string {
	for i, a := range args {
		if a == "-c" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "-c=") {
			return strings.TrimPrefix(a, "-c=")
		}
	}
	return ""
}

// assemble 装配 HTTP 处理链:
//
//	/healthz                         → 轻链(request_id/recover/access_log)
//	POST /v1/chat/completions        → 全链(request_id/recover/access_log/auth/quota/ratelimit)
//	                                   → handler(解析→路由→单候选→转发→计量)
//
// 顺序即契约(architecture「中间件链」)。返回的 cleanup 在进程退出前排干计量管道。
// routeEntry 业务路由表条目(单源):注册循环与漂移门禁(assemble 返回的 declared)同源消费。
// 业务模式曾三处手写(chatMux/root.Handle/routes 清单),失步必然发生——/v1/messages
// 曾因此 404(2026-09-26);新增入站形态只改本表,不可能再出现「挂了链没注册」的半状态。
type routeEntry struct {
	Pattern string
	Handler http.Handler
}

func assemble(cfg *config.Config, cfgPath string, logger *slog.Logger) (http.Handler, []string, func(), error) {
	// SQLite:运行态(api_keys/usage_logs);迁移随打开执行
	db, err := store.Open(cfg.Storage.SQLite.Path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("打开存储: %w", err)
	}
	// 告警通知器(webhook-alerting):未配置 webhook_url 为 nil,各接入点 Emit 零开销
	alerts := alerting.New(alerting.Options{
		WebhookURL:  cfg.Alerts.WebhookURL,
		DedupWindow: time.Duration(cfg.Alerts.DedupWindowSec) * time.Second,
		Logger:      logger,
	})
	// 刷盘参数接线(缺省值已在 config.validate 兜底):配置-行为不可漂移。
	// sink=ingest:远端汇 + 本地缓冲(投递失败整批落缓冲,恢复按序续传——ADR-0007)
	meteringOpts := metering.Options{
		Interval: time.Duration(cfg.Metering.FlushIntervalMs) * time.Millisecond,
		Batch:    cfg.Metering.FlushBatch,
		OnSinkDown: func(sink string, rows int) {
			alerts.Emit("metering_sink_down", "critical",
				fmt.Sprintf("计量远端汇 %s 投递失败,%d 行落本地缓冲", sink, rows))
		},
	}
	if cfg.Metering.Sink == "ingest" {
		bufferCap := cfg.Metering.BufferCapacity
		if bufferCap <= 0 {
			bufferCap = 1_000_000
		}
		meteringOpts.Remote = &metering.IngestSink{URL: cfg.Metering.IngestURL, Token: cfg.Metering.IngestToken}
		meteringOpts.Buffer = metering.NewBufferStore(db, bufferCap)
	}
	pipeline := metering.NewPipeline(db, meteringOpts, logger)
	tpm := ratelimit.NewTPM()
	// 限流计数后端(ADR-0008 修订):inmem 缺省(实例自治,零变更);redis 共享视图(cluster)。
	var sharedBackend ratelimit.SharedBackend
	var tpmRecorder ratelimit.TPMRecorder = tpm
	switch cfg.RateLimit.Backend {
	case "", "inmem":
	case "redis":
		if cfg.RateLimit.RedisAddr == "" {
			logger.Error("ratelimit.backend=redis 需要配置 ratelimit.redis_addr")
			os.Exit(1)
		}
		shared := ratelimit.NewRedisBackend(cfg.RateLimit.RedisAddr, logger)
		sharedBackend = shared
		tpmRecorder = shared
		logger.Info("限流共享视图启用(redis)", "addr", cfg.RateLimit.RedisAddr)
	default:
		logger.Error("ratelimit.backend 须为 inmem|redis", "got", cfg.RateLimit.Backend)
		os.Exit(1)
	}

	// 适配器注册:Phase 2 仅 openai 基座+quirks;key 取 provider 第一把 key 的环境变量值
	// buildPool 同时服务启动构建与拓扑热替换(3.2 控制面源):env 解析+建池+adapter 注册一体
	registry := adapter.NewRegistry()
	buildPool := func(prov config.Provider) (*keypool.Pool, adapter.Adapter, error) {
		if len(prov.Keys) == 0 {
			return nil, nil, fmt.Errorf("provider %s 无可用 key 引用", prov.Name)
		}
		inputs := make([]keypool.KeyInput, 0, len(prov.Keys))
		first := ""
		for i, kr := range prov.Keys {
			v := os.Getenv(kr.Env)
			if v == "" {
				return nil, nil, fmt.Errorf("provider %s 的 key 环境变量 %s 未设置", prov.Name, kr.Env)
			}
			if i == 0 {
				first = v
			}
			inputs = append(inputs, keypool.KeyInput{ID: kr.Env, Value: v, Weight: kr.Weight})
		}
		ad, err := adapter.For(prov.Adapter, prov.BaseURL, first, adapter.Options{Auth: prov.Auth})
		if err != nil {
			return nil, nil, fmt.Errorf("provider %s: %w", prov.Name, err)
		}
		registry.Register(prov.Name, ad) // 启动装配容器;转发路径改读拓扑快照(3.1)
		return keypool.NewPool(inputs), ad, nil
	}
	pools := make(map[string]*keypool.Pool, len(cfg.Providers))
	for _, p := range cfg.Providers {
		pool, _, err := buildPool(p)
		if err != nil {
			return nil, nil, nil, err
		}
		pools[p.Name] = pool
	}

	// 价目:人类可读 → credit 整数(加载期一次性换算,全链路零浮点);
	// 分时段倍率窗在同一次加载期换算出各窗有效三段价,运行时查表(metering.md「计价」)。
	// credit 锚定本位币(metering.settlement,缺省 CNY);异币种报价按 fx_usd_cny_milli
	// 一次换算(构造价目表时执行,热路径零变化)。
	// YAML = 静态兜底;model_pricing 表 = 运营覆盖(管理面在线改价),合并语义见 pricing.go
	// 本位币规范化:大小写归一 + 空值缺省 CNY(validate 只校验未写回,装配侧兜底)
	settlement := strings.ToUpper(cfg.Metering.Settlement)
	if settlement == "" {
		settlement = metering.SettlementCNY
	}
	fxCnyPerUsdMilli := cfg.Metering.FXUSDCNYMilli
	staticDefs := make(map[string]staticPricing, len(cfg.Pricing))
	for alias, pr := range cfg.Pricing {
		inMicro, err := metering.ParsePrice(pr.Input)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("pricing[%s].input: %w", alias, err)
		}
		outMicro, err := metering.ParsePrice(pr.Output)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("pricing[%s].output: %w", alias, err)
		}
		cachedMicro := int64(0) // 缺省=input(CostWithCached 内部处理)
		if pr.CachedInput != "" {
			cachedMicro, err = metering.ParsePrice(pr.CachedInput)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("pricing[%s].cached_input: %w", alias, err)
			}
		}
		def := metering.Pricing{
			Currency:               strings.ToUpper(pr.Currency),
			InputCreditsPerM:       inMicro,
			CachedInputCreditsPerM: cachedMicro,
			OutputCreditsPerM:      outMicro,
		}
		zone := time.UTC
		if pr.Timezone != "" {
			zone, err = metering.ParseZoneOffset(pr.Timezone)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("pricing[%s].timezone: %w", alias, err)
			}
		}
		specs := make([]metering.WindowSpec, 0, len(pr.TimeWindows))
		for _, tw := range pr.TimeWindows {
			s, e, err := metering.ParseWindowMinutes(tw.Window)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("pricing[%s]: %w", alias, err)
			}
			bp, err := metering.ParseMultiplier(tw.Multiplier)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("pricing[%s]: %w", alias, err)
			}
			specs = append(specs, metering.WindowSpec{StartMin: s, EndMin: e, BasisPoints: bp})
		}
		staticDefs[alias] = staticPricing{def: def, zone: zone, specs: specs}
	}
	rebuildPricing := func() (map[string]*metering.PricingTable, error) {
		return buildPricingTables(db, staticDefs, settlement, fxCnyPerUsdMilli)
	}
	initialTables, err := rebuildPricing()
	if err != nil {
		return nil, nil, nil, err
	}
	pricingDir := metering.NewPricingDirectory(initialTables)
	// 静态价目读侧(GET /admin/v1/pricing 的 yaml 来源行):窗口/时区如实透出,
	// 防止控制台编辑 yaml 行时静默丢窗
	staticHuman := make(map[string]admin.StaticEntry, len(staticDefs))
	for alias, sp := range staticDefs {
		wins := make([]admin.PricingWindowView, 0, len(sp.specs))
		for _, spec := range sp.specs {
			wins = append(wins, admin.PricingWindowView{
				Window:     formatClockMinutes(spec.StartMin) + "-" + formatClockMinutes(spec.EndMin),
				Multiplier: metering.FormatMultiplier(spec.BasisPoints),
			})
		}
		staticHuman[alias] = admin.StaticEntry{Def: sp.def, Timezone: sp.zone.String(), TimeWindows: wins}
	}

	// 语义缓存(T4.5):租户开关(默认关)+ HNSW 索引 + 进程内自调用嵌入
	var cacheInst *semantic.Cache
	var embedKey string
	late := &lateEmbedder{}
	if cfg.Semantic.Enabled {
		c, err := semantic.NewCache(db, semantic.Options{
			Dim: cfg.Semantic.Dim, Threshold: cfg.Semantic.Threshold,
			TTL: time.Duration(cfg.Semantic.TTLHours) * time.Hour, Capacity: cfg.Semantic.Capacity,
			EmbeddingModel: cfg.Semantic.EmbeddingModel,
		}, late)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("初始化语义缓存: %w", err)
		}
		cacheInst = c
		embedKey = ensureInternalEmbedKey(db, logger)
		if embedKey == "" {
			return nil, nil, nil, errors.New("内部嵌入 key 签发失败")
		}
	}
	tenantSw := func(tenantID string) bool {
		var en int64
		if err := db.QueryRow(`SELECT semantic_cache_enabled FROM tenants WHERE id = ?`, tenantID).Scan(&en); err != nil {
			return false
		}
		return en == 1
	}

	// WASM 插件注册表(ADR-0011):YAML 静态声明注册(运行态增改走管理面,重启回落本声明)。
	// 注册即校验:wasm magic → 编译 → 探测实例 → ABI 版本 → plugin_start,任一失败启动即断(fail-fast)。
	pluginReg := plugin.NewRegistry() // 恒建:管理面运行态注册端点依赖;空表快照零堆分配
	{
		for _, pc := range cfg.Plugins {
			bin, perr := os.ReadFile(pc.Path)
			if perr != nil {
				return nil, nil, nil, fmt.Errorf("插件 %s 读取失败: %w", pc.Name, perr)
			}
			mib := pc.MemoryLimitMiB
			if mib == 0 {
				mib = 64 // plugin.ResourceDefaults 对齐;config 层保持零值语义
			}
			if _, _, rerr := pluginReg.Register(plugin.Options{
				Name:             pc.Name,
				Binary:           bin,
				Config:           pc.Config,
				Enabled:          pc.Enabled != nil && *pc.Enabled,
				FailOpen:         pc.FailOpen,
				TimeoutMs:        pc.TimeoutMs,
				MemoryLimitPages: uint32(mib) * 16, // 64KiB/页
				PoolSize:         pc.PoolSize,
				Logger:           logger,
			}); rerr != nil {
				return nil, nil, nil, fmt.Errorf("插件 %s 注册失败: %w", pc.Name, rerr)
			}
			logger.Info("WASM 插件已注册", "plugin", pc.Name, "fail_open", pc.FailOpen)
		}
	}

	chatProxy := proxy.New(proxy.Config{
		Registry:  registry,
		Providers: cfg.Providers,
		Routes:    cfg.ModelRoutes,
		Pricing:   pricingDir,
		Timeouts: proxy.Timeouts{
			Dial:       cfg.Server.Timeouts.DialDuration(),
			FirstToken: cfg.Server.Timeouts.FirstTokenDuration(),
			ChunkIdle:  cfg.Server.Timeouts.ChunkIdleDuration(),
		},
		Pipeline:         pipeline,
		Pools:            pools,
		TPM:              tpmRecorder,
		Cache:            cacheInst,
		TenantSwitch:     tenantSw,
		Capabilities:     cfg.Models,
		StrictGovernance: cfg.StrictGovernance,
		Plugins:          pluginReg,
		Alerts:           alerts,
		Logger:           logger,
	})

	// 控制面拓扑热替换(3.2 双驱动的控制面侧;文件源为缺省,配置 topology_url 后启用):
	// 启动即拉、ETag 条件请求(304 跳过)、失败/损坏保持当前快照并告警降级(3.3);
	// 池构建复用启动闭包 buildPool(env 解析+adapter 注册),配置未变 provider 运行态无损
	if cfg.ControlPlane.TopologyURL != "" {
		interval := time.Duration(cfg.ControlPlane.PollIntervalSec) * time.Second
		if interval <= 0 {
			interval = 60 * time.Second
		}
		go func() {
			client := &http.Client{Timeout: 10 * time.Second}
			etag := ""
			syncOnce := func() {
				payload, newETag, notModified, err := config.FetchTopology(client, cfg.ControlPlane.TopologyURL, etag)
				if err != nil {
					logger.Warn("控制面拓扑拉取失败,保持当前快照", "error_detail", err.Error())
					return
				}
				if notModified {
					return
				}
				providers, routes, models := payload.ToTopology()
				stats, rerr := chatProxy.ReplaceTopology(proxy.Config{
					Providers:    providers,
					Routes:       routes,
					Capabilities: models,
				}, buildPool)
				if rerr != nil {
					logger.Warn("拓扑替换被拒绝,保持当前快照", "error_detail", rerr.Error())
					return
				}
				etag = newETag
				logger.Info("控制面拓扑已热替换", "reused", stats.Reused,
					"created", stats.Created, "removed", stats.Removed)
			}
			syncOnce() // 启动即拉,不等首个周期
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for range ticker.C {
				syncOnce()
			}
		}()
	}

	// SIGHUP 配置热加载(sighup-config-reload;运维手势 → 既有原子热替换机器):
	// 重读配置文件,校验失败保持旧态告警;成功则拓扑/池/适配器/能力目录经
	// ReplaceTopology 原子热替换(provider 未变时池运行态无损,与控制面拉取同一入口)。
	// 范围登记:热替换仅覆盖上述拓扑域,其余装配一律不热更(auth/metrics/metering 刷盘参数/
	// 管理面/静态价目兜底/告警装配/日志与超时等;价目运营态走管理面,webhook 变更重启生效,
	// 与 platform-data-plane 热更登记同文)。
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	go func() {
		for range sighup {
			newCfg, lerr := config.Load(cfgPath)
			if lerr != nil {
				logger.Warn("SIGHUP 重载失败,保持当前配置", "error_detail", lerr.Error())
				continue
			}
			stats, rerr := chatProxy.ReplaceTopology(proxy.Config{
				Providers:    append([]config.Provider(nil), newCfg.Providers...),
				Routes:       newCfg.ModelRoutes,
				Capabilities: newCfg.Models,
			}, buildPool)
			if rerr != nil {
				logger.Warn("SIGHUP 拓扑替换被拒绝,保持当前快照", "error_detail", rerr.Error())
				continue
			}
			logger.Info("SIGHUP 配置已热加载", "reused", stats.Reused,
				"created", stats.Created, "removed", stats.Removed)
		}
	}()

	// 业务路由全链:顺序即契约 request_id → recover → access_log → auth → quota → ratelimit
	platformSecrets := make([][]byte, 0, len(cfg.Auth.PlatformKeySecrets))
	for _, sec := range cfg.Auth.PlatformKeySecrets {
		if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sec)); err == nil && len(raw) >= 16 {
			platformSecrets = append(platformSecrets, raw)
		}
	}
	verifier := auth.NewVerifier(db, auth.Options{
		PlatformSecrets:     platformSecrets,
		RevocationsURL:      cfg.Auth.RevocationsURL,
		RevocationsInterval: time.Duration(cfg.Auth.RevocationsIntervalSec) * time.Second,
	}, logger)
	gate := &ratelimit.Gate{
		RPM:    ratelimit.New(),
		TPM:    tpm,
		Quota:  ratelimit.NewQuota(db, ratelimit.Options{}, logger),
		Logger: logger,
		Shared: sharedBackend,
	}
	// 业务路由表(单源):模式在此只写一次,注册循环与漂移门禁(assemble 返回的 declared)
	// 同源消费——曾三处手写(chatMux/root.Handle/routes 清单)必然失步,/v1/messages 因此 404 过。
	// 中间件分组:数据面全链(auth+配额+限流)/模型发现(仅鉴权)/探活(轻链);顺序即契约。
	dataPlaneMW := []server.Middleware{
		server.RequestID, server.Recover(logger), server.AccessLog(logger),
		verifier.Middleware, gate.Middleware,
	}
	readonlyMW := []server.Middleware{
		server.RequestID, server.Recover(logger), server.AccessLog(logger),
		verifier.Middleware,
	}
	entries := []routeEntry{
		{"POST /v1/chat/completions", server.Chain(chatProxy.Handler(), dataPlaneMW...)},
		{"POST /v1/embeddings", server.Chain(chatProxy.HandlerEmbeddings(), dataPlaneMW...)},
		// Responses 入站(ADR-0012):翻译后进同一管道,鉴权/配额/限流与 chat 同规
		{"POST /v1/responses", server.Chain(chatProxy.HandlerResponses(), dataPlaneMW...)},
		// Anthropic Messages 入站(ADR-0013):同款边缘翻译;鉴权头适配仅挂本路由,先于 verifier
		{"POST /v1/messages", server.Chain(chatProxy.HandlerMessages(),
			server.RequestID, server.Recover(logger), server.AccessLog(logger),
			server.AdaptAnthropicKey, verifier.Middleware, gate.Middleware)},
		// Gemini 原生入站(gemini-native-protocol):同款边缘翻译;鉴权头/查询参数适配仅挂
		// 本路由,先于 verifier(?key= 读后剥离,防访问日志泄漏);{modelMethod} 整段通配,
		// handler 内解析 {model}:{method} 后缀(mux 通配符不支持段内混合)
		{"POST /v1beta/models/{modelMethod}", server.Chain(chatProxy.HandlerGemini(),
			server.RequestID, server.Recover(logger), server.AccessLog(logger),
			server.AdaptGeminiKey, verifier.Middleware, gate.Middleware)},
		// 无状态边界:不存储 response,恒 404(挂鉴权链,不泄漏给未鉴权方)
		{"GET /v1/responses/{id}", server.Chain(chatProxy.HandlerResponsesStateless(), dataPlaneMW...)},
		{"DELETE /v1/responses/{id}", server.Chain(chatProxy.HandlerResponsesStateless(), dataPlaneMW...)},
		// 模型发现(gateway-parity):数据面鉴权必须,但刻意不挂配额/限流/计量/插件——
		// 只读元数据面零上游成本,且配额耗尽的客户端恰需列模型诊断(诊断链路不被成本闸阻断)
		{"GET /v1/models", server.Chain(chatProxy.HandlerModels(), readonlyMW...)},
		{"GET /v1/models/{model}", server.Chain(chatProxy.HandlerModels(), readonlyMW...)},
		{"GET /healthz", server.Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version.Version})
		}), server.RequestID, server.Recover(logger), server.AccessLog(logger))},
	}

	// 管理面(T4.1):MUNIU_ADMIN_TOKEN 未配置则整体关闭(含控制台——无后端的空壳页
	// 不该对外暴露,与告警文案保持一致)
	adminToken := os.Getenv("MUNIU_ADMIN_TOKEN")
	if adminToken == "" {
		logger.Warn("MUNIU_ADMIN_TOKEN 未配置,管理面与控制台关闭")
	}
	refreshPricing := func() error {
		tables, err := rebuildPricing()
		if err != nil {
			return err
		}
		pricingDir.Replace(tables)
		return nil
	}
	adminDeps := admin.Deps{
		DB: db, Token: adminToken, Providers: cfg.Providers, Pools: pools,
		Verifier: verifier, Logger: logger,
		PricingDir:     pricingDir,
		StaticPricing:  staticHuman,
		RefreshPricing: refreshPricing,
		Plugins:        pluginReg,
	}
	if cacheInst != nil {
		adminDeps.CacheStats = cacheInst.Stats
	}
	adminHandler := admin.New(adminDeps)

	// 探活已入业务路由表(轻链,不鉴权)。

	// 可观测(ADR-0009):启用且管理 token 已配置时暴露 /metrics(token 门禁,
	// 恒时比较防时序侧信道;Recover 防仪表函数 panic 裸断连接)。无 token 不挂载。
	metricsEnabled := cfg.Metrics.Enabled && adminToken != ""
	if cfg.Metrics.Enabled && adminToken == "" {
		logger.Warn("metrics.enabled=true 但 MUNIU_ADMIN_TOKEN 未配置,/metrics 不挂载(ADR-0009 管理面鉴权纪律)")
	}
	metrics.SetEnabled(metricsEnabled)
	if metricsEnabled {
		metrics.NewGaugeFunc("muniu_pipeline_in_flight", "计量管道在途水位", func() []metrics.Sample {
			return []metrics.Sample{{Value: float64(pipeline.InFlight())}}
		})
		metrics.NewGaugeFunc("muniu_metering_buffer_rows", "计量本地缓冲行数", func() []metrics.Sample {
			n, err := store.CountBufferedRows(db)
			if err != nil {
				return nil
			}
			return []metrics.Sample{{Value: float64(n)}}
		})
		metrics.NewGaugeFunc("muniu_keypool_state", "key 池状态计数", func() []metrics.Sample {
			now := time.Now()
			var out []metrics.Sample
			for name, pool := range pools {
				for state, n := range pool.StateCounts(now) {
					out = append(out, metrics.Sample{Labels: [][2]string{{"provider", name}, {"state", state}}, Value: float64(n)})
				}
			}
			return out
		})
	}

	// 注册:模式只在路由表写一次;/admin/v1/ 为子树挂载(模式集由 admin.Routes() 单源回报),
	// /console/ 静态资源不在契约内
	root := http.NewServeMux()
	for _, e := range entries {
		root.Handle(e.Pattern, e.Handler)
	}
	declared := make([]string, 0, len(entries)+len(admin.Routes())+1)
	for _, e := range entries {
		declared = append(declared, e.Pattern)
	}
	if adminHandler != nil {
		root.Handle("/admin/v1/", adminHandler)
		root.Handle("GET /console/", web.Handler())
		declared = append(declared, admin.Routes()...)
	}
	if metricsEnabled {
		metricsHandler := server.Recover(logger)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(adminToken)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
					protocol.TypeAuthFailed, "auth_failed", "管理 token 无效", ""))
				return
			}
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			metrics.RenderTo(w)
		}))
		root.Handle("GET /metrics", metricsHandler)
		declared = append(declared, "GET /metrics")
	}

	// 控制面价目拉取(ADR-0005 状态同步增量 #1):响应形态与管理 API 一致,覆盖写入后刷新目录。
	// 全量期望语义仅 upsert(撤销覆盖仍走管理面 DELETE);失败保持原目录,下轮重试
	if cfg.ControlPlane.PricingURL != "" {
		interval := time.Duration(cfg.ControlPlane.PollIntervalSec) * time.Second
		if interval <= 0 {
			interval = 60 * time.Second
		}
		go func() {
			client := &http.Client{Timeout: 10 * time.Second}
			pendingRefresh := false // 上轮刷新失败的待重试标记(比对跳过不得短路刷新)
			syncOnce := func() {
				resp, err := client.Get(cfg.ControlPlane.PricingURL)
				if err != nil {
					logger.Warn("控制面价目拉取失败", "error_detail", err.Error())
					return
				}
				var body struct {
					Pricing []admin.PricingView `json:"pricing"`
				}
				err = json.NewDecoder(resp.Body).Decode(&body)
				_ = resp.Body.Close()
				if resp.StatusCode != http.StatusOK || err != nil {
					logger.Warn("控制面价目响应非法", "status", resp.StatusCode)
					return
				}
				now := time.Now()
				changed := false
				for _, item := range body.Pricing {
					if item.Alias == "" {
						continue
					}
					rec, perr := admin.ParsePricingRecord(item.Alias, item, now)
					if perr != nil {
						logger.Warn("控制面价目条目非法,跳过", "alias", item.Alias, "error_detail", perr.Error())
						continue
					}
					// 内容未变化则跳过:避免 updated_at 失去「最后真实变更」语义与无谓写放大
					if existing, gerr := store.GetPricing(db, rec.Alias); gerr == nil &&
						existing.Currency == rec.Currency && existing.InputCredits == rec.InputCredits &&
						existing.CachedInputCredits == rec.CachedInputCredits && existing.OutputCredits == rec.OutputCredits &&
						existing.Timezone == rec.Timezone && windowsEqual(existing.Windows, rec.Windows) {
						continue
					}
					if uerr := store.UpsertPricing(db, rec); uerr != nil {
						logger.Warn("控制面价目落库失败", "alias", item.Alias, "error_detail", uerr.Error())
						continue
					}
					changed = true
					pendingRefresh = true
				}
				if changed || pendingRefresh {
					if rerr := refreshPricing(); rerr != nil {
						pendingRefresh = true
						logger.Warn("控制面价目刷新失败(下轮重试)", "error_detail", rerr.Error())
					} else {
						pendingRefresh = false
						logger.Info("控制面价目已同步生效", "entries", len(body.Pricing))
					}
				}
			}
			syncOnce() // 启动即拉,不等首个周期
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for range ticker.C {
				syncOnce()
			}
		}()
	}

	// 嵌入通道绑定:HTTP 链就绪后进程内自调用 /v1/embeddings
	if cacheInst != nil {
		bindEmbedder(late, root, embedKey)
	}

	cleanup := func() {
		late.set(func(context.Context, string, string) ([]float32, error) {
			return nil, errors.New("已关闭")
		})
		if pluginReg != nil {
			pluginReg.Close() // 等待在途插件调用完成后关停(热替换同一语义)
		}
		verifier.Close()
		_ = pipeline.Close() // 退出前排干计量存量
	}
	return root, declared, cleanup, nil
}
