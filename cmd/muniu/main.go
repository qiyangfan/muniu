// muniu 主进程入口：只做装配与优雅退出，禁止业务逻辑。
// 装配顺序：配置→存储→日志→适配器→计量→中间件链→代理。
package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
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
	"sync"
	"syscall"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	_ "github.com/solvela/muniu/internal/adapter/bridge" // 接线 anthropic 桥接工厂
	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/alerting"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/balance"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/health"
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
	if aa, ok := adminsSubArgs(os.Args); ok {
		os.Exit(runAdmins(flagConfOf(os.Args), aa))
	}
	// keys/backup 管理子命令与网关服务分流，同一配置定位与存储。
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

	// MUNIU_ADDR 环境变量优先于配置中的监听地址。
	addr := cfg.Server.Addr
	if p := os.Getenv("MUNIU_ADDR"); p != "" {
		addr = p
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ln, err := server.Listen(addr)
	if err != nil {
		logger.Error("监听失败", "addr", addr, "error_detail", err.Error())
		cleanup() // 计量存量是钱，任何退出路径都必须排干；os.Exit 不执行 defer
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

// flagConfOf 提取全局 -c 配置路径，keys 分流需在 flag.Parse 前手工解析。
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

// routeEntry 描述路由注册用的路径和 handler。
type routeEntry struct {
	Pattern string
	Handler http.Handler
}

// assemble 装配 HTTP 处理链并注册路由；返回的 cleanup 会在退出前排干计量管道。
func assemble(cfg *config.Config, cfgPath string, logger *slog.Logger) (http.Handler, []string, func(), error) {
	// SQLite 运行态库（api_keys/usage_logs），迁移随打开执行。
	db, err := store.Open(cfg.Storage.SQLite.Path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("打开存储: %w", err)
	}
	// 告警通知器：未配置 webhook_url 时为 nil，各接入点 Emit 零开销。
	// 投递终局回调落库自监控:通道挂了不能再用通道自报(递归陷阱),失败事实只落库走管理面可见。
	alerts := alerting.New(alerting.Options{
		WebhookURL:  cfg.Alerts.WebhookURL,
		DedupWindow: time.Duration(cfg.Alerts.DedupWindowSec) * time.Second,
		Logger:      logger,
		OnDeliveryFailure: func(f alerting.DeliveryFailure) {
			if err := store.RecordAlertDeliveryFailure(db, f.EventKey, f.Severity, f.Summary, f.ErrorClass, f.At); err != nil {
				logger.Error("告警投递失败落库失败", "error_detail", err.Error())
			}
		},
		OnDeliverySuccess: func(eventKey string, at time.Time) {
			if err := store.RecordAlertDeliverySuccess(db, at); err != nil {
				logger.Error("告警投递解除落库失败", "error_detail", err.Error())
			}
		},
	})
	// 刷盘参数接线，缺省值已在 config.validate 兜底。
	// sink=ingest 时为远端汇加本地缓冲：投递失败整批落缓冲，恢复后按序续传。
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
	// 限流计数后端：inmem 缺省（实例自治，零变更）；redis 为集群共享视图。
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

	// buildPool 同时用于启动装配和拓扑热替换。
	registry := adapter.NewRegistry()
	buildPool := func(prov config.Provider) (*keypool.Pool, adapter.Adapter, error) {
		if len(prov.Keys) == 0 {
			return nil, nil, fmt.Errorf("provider %s 无可用 key 引用", prov.Name)
		}
		inputs := make([]keypool.KeyInput, 0, len(prov.Keys))
		first := ""
		for i, kr := range prov.Keys {
			var v string
			if kr.Account != "" {
				// 账号引用:从账号存储解析当前凭据(装配层唯一解析点,oauth 包不依赖 store);
				// 不存在即装配失败——文件源启动 fail-fast,控制面/热替换路径快照被拒(旧快照保留)。
				row, gerr := store.GetOAuthAccount(db, kr.Account)
				if gerr != nil {
					return nil, nil, fmt.Errorf("provider %s 的账号引用 %s 不可用: %w", prov.Name, kr.Account, gerr)
				}
				v = row.AccessToken
				if v == "" {
					return nil, nil, fmt.Errorf("provider %s 的账号引用 %s 凭据为空", prov.Name, kr.Account)
				}
			} else {
				v = os.Getenv(kr.Env)
				if v == "" {
					return nil, nil, fmt.Errorf("provider %s 的 key 环境变量 %s 未设置", prov.Name, kr.Env)
				}
			}
			if i == 0 {
				first = v
			}
			// 池成员 ID = 引用身份(env 名或账号 ID):冷却落库/亲和提示/账号同步同键对齐
			id := kr.Env
			if kr.Account != "" {
				id = kr.Account
			}
			inputs = append(inputs, keypool.KeyInput{ID: id, Value: v, Weight: kr.Weight})
		}
		ad, err := adapter.For(prov.Adapter, prov.BaseURL, first, adapter.Options{
			Auth: prov.Auth, NativeMultimodalEmbeddingURL: prov.NativeMultimodalEmbeddingsURL,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("provider %s: %w", prov.Name, err)
		}
		registry.Register(prov.Name, ad) // 启动装配登记，转发路径改读拓扑快照
		pool := keypool.NewPool(inputs)
		// 冷却持久化(credit 桶全程/rate 长冷却):加载恢复 → 注 sink(≥5min 迁移落库)→ 孤儿对账;
		// 加载失败降级为空恢复(最坏多探活一次),对账失败留痕不影响转发。
		if rows, lerr := store.LoadKeyCooldowns(db, prov.Name); lerr != nil {
			logger.Warn("冷却行加载失败,重启恢复降级", "provider", prov.Name, "error_detail", lerr.Error())
		} else {
			pool.LoadCooldowns(cooldownRecords(rows), time.Now())
		}
		if perr := store.PruneKeyCooldowns(db, prov.Name, keyIDSet(pool.KeyIDs())); perr != nil {
			logger.Warn("冷却孤儿对账失败", "provider", prov.Name, "error_detail", perr.Error())
		}
		return pool.WithSink(prov.Name, cooldownSink{db: db, logger: logger}), ad, nil
	}
	pools := make(map[string]*keypool.Pool, len(cfg.Providers))
	for _, p := range cfg.Providers {
		pool, _, err := buildPool(p)
		if err != nil {
			return nil, nil, nil, err
		}
		pools[p.Name] = pool
	}

	// 价目在加载期一次性换算为 credit 整数，全程零浮点；分时段倍率窗同样预先算出各窗有效三段价。
	// credit 锚定本位币（缺省 CNY），异币种报价按 fx_usd_cny_milli 在此一次性换算，热路径只查表。
	// YAML 为静态兜底，model_pricing 表为运营覆盖；本位币空值在此补 CNY（validate 只校验不写回）。
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
		cachedMicro := int64(0) // 缺省即 input，由 CostWithCached 内部处理
		if pr.CachedInput != "" {
			cachedMicro, err = metering.ParsePrice(pr.CachedInput)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("pricing[%s].cached_input: %w", alias, err)
			}
		}
		visionMicro := int64(0) // 缺省视觉价回落 input，由 CostWithVision 处理
		if pr.VisionInput != "" {
			visionMicro, err = metering.ParsePrice(pr.VisionInput)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("pricing[%s].vision_input: %w", alias, err)
			}
		}
		def := metering.Pricing{
			Currency:               strings.ToUpper(pr.Currency),
			InputCreditsPerM:       inMicro,
			CachedInputCreditsPerM: cachedMicro,
			VisionInputCreditsPerM: visionMicro,
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
	imageDir, staticImages, updateImages, err := assembleImagePricing(db, cfg, settlement, fxCnyPerUsdMilli)
	if err != nil {
		return nil, nil, nil, err
	}
	// 静态价目读侧（GET /admin/v1/pricing 的 yaml 来源行）如实透出窗口与时区，
	// 防止控制台编辑 yaml 行时静默丢窗。
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

	// 语义缓存由租户开关控制，嵌入请求经进程内 HTTP 路由发送。
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

	// WASM 插件注册表：YAML 静态声明注册，运行态增改走管理面，重启回落本声明。
	// 注册即校验：wasm magic → 编译 → 探测实例 → ABI 版本 → plugin_start，任一失败启动即断。
	pluginReg := plugin.NewRegistry() // 恒建：管理面运行态注册端点依赖，空表快照零堆分配
	{
		for _, pc := range cfg.Plugins {
			bin, perr := os.ReadFile(pc.Path)
			if perr != nil {
				return nil, nil, nil, fmt.Errorf("插件 %s 读取失败: %w", pc.Name, perr)
			}
			mib := pc.MemoryLimitMiB
			if mib == 0 {
				mib = 64 // 与 plugin.ResourceDefaults 对齐，config 层保持零值语义
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

	// 上游健康观测与熔断:免费控制面探针(缺省开,渠道可退订,适配器能力闸)+ 付费推理探针
	// (probe_model 声明制)+ 被动统计;降级渠道由 proxy 候选域收缩自动降级路由。
	// Sources 运行期经拓扑快照动态映射 key(热替换感知),探针零触碰 Pick/Report。
	var chatProxy *proxy.Proxy
	var healthMgr *health.Manager
	if cfg.Health.Enabled == nil || *cfg.Health.Enabled {
		channelBase := map[string]health.Channel{}
		for _, p := range cfg.Providers {
			// 全渠道入 Sources:无 key 渠道零探针载体,但被动统计与状态推进(open 计时到期转半开)
			// 依赖周期 tick 触达——排除在外会让被动降级永不进入试探期,即永久熔断。
			ch := health.Channel{Name: p.Name, Adapter: p.Adapter, BaseURL: p.BaseURL, Auth: p.Auth}
			free := cfg.Health.FreeProbeEnabled == nil || *cfg.Health.FreeProbeEnabled
			if p.Health != nil && p.Health.FreeProbe != nil {
				free = *p.Health.FreeProbe
			}
			// 探针闸:适配器不支持探针(gemini 原生 v1,登记限制)或无 key 载体时静默跳过,被动统计照常
			probes := health.ProbeSupported(p.Adapter) && len(p.Keys) > 0
			ch.FreeProbe = free && probes
			if p.Health != nil && p.Health.ProbeModel != "" && probes {
				ch.ProbeModel = p.Health.ProbeModel
			}
			channelBase[p.Name] = ch
		}
		healthMgr = health.NewManager(health.Options{
			Interval:         time.Duration(cfg.Health.ProbeIntervalSec) * time.Second,
			Timeout:          time.Duration(cfg.Health.ProbeTimeoutSec) * time.Second,
			FailureThreshold: cfg.Health.FailureThreshold,
			OpenFor:          time.Duration(cfg.Health.OpenSeconds) * time.Second,
			SwitchWindow:     time.Duration(cfg.Health.SwitchWindowSec) * time.Second,
			SwitchMinSamples: cfg.Health.SwitchMinSamples,
			SwitchRateMilli:  cfg.Health.SwitchRateMilli,
			Logger:           logger,
			Sources: func() map[string]health.Channel {
				if chatProxy == nil {
					return nil // 首轮探针早于 proxy 装配完成:空集跳过,下一轮拓扑已就绪
				}
				providers, pools := chatProxy.Topology()
				out := make(map[string]health.Channel, len(channelBase))
				for _, p := range providers {
					base, ok := channelBase[p.Name]
					if !ok {
						continue // 热替换新增渠道的探针声明需重启生效(与余额源同口径)
					}
					ch := base
					if p.ProxyURL != "" {
						// 渠道声明出站代理:探针与推理同路出站(代理不可达计入探针失败,与数据面同口径)
						if cl, cerr := outboundClient(p.ProxyURL, outboundDial); cerr == nil {
							ch.Outbound = cl
						} else {
							logger.Warn("渠道出站代理解析失败,探针走默认出站", "provider", p.Name, "error", cerr.Error())
						}
					}
					for _, kv := range pools[p.Name].Snapshot() {
						ch.Keys = append(ch.Keys, health.ChannelKey{ID: kv.ID, Value: kv.Value})
					}
					out[p.Name] = ch
				}
				return out
			},
			Emit: func(e health.Event) {
				switch e.Kind {
				case "provider_degraded":
					alerts.EmitContext("provider_degraded:"+e.Provider, "warning", e.Summary,
						map[string]any{"provider": e.Provider, "reason": e.Reason})
				case "provider_recovered":
					alerts.EmitContext("provider_recovered:"+e.Provider, "info", e.Summary,
						map[string]any{"provider": e.Provider})
				case "switch_rate_exceeded":
					alerts.EmitContext("switch_rate_exceeded:"+e.Provider, "warning", e.Summary,
						map[string]any{"alias": e.Provider})
				}
			},
		})
		logger.Info("上游健康观测已启用", "free_probe", cfg.Health.FreeProbeEnabled == nil || *cfg.Health.FreeProbeEnabled)
	}
	chatProxy = proxy.New(proxy.Config{
		Registry:     registry,
		Providers:    cfg.Providers,
		Routes:       cfg.ModelRoutes,
		Pricing:      pricingDir,
		ImagePricing: imageDir,
		Settlement:   settlement,
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
		OAuth:            cfg.OAuth, // 实例 oauth 段:拓扑守门 auth 共享校验的完整性基准
		Plugins:          pluginReg,
		Alerts:           alerts,
		Health:           healthMgr,
		Logger:           logger,
	})
	outboundDial = cfg.Server.Timeouts.DialDuration() // 探针/余额出站代理与数据面建连超时同口径
	// 健康循环在 proxy 装配完成后启动:Sources 闭包读 chatProxy,先赋值再 Start
	// 保证 happens-before(探针首轮不会与装配写竞争,也不会打到装配前的 nil 拓扑)
	if healthMgr != nil {
		healthMgr.Start()
	}
	// oauth 刷新器同序启动(oauth 段未声明时零开销零 goroutine);flow 交管理面授权端点,
	// cancel 挂 cleanup 随退出停环。
	oauthFlow, oauthCancel := startOAuthRefresher(context.Background(), cfg, db, chatProxy, logger)

	// 控制面拓扑热替换：文件源为缺省，配置 topology_url 后启用。
	// 启动即拉，ETag 条件请求（304 跳过），失败或损坏保持当前快照并告警降级；
	// 池构建复用启动闭包 buildPool（env 解析与 adapter 注册），配置未变时 provider 运行态无损。
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
					InheritProxy: true, // 控制面载荷无 proxy_url 字段:沿用旧声明(评审 R1)
				}, buildPool)
				if rerr != nil {
					logger.Warn("拓扑替换被拒绝,保持当前快照", "error_detail", rerr.Error())
					return
				}
				etag = newETag
				logger.Info("控制面拓扑已热替换", "reused", stats.Reused,
					"created", stats.Created, "removed", stats.Removed)
			}
			syncOnce() // 启动即拉，不等首个周期
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for range ticker.C {
				syncOnce()
			}
		}()
	}

	// SIGHUP 热加载：重读配置，校验失败保持旧态并告警；成功则以 ReplaceTopology
	// 原子替换拓扑、池、适配器与能力目录，provider 未变时池运行态无损。
	// 只有拓扑域热更；auth/metrics/刷盘参数/管理面/静态价目/告警/日志与超时变更需重启。
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

	// 余额观测评估循环:渠道级余额源声明驱动,声明变更属非热更域(需重启生效)。
	// 凭证解析与查询器构造一次性完成于装配期(缺凭证启动即失败,与 key env 同纪律);
	// 运行期经拓扑快照动态映射 key(热替换感知),余额查询不触 Pick/Report。
	var balanceSvc *balance.Service
	if cfg.Balance.Enabled == nil || *cfg.Balance.Enabled {
		bindingBase := map[string]balance.ChannelBinding{}
		for _, p := range cfg.Providers {
			pb := p.Balance
			if pb == nil {
				continue
			}
			src := pb.Source
			if src == "" || src == balance.SourceNone {
				continue
			}
			binding := balance.ChannelBinding{Kind: src}
			if pb.Below != "" {
				v, perr := metering.ParsePrice(pb.Below)
				if perr != nil { // config 校验已保证,防御分支
					return nil, nil, nil, fmt.Errorf("渠道 %s balance.below 解析失败: %w", p.Name, perr)
				}
				binding.BelowCredits = &v
			}
			// 渠道声明出站代理:余额查询与推理/探针同路出站,统一经 outboundClient 缓存
			//(同 URL 单实例,连接池共用);解析失败启动即失败,不让渠道悄悄直连。
			var bcl *http.Client
			if p.ProxyURL != "" {
				cl, cerr := outboundClient(p.ProxyURL, outboundDial)
				if cerr != nil {
					return nil, nil, nil, fmt.Errorf("渠道 %s 出站代理解析失败: %w", p.Name, cerr)
				}
				bcl = cl
			}
			switch {
			case balance.IsBearer(src):
				// billing-compat 的币种不可自描述(new-api 随站点显示配置而变),经配置显式声明;
				// 其余 Bearer 源忽略该字段(config 校验已拒绝)。
				q, qerr := balance.ForWithClient(src, balance.SourceConfig{
					BaseURL:  p.BaseURL,
					Currency: strings.ToUpper(pb.Currency),
				}, bcl)
				if qerr != nil {
					return nil, nil, nil, fmt.Errorf("渠道 %s 余额源构造失败: %w", p.Name, qerr)
				}
				binding.Querier = q
			case src == balance.SourceManual:
				binding.Currency = strings.ToUpper(pb.Currency)
			default: // 云账务:凭证只经环境变量进内存,不入日志与配置态
				ak, sk := os.Getenv(pb.AccessKeyEnv), os.Getenv(pb.SecretKeyEnv)
				if ak == "" || sk == "" {
					return nil, nil, nil, fmt.Errorf("渠道 %s 的余额凭证环境变量未设置(%s/%s)", p.Name, pb.AccessKeyEnv, pb.SecretKeyEnv)
				}
				q, qerr := balance.ForWithClient(src, balance.SourceConfig{AccessKey: ak, SecretKey: sk}, bcl)
				if qerr != nil {
					return nil, nil, nil, fmt.Errorf("渠道 %s 余额源构造失败: %w", p.Name, qerr)
				}
				binding.Querier = q
			}
			bindingBase[p.Name] = binding
		}
		if len(bindingBase) > 0 {
			balanceSvc = balance.NewService(balance.Options{
				DB: db, Notifier: alerts, Logger: logger,
				// 逐 key 余额回写(credit 桶联动):欠费 → 池冷却让位(幂等),充值 → 提前释放;
				// metrics 双记,与 upstream 侧 cause 分列。
				OnKeyEvaluated: func(provider, keyID string, exhausted bool, now time.Time) {
					if exhausted {
						proxy.CreditEvents.Inc("balance", "enter")
					} else {
						proxy.CreditEvents.Inc("balance", "release")
					}
					if chatProxy == nil {
						return
					}
					_, pools := chatProxy.Topology()
					if pool := pools[provider]; pool != nil {
						pool.RecordCreditExhausted(keyID, exhausted, now)
					}
				},
				Interval:         time.Duration(cfg.Balance.EvalIntervalSec) * time.Second,
				PollTimeout:      time.Duration(cfg.Balance.PollTimeoutSec) * time.Second,
				WindowDays:       cfg.Balance.BurnWindowDays,
				MinSampleDays:    cfg.Balance.BurnMinDays,
				TierHours:        cfg.Balance.TierHours,
				RearmMilli:       cfg.Balance.RearmRatioMilli,
				FailureThreshold: cfg.Balance.FailureThreshold,
				SpikeRatioMilli:  *cfg.Balance.BurnSpikeRatioMilli,
				DropRatioMilli:   *cfg.Balance.DropRatioMilli,
				Settlement:       settlement,
				Sources: func() map[string]balance.ChannelBinding {
					providers, pools := chatProxy.Topology()
					out := make(map[string]balance.ChannelBinding, len(bindingBase))
					for _, p := range providers {
						base, ok := bindingBase[p.Name]
						if !ok {
							// 热替换新声明的余额源不热生效(查询器与凭证是装配期产物);
							// 未声明余额源的渠道属常态,静默跳过不产生日志噪音
							if pb := p.Balance; pb != nil && pb.Source != "" && pb.Source != balance.SourceNone {
								logger.Warn("渠道余额源声明需重启生效", "provider", p.Name)
							}
							continue
						}
						binding := base
						if balance.IsBearer(base.Kind) {
							for _, kv := range pools[p.Name].Snapshot() {
								binding.Keys = append(binding.Keys,
									balance.ChannelKey{ID: kv.ID, Value: kv.Value, Disabled: kv.Disabled})
							}
						} else {
							binding.Keys = []balance.ChannelKey{{ID: store.AccountKeyID}}
						}
						out[p.Name] = binding
					}
					return out
				},
			})
			balanceSvc.Start()
			logger.Info("余额观测已启用", "providers", len(bindingBase))
		}
	}

	// 业务路由中间件顺序敏感：request_id → recover → access_log → auth → quota → ratelimit。
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
	// 业务路由表：模式在此只写一次，注册循环与漂移门禁（assemble 返回的 declared）
	// 同源消费，多处手写清单必然失步。中间件分组：数据面全链（auth+配额+限流）、
	// 模型发现（仅鉴权）、探活（轻链）。
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
		{"POST /v1/images/generations", server.Chain(chatProxy.HandlerImages(), dataPlaneMW...)},
		{"POST /v1/embeddings", server.Chain(chatProxy.HandlerEmbeddings(), dataPlaneMW...)},
		{"POST /api/v1/services/embeddings/multimodal-embedding/multimodal-embedding", server.Chain(chatProxy.HandlerMultimodalEmbeddings(), dataPlaneMW...)},
		// Responses 入站：翻译后进同一管道，鉴权/配额/限流与 chat 同规。
		{"POST /v1/responses", server.Chain(chatProxy.HandlerResponses(), dataPlaneMW...)},
		// Anthropic Messages 入站：同款边缘翻译；鉴权头适配仅挂本路由，先于 verifier。
		{"POST /v1/messages", server.Chain(chatProxy.HandlerMessages(),
			server.RequestID, server.Recover(logger), server.AccessLog(logger),
			server.AdaptAnthropicKey, verifier.Middleware, gate.Middleware)},
		// Gemini 原生入站：同款边缘翻译；鉴权头与查询参数适配仅挂本路由，先于 verifier，
		// ?key= 读后剥离以防访问日志泄漏；{modelMethod} 整段通配，handler 内解析
		// {model}:{method} 后缀，mux 通配符不支持段内混合。
		{"POST /v1beta/models/{modelMethod}", server.Chain(chatProxy.HandlerGemini(),
			server.RequestID, server.Recover(logger), server.AccessLog(logger),
			server.AdaptGeminiKey, verifier.Middleware, gate.Middleware)},
		// 无状态边界：不存储 response，恒 404；挂鉴权链，不泄漏给未鉴权方。
		{"GET /v1/responses/{id}", server.Chain(chatProxy.HandlerResponsesStateless(), dataPlaneMW...)},
		{"DELETE /v1/responses/{id}", server.Chain(chatProxy.HandlerResponsesStateless(), dataPlaneMW...)},
		// 模型目录只读且无上游成本；保留鉴权，不走配额、限流、计量或插件。
		{"GET /v1/models", server.Chain(chatProxy.HandlerModels(), readonlyMW...)},
		{"GET /v1/models/{model}", server.Chain(chatProxy.HandlerModels(), readonlyMW...)},
		{"GET /healthz", server.Chain(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version.Version})
		}), server.RequestID, server.Recover(logger), server.AccessLog(logger))},
	}

	// 未配置管理 token 且没有启用管理员账号时关闭管理面。
	adminToken := os.Getenv("MUNIU_ADMIN_TOKEN")
	adminSvc, err := auth.NewAdminService(db)
	if err != nil {
		return nil, nil, nil, err
	}
	activeAdmins, err := store.HasActiveAdmins(db)
	if err != nil {
		return nil, nil, nil, err
	}
	if adminToken == "" && !activeAdmins {
		logger.Warn("管理令牌与活跃管理员均未配置,管理面与控制台关闭")
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
		DB: db, Token: adminToken, AdminAuth: adminSvc, Providers: cfg.Providers, Pools: pools,
		Verifier: verifier, Logger: logger, Balance: balanceSvc,
		AlertWebhookConfigured: cfg.Alerts.WebhookURL != "",
		Health:                 healthMgr,
		Topology:               chatProxy.Topology,
		PricingDir:             pricingDir,
		StaticImagePricing:     staticImages,
		UpdateImagePricing:     updateImages,
		StaticPricing:          staticHuman,
		RefreshPricing:         refreshPricing,
		Plugins:                pluginReg,
		OAuthFlow:              oauthFlow, // nil = oauth 段未声明,授权端点返回关闭态
	}
	if cacheInst != nil {
		adminDeps.CacheStats = cacheInst.Stats
	}
	adminHandler := admin.New(adminDeps)

	// 探活已入业务路由表，轻链不鉴权。

	// 可观测：启用且管理 token 已配置时暴露 /metrics；token 门禁恒时比较防时序侧信道，
	// Recover 防仪表函数 panic 裸断连接。无 token 不挂载。
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
			_, currentPools := chatProxy.Topology()
			for name, pool := range currentPools {
				for state, n := range pool.StateCounts(now) {
					out = append(out, metrics.Sample{Labels: [][2]string{{"provider", name}, {"state", state}}, Value: float64(n)})
				}
			}
			return out
		})
	}

	// 注册：模式只在路由表写一次；/admin/v1/ 为子树挂载，模式集由 admin.Routes() 回报，
	// /console/ 静态资源不计入 declared 模式集。
	root := http.NewServeMux()
	for _, e := range entries {
		root.Handle(e.Pattern, e.Handler)
	}
	declared := make([]string, 0, len(entries)+len(admin.Routes())+1)
	for _, e := range entries {
		declared = append(declared, e.Pattern)
	}
	if adminHandler != nil {
		root.Handle("/admin/v1/", server.Chain(adminHandler, server.RequestID, server.Recover(logger)))
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

	// 控制面价目拉取：响应形态与管理 API 一致，覆盖写入后刷新目录。
	// 全量期望语义仅 upsert，撤销覆盖仍走管理面 DELETE；失败保持原目录，下轮重试。
	if cfg.ControlPlane.PricingURL != "" {
		interval := time.Duration(cfg.ControlPlane.PollIntervalSec) * time.Second
		if interval <= 0 {
			interval = 60 * time.Second
		}
		go func() {
			client := &http.Client{Timeout: 10 * time.Second}
			pendingRefresh := false // 上轮刷新失败的待重试标记，比对跳过不得短路刷新
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
					// 内容未变化则跳过，避免 updated_at 失去最后真实变更语义并产生无谓写放大
					if existing, gerr := store.GetPricing(db, rec.Alias); gerr == nil &&
						existing.Currency == rec.Currency && existing.InputCredits == rec.InputCredits &&
						existing.CachedInputCredits == rec.CachedInputCredits && existing.VisionInputCredits == rec.VisionInputCredits && existing.OutputCredits == rec.OutputCredits &&
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
			syncOnce() // 启动即拉，不等首个周期
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for range ticker.C {
				syncOnce()
			}
		}()
	}

	// 嵌入通道绑定：HTTP 链就绪后进程内自调用 /v1/embeddings。
	if cacheInst != nil {
		bindEmbedder(late, root, embedKey)
	}

	cleanup := func() {
		late.set(func(context.Context, string, string) ([]float32, error) {
			return nil, errors.New("已关闭")
		})
		if pluginReg != nil {
			pluginReg.Close() // 等待在途插件调用完成后关停，与热替换同一语义
		}
		if balanceSvc != nil {
			balanceSvc.Close() // 取消在途余额查询,状态已随评估落库
		}
		if healthMgr != nil {
			healthMgr.Close() // 停探针循环,在途探针随 ctx 取消(与余额观测启用与否无关)
		}
		if oauthCancel != nil {
			oauthCancel() // 停 oauth 刷新循环
		}
		verifier.Close()
		_ = pipeline.Close() // 退出前排干计量存量
	}
	return root, declared, cleanup, nil
}

// outboundClientCache 渠道出站代理 client 缓存:按声明 URL 缓存独立 client,
// 探针(动态拓扑)与余额查询(装配期)共用;缓存键=声明的 proxy_url,数量受配置面约束。
var outboundClientCache sync.Map

// outboundDial 探针/余额出站 client 的拨号超时,与数据面同一口径(装配期一次定型)。
var outboundDial time.Duration

// outboundClient 解析渠道出站代理 client(探针 Sources 闭包动态消费);解析失败返回错误
// 由调用方按场景降级(探针回默认 client 并告警,余额装配启动即失败)。
func outboundClient(raw string, dial time.Duration) (*http.Client, error) {
	if v, ok := outboundClientCache.Load(raw); ok {
		return v.(*http.Client), nil
	}
	cl, err := proxy.ProxiedClient(raw, dial)
	if err != nil {
		return nil, err
	}
	outboundClientCache.Store(raw, cl)
	return cl, nil
}

// cooldownSink keypool 冷却持久化出口(store 实现):欠费桶全程/rate 长冷却的迁移落库。
// 落库失败留 Warn 痕(契约「失败由实现方留痕」):持久化静默全灭的唯一症状是重启丢冷却,运维需有线索。
type cooldownSink struct {
	db     *sql.DB
	logger *slog.Logger
}

func (s cooldownSink) SaveCooldown(provider, keyID, bucket string, until time.Time, consec int, seq int64) error {
	if err := store.SaveKeyCooldown(s.db, provider, keyID, bucket, until, consec, time.Now(), seq); err != nil {
		s.logger.Warn("冷却落库失败", "provider", provider, "key_id", keyID, "error_detail", err.Error())
	}
	return nil
}

// cooldownRecords store 行 → keypool 恢复记录(存储与池互不依赖,装配层换形)。
func cooldownRecords(rows []store.KeyCooldownRow) []keypool.CooldownRecord {
	out := make([]keypool.CooldownRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, keypool.CooldownRecord{KeyID: r.KeyID, Bucket: r.Bucket, Until: r.Until, Consec: r.Consec})
	}
	return out
}

// keyIDSet 键集合(set 语义供对账)。
func keyIDSet(ids []string) map[string]bool {
	m := make(map[string]bool, len(ids))
	for _, id := range ids {
		m[id] = true
	}
	return m
}
