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
	"github.com/solvela/muniu/internal/oauth"
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
	deps, err := newRuntimeDeps(cfg, logger)
	if err != nil {
		return nil, nil, nil, err
	}
	db, alerts, pipeline := deps.db, deps.alerts, deps.pipeline
	tpm, tpmRecorder, sharedBackend, buildPool, pools := deps.tpm, deps.tpmRecorder,
		deps.sharedBackend, deps.buildPool, deps.pools

	pricing, err := newPricingAssembly(cfg, db)
	if err != nil {
		return nil, nil, nil, err
	}
	settlement, rebuildPricing, pricingDir := pricing.settlement, pricing.rebuild, pricing.dir
	imageDir, staticImages, updateImages := pricing.imageDir, pricing.staticImages, pricing.updateImages
	// 静态价目读侧（GET /admin/v1/pricing 的 yaml 来源行）如实透出窗口与时区，
	// 防止控制台编辑 yaml 行时静默丢窗。
	staticHuman := staticPricingView(pricing.staticDefs)

	// 语义缓存由租户开关控制，嵌入请求经进程内 HTTP 路由发送。
	cacheInst, embedKey, late, err := newSemanticCache(cfg, db, logger)
	if err != nil {
		return nil, nil, nil, err
	}
	tenantSw := tenantSwitch(db)

	// WASM 插件注册表：YAML 静态声明注册，运行态增改走管理面，重启回落本声明。
	pluginReg, err := newPluginRegistry(cfg, logger)
	if err != nil {
		return nil, nil, nil, err
	}

	// 上游健康观测与熔断:免费控制面探针(缺省开,渠道可退订,适配器能力闸)+ 付费推理探针
	// (probe_model 声明制)+ 被动统计;降级渠道由 proxy 候选域收缩自动降级路由。
	chatProxy, healthMgr, oauthFlow, oauthCancel := newProxyRuntime(proxyAssembly{
		cfg: cfg, cfgPath: cfgPath, logger: logger, db: db, alerts: alerts, pipeline: pipeline,
		registry: deps.registry, pools: pools, buildPool: buildPool,
		pricingDir: pricingDir, imageDir: imageDir, settlement: settlement,
		cacheInst: cacheInst, tenantSw: tenantSw, pluginReg: pluginReg, tpmRecorder: tpmRecorder,
	})

	// 余额观测评估循环:渠道级余额源声明驱动,声明变更属非热更域(需重启生效)。
	balanceSvc, err := newBalanceService(cfg, db, alerts, logger, func() *proxy.Proxy { return chatProxy }, settlement)
	if err != nil {
		return nil, nil, nil, err
	}

	verifier := newVerifier(cfg, db, logger)
	gate := newGate(tpm, db, logger, sharedBackend)
	entries := buildRoutes(chatProxy, verifier, gate, logger)

	// 未配置管理 token 且没有启用管理员账号时关闭管理面。
	adminToken, adminSvc, err := newAdminContext(db, logger)
	if err != nil {
		return nil, nil, nil, err
	}
	refreshPricing := func() error { return refreshPricingDirectory(rebuildPricing, pricingDir) }
	adminDeps := buildAdminDeps(adminWiring{
		cfg: cfg, db: db, token: adminToken, adminSvc: adminSvc, pools: pools,
		verifier: verifier, logger: logger, balanceSvc: balanceSvc, healthMgr: healthMgr,
		px: chatProxy, pricingDir: pricingDir, staticImages: staticImages, updateImages: updateImages,
		staticHuman: staticHuman, refreshPricing: refreshPricing, pluginReg: pluginReg, oauthFlow: oauthFlow,
	})
	adminHandler := newAdminHandler(adminDeps, cacheInst)

	root, declared, cleanup := finishAssembly(cfg, logger, db, pipeline, chatProxy, entries,
		adminHandler, adminToken, refreshPricing, cacheInst, late, embedKey,
		pluginReg, balanceSvc, healthMgr, oauthCancel, verifier)
	return root, declared, cleanup, nil
}

// adminWiring 管理面依赖装配输入。
type adminWiring struct {
	cfg            *config.Config
	db             *sql.DB
	token          string
	adminSvc       *auth.AdminService
	pools          map[string]*keypool.Pool
	verifier       *auth.Verifier
	logger         *slog.Logger
	balanceSvc     *balance.Service
	healthMgr      *health.Manager
	px             *proxy.Proxy
	pricingDir     *metering.PricingDirectory
	staticImages   map[string]metering.ImagePrice
	updateImages   func(string, *store.ImagePricingRecord) error
	staticHuman    map[string]admin.StaticEntry
	refreshPricing func() error
	pluginReg      *plugin.Registry
	oauthFlow      *oauth.Flow
}

// buildAdminDeps 组装管理面依赖。
func buildAdminDeps(w adminWiring) admin.Deps {
	return admin.Deps{
		DB: w.db, Token: w.token, AdminAuth: w.adminSvc, Providers: w.cfg.Providers, Pools: w.pools,
		Verifier: w.verifier, Logger: w.logger, Balance: w.balanceSvc,
		AlertWebhookConfigured: w.cfg.Alerts.WebhookURL != "",
		Health:                 w.healthMgr,
		Topology:               w.px.Topology,
		PricingDir:             w.pricingDir,
		StaticImagePricing:     w.staticImages,
		UpdateImagePricing:     w.updateImages,
		StaticPricing:          w.staticHuman,
		RefreshPricing:         w.refreshPricing,
		Plugins:                w.pluginReg,
		OAuthFlow:              w.oauthFlow, // nil = oauth 段未声明,授权端点返回关闭态
	}
}

// finishAssembly 完成可观测挂载、价目轮询、嵌入绑定与退出清理。
func finishAssembly(cfg *config.Config, logger *slog.Logger, db *sql.DB, pipeline *metering.Pipeline,
	px *proxy.Proxy, entries []routeEntry, adminHandler http.Handler, adminToken string,
	refreshPricing func() error, cacheInst *semantic.Cache, late *lateEmbedder, embedKey string,
	pluginReg *plugin.Registry, balanceSvc *balance.Service, healthMgr *health.Manager,
	oauthCancel func(), verifier *auth.Verifier) (http.Handler, []string, func()) {
	// 可观测：启用且管理 token 已配置时暴露 /metrics；token 门禁恒时比较防时序侧信道，
	// Recover 防仪表函数 panic 裸断连接。无 token 不挂载。
	metricsEnabled := newMetrics(cfg, adminToken, db, pipeline, px, logger)
	root, declared := mountRoutes(entries, adminHandler, metricsEnabled, adminToken, logger)
	// 控制面价目拉取：响应形态与管理 API 一致，覆盖写入后刷新目录。
	// 全量期望语义仅 upsert，撤销覆盖仍走管理面 DELETE；失败保持原目录，下轮重试。
	startPricingPoll(cfg, logger, db, refreshPricing)
	// 嵌入通道绑定：HTTP 链就绪后进程内自调用 /v1/embeddings。
	if cacheInst != nil {
		bindEmbedder(late, root, embedKey)
	}
	return root, declared, newCleanup(late, pluginReg, balanceSvc, healthMgr, oauthCancel, verifier, pipeline)
}

// refreshPricingDirectory 重建价目表并原子替换运行目录。
func refreshPricingDirectory(
	rebuild func() (map[string]*metering.PricingTable, error), dir *metering.PricingDirectory,
) error {
	tables, err := rebuild()
	if err != nil {
		return err
	}
	dir.Replace(tables)
	return nil
}

// newAdminHandler 构造管理面 handler；缓存统计仅在启用时挂载。
func newAdminHandler(deps admin.Deps, cache *semantic.Cache) http.Handler {
	if cache != nil {
		deps.CacheStats = cache.Stats
	}
	return admin.New(deps)
}

// newAdminContext 读取管理 token 并构造管理员服务；两者皆缺时管理面关闭。
func newAdminContext(db *sql.DB, logger *slog.Logger) (string, *auth.AdminService, error) {
	token := os.Getenv("MUNIU_ADMIN_TOKEN")
	svc, err := auth.NewAdminService(db)
	if err != nil {
		return "", nil, err
	}
	active, err := store.HasActiveAdmins(db)
	if err != nil {
		return "", nil, err
	}
	if token == "" && !active {
		logger.Warn("管理令牌与活跃管理员均未配置,管理面与控制台关闭")
	}
	return token, svc, nil
}

// startHealth 启用时启动探针循环。
func startHealth(mgr *health.Manager) {
	if mgr != nil {
		mgr.Start()
	}
}

// mountRoutes 注册业务路由、管理面与控制台、/metrics。
// 模式只在路由表写一次；/admin/v1/ 为子树挂载，模式集由 admin.Routes() 回报，
// /console/ 静态资源不计入 declared 模式集。
func mountRoutes(
	entries []routeEntry, adminHandler http.Handler, metricsEnabled bool,
	adminToken string, logger *slog.Logger,
) (http.Handler, []string) {
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
		root.Handle("GET /metrics", server.Recover(logger)(metricsHandler(adminToken)))
		declared = append(declared, "GET /metrics")
	}
	return root, declared
}

// metricsHandler 管理 token 恒时比较门禁。
func metricsHandler(adminToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	})
}

// newCleanup 退出前排干计量存量并停环；os.Exit 不执行 defer，故显式返回闭包。
func newCleanup(late *lateEmbedder, plugins *plugin.Registry, balanceSvc *balance.Service,
	healthMgr *health.Manager, oauthCancel func(), verifier *auth.Verifier, pipeline *metering.Pipeline) func() {
	return func() {
		late.set(func(context.Context, string, string) ([]float32, error) {
			return nil, errors.New("已关闭")
		})
		if plugins != nil {
			plugins.Close() // 等待在途插件调用完成后关停，与热替换同一语义
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

// runtimeDeps 运行态基础件：存储、告警、计量、限流后端与池。
type runtimeDeps struct {
	db            *sql.DB
	alerts        *alerting.Notifier
	pipeline      *metering.Pipeline
	tpm           *ratelimit.TPM
	tpmRecorder   ratelimit.TPMRecorder
	sharedBackend ratelimit.SharedBackend
	registry      *adapter.Registry
	buildPool     func(config.Provider) (*keypool.Pool, adapter.Adapter, error)
	pools         map[string]*keypool.Pool
}

// newRuntimeDeps 打开存储并装配基础件；buildPool 同时用于启动装配和拓扑热替换。
func newRuntimeDeps(cfg *config.Config, logger *slog.Logger) (runtimeDeps, error) {
	// SQLite 运行态库（api_keys/usage_logs），迁移随打开执行。
	db, err := store.Open(cfg.Storage.SQLite.Path)
	if err != nil {
		return runtimeDeps{}, fmt.Errorf("打开存储: %w", err)
	}
	alerts := newAlerting(cfg, db, logger)
	pipeline := newMeteringPipeline(cfg, db, alerts, logger)
	tpm, recorder, shared, err := newRateLimit(cfg, logger)
	if err != nil {
		return runtimeDeps{}, err
	}
	registry := adapter.NewRegistry()
	buildPool := newPoolBuilder(db, registry, logger)
	pools, err := buildAllPools(cfg.Providers, buildPool)
	if err != nil {
		return runtimeDeps{}, err
	}
	return runtimeDeps{
		db: db, alerts: alerts, pipeline: pipeline,
		tpm: tpm, tpmRecorder: recorder, sharedBackend: shared,
		registry: registry, buildPool: buildPool, pools: pools,
	}, nil
}

// proxyAssembly proxy 及其后台循环的装配输入。
type proxyAssembly struct {
	cfg         *config.Config
	cfgPath     string
	logger      *slog.Logger
	db          *sql.DB
	alerts      *alerting.Notifier
	pipeline    *metering.Pipeline
	registry    *adapter.Registry
	pools       map[string]*keypool.Pool
	buildPool   func(config.Provider) (*keypool.Pool, adapter.Adapter, error)
	pricingDir  *metering.PricingDirectory
	imageDir    *metering.ImagePriceDirectory
	settlement  string
	cacheInst   *semantic.Cache
	tenantSw    func(string) bool
	pluginReg   *plugin.Registry
	tpmRecorder ratelimit.TPMRecorder
}

// newProxyRuntime 装配 proxy 并按序启动健康/oauth/拓扑/SIGHUP 循环。
//
// 健康循环在 proxy 装配完成后启动：Sources 闭包读 proxy，先赋值再 Start 保证
// happens-before（探针首轮不会与装配写竞争，也不会打到装配前的 nil 拓扑）。
func newProxyRuntime(a proxyAssembly) (*proxy.Proxy, *health.Manager, *oauth.Flow, func()) {
	var px *proxy.Proxy
	healthMgr := newHealthManager(a.cfg, a.alerts, a.logger, func() *proxy.Proxy { return px })
	px = proxy.New(proxy.Config{
		Registry:     a.registry,
		Providers:    a.cfg.Providers,
		Routes:       a.cfg.ModelRoutes,
		Pricing:      a.pricingDir,
		ImagePricing: a.imageDir,
		Settlement:   a.settlement,
		Timeouts: proxy.Timeouts{
			Dial:       a.cfg.Server.Timeouts.DialDuration(),
			FirstToken: a.cfg.Server.Timeouts.FirstTokenDuration(),
			ChunkIdle:  a.cfg.Server.Timeouts.ChunkIdleDuration(),
		},
		Pipeline:         a.pipeline,
		Pools:            a.pools,
		TPM:              a.tpmRecorder,
		Cache:            a.cacheInst,
		TenantSwitch:     a.tenantSw,
		Capabilities:     a.cfg.Models,
		StrictGovernance: a.cfg.StrictGovernance,
		OAuth:            a.cfg.OAuth, // 实例 oauth 段:拓扑守门 auth 共享校验的完整性基准
		Plugins:          a.pluginReg,
		Alerts:           a.alerts,
		Health:           healthMgr,
		Logger:           a.logger,
	})
	outboundDial = a.cfg.Server.Timeouts.DialDuration() // 探针/余额出站代理与数据面建连超时同口径
	startHealth(healthMgr)
	// oauth 刷新器同序启动(oauth 段未声明时零开销零 goroutine);flow 交管理面授权端点,
	// cancel 挂 cleanup 随退出停环。
	oauthFlow, oauthCancel := startOAuthRefresher(context.Background(), a.cfg, a.db, px, a.logger)
	startTopologyPoll(a.cfg, a.logger, px, a.buildPool)
	startSighupReload(a.cfgPath, a.logger, px, a.buildPool)
	return px, healthMgr, oauthFlow, oauthCancel
}

// newAlerting 构造告警通知器：未配置 webhook_url 时为 nil，各接入点 Emit 零开销。
// 投递终局回调落库自监控:通道挂了不能再用通道自报(递归陷阱),失败事实只落库走管理面可见。
func newAlerting(cfg *config.Config, db *sql.DB, logger *slog.Logger) *alerting.Notifier {
	return alerting.New(alerting.Options{
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
}

// newMeteringPipeline 接线刷盘参数（缺省值已在 config.validate 兜底）。
// sink=ingest 时为远端汇加本地缓冲：投递失败整批落缓冲，恢复后按序续传。
func newMeteringPipeline(
	cfg *config.Config, db *sql.DB, alerts *alerting.Notifier, logger *slog.Logger,
) *metering.Pipeline {
	opts := metering.Options{
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
		opts.Remote = &metering.IngestSink{URL: cfg.Metering.IngestURL, Token: cfg.Metering.IngestToken}
		opts.Buffer = metering.NewBufferStore(db, bufferCap)
	}
	return metering.NewPipeline(db, opts, logger)
}

// newRateLimit 选择限流计数后端：inmem 缺省（实例自治，零变更）；redis 为集群共享视图。
func newRateLimit(cfg *config.Config, logger *slog.Logger) (
	*ratelimit.TPM, ratelimit.TPMRecorder, ratelimit.SharedBackend, error,
) {
	tpm := ratelimit.NewTPM()
	var sharedBackend ratelimit.SharedBackend
	var recorder ratelimit.TPMRecorder = tpm
	switch cfg.RateLimit.Backend {
	case "", "inmem":
	case "redis":
		if cfg.RateLimit.RedisAddr == "" {
			return nil, nil, nil, errors.New("ratelimit.backend=redis 需要配置 ratelimit.redis_addr")
		}
		shared := ratelimit.NewRedisBackend(cfg.RateLimit.RedisAddr, logger)
		sharedBackend = shared
		recorder = shared
		logger.Info("限流共享视图启用(redis)", "addr", cfg.RateLimit.RedisAddr)
	default:
		return nil, nil, nil, fmt.Errorf("ratelimit.backend 须为 inmem|redis,got %s", cfg.RateLimit.Backend)
	}
	return tpm, recorder, sharedBackend, nil
}

// newPoolBuilder 构造池装配闭包：启动装配与拓扑热替换共用同一份解析/注册逻辑。
func newPoolBuilder(
	db *sql.DB, registry *adapter.Registry, logger *slog.Logger,
) func(config.Provider) (*keypool.Pool, adapter.Adapter, error) {
	return func(prov config.Provider) (*keypool.Pool, adapter.Adapter, error) {
		inputs, first, err := resolveKeyInputs(db, prov)
		if err != nil {
			return nil, nil, err
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
}

// resolveKeyInputs 解析渠道 keys 为池输入，并返回首个凭据供适配器构造。
func resolveKeyInputs(db *sql.DB, prov config.Provider) ([]keypool.KeyInput, string, error) {
	if len(prov.Keys) == 0 {
		return nil, "", fmt.Errorf("provider %s 无可用 key 引用", prov.Name)
	}
	inputs := make([]keypool.KeyInput, 0, len(prov.Keys))
	first := ""
	for i, kr := range prov.Keys {
		v, err := resolveKeyValue(db, prov.Name, kr)
		if err != nil {
			return nil, "", err
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
	return inputs, first, nil
}

// resolveKeyValue 解析单个 key 引用：账号引用从存储取当前凭据，否则读环境变量。
//
// 账号引用：装配层唯一解析点（oauth 包不依赖 store）；不存在即装配失败——文件源启动
// fail-fast，控制面/热替换路径快照被拒（旧快照保留）。
func resolveKeyValue(db *sql.DB, providerName string, kr config.KeyRef) (string, error) {
	if kr.Account != "" {
		row, err := store.GetOAuthAccount(db, kr.Account)
		if err != nil {
			return "", fmt.Errorf("provider %s 的账号引用 %s 不可用: %w", providerName, kr.Account, err)
		}
		if row.AccessToken == "" {
			return "", fmt.Errorf("provider %s 的账号引用 %s 凭据为空", providerName, kr.Account)
		}
		return row.AccessToken, nil
	}
	v := os.Getenv(kr.Env)
	if v == "" {
		return "", fmt.Errorf("provider %s 的 key 环境变量 %s 未设置", providerName, kr.Env)
	}
	return v, nil
}

// buildAllPools 按渠道构建初始池集。
func buildAllPools(
	providers []config.Provider,
	build func(config.Provider) (*keypool.Pool, adapter.Adapter, error),
) (map[string]*keypool.Pool, error) {
	pools := make(map[string]*keypool.Pool, len(providers))
	for _, p := range providers {
		pool, _, err := build(p)
		if err != nil {
			return nil, err
		}
		pools[p.Name] = pool
	}
	return pools, nil
}

// pricingAssembly 价目装配产物：静态定义、重建闭包、运行目录与静态读侧视图。
type pricingAssembly struct {
	settlement   string
	fxMilli      int64
	staticDefs   map[string]staticPricing
	rebuild      func() (map[string]*metering.PricingTable, error)
	dir          *metering.PricingDirectory
	imageDir     *metering.ImagePriceDirectory
	staticImages map[string]metering.ImagePrice
	updateImages func(string, *store.ImagePricingRecord) error
}

// newPricingAssembly 在加载期把 YAML 价目一次性换算为 credit 整数，全程零浮点；
// 分时段倍率窗同样预先算出各窗有效三段价。
// credit 锚定本位币（缺省 CNY），异币种报价按 fx_usd_cny_milli 在此一次性换算，热路径只查表。
// YAML 为静态兜底，model_pricing 表为运营覆盖；本位币空值在此补 CNY（validate 只校验不写回）。
func newPricingAssembly(cfg *config.Config, db *sql.DB) (pricingAssembly, error) {
	settlement := strings.ToUpper(cfg.Metering.Settlement)
	if settlement == "" {
		settlement = metering.SettlementCNY
	}
	fx := cfg.Metering.FXUSDCNYMilli
	staticDefs := make(map[string]staticPricing, len(cfg.Pricing))
	for alias, pr := range cfg.Pricing {
		sp, err := parseStaticPricing(alias, pr)
		if err != nil {
			return pricingAssembly{}, err
		}
		staticDefs[alias] = sp
	}
	rebuild := func() (map[string]*metering.PricingTable, error) {
		return buildPricingTables(db, staticDefs, settlement, fx)
	}
	tables, err := rebuild()
	if err != nil {
		return pricingAssembly{}, err
	}
	imageDir, staticImages, updateImages, err := assembleImagePricing(db, cfg, settlement, fx)
	if err != nil {
		return pricingAssembly{}, err
	}
	return pricingAssembly{
		settlement:   settlement,
		fxMilli:      fx,
		staticDefs:   staticDefs,
		rebuild:      rebuild,
		dir:          metering.NewPricingDirectory(tables),
		imageDir:     imageDir,
		staticImages: staticImages,
		updateImages: updateImages,
	}, nil
}

// parseStaticPricing 解析单条 YAML 价目：三段价、时区与倍率窗。
func parseStaticPricing(alias string, pr config.Price) (staticPricing, error) {
	inMicro, err := metering.ParsePrice(pr.Input)
	if err != nil {
		return staticPricing{}, fmt.Errorf("pricing[%s].input: %w", alias, err)
	}
	outMicro, err := metering.ParsePrice(pr.Output)
	if err != nil {
		return staticPricing{}, fmt.Errorf("pricing[%s].output: %w", alias, err)
	}
	cachedMicro, err := parseOptionalPrice(alias, "cached_input", pr.CachedInput)
	if err != nil {
		return staticPricing{}, err
	}
	visionMicro, err := parseOptionalPrice(alias, "vision_input", pr.VisionInput)
	if err != nil {
		return staticPricing{}, err
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
			return staticPricing{}, fmt.Errorf("pricing[%s].timezone: %w", alias, err)
		}
	}
	specs, err := parseTimeWindows(alias, pr.TimeWindows)
	if err != nil {
		return staticPricing{}, err
	}
	return staticPricing{def: def, zone: zone, specs: specs}, nil
}

// parseOptionalPrice 解析可选价目字段：空串表示走既有的缺省回落语义。
func parseOptionalPrice(alias, field, raw string) (int64, error) {
	if raw == "" {
		return 0, nil
	}
	v, err := metering.ParsePrice(raw)
	if err != nil {
		return 0, fmt.Errorf("pricing[%s].%s: %w", alias, field, err)
	}
	return v, nil
}

// parseTimeWindows 解析分时段倍率窗。
func parseTimeWindows(alias string, windows []config.PriceWindow) ([]metering.WindowSpec, error) {
	specs := make([]metering.WindowSpec, 0, len(windows))
	for _, tw := range windows {
		s, e, err := metering.ParseWindowMinutes(tw.Window)
		if err != nil {
			return nil, fmt.Errorf("pricing[%s]: %w", alias, err)
		}
		bp, err := metering.ParseMultiplier(tw.Multiplier)
		if err != nil {
			return nil, fmt.Errorf("pricing[%s]: %w", alias, err)
		}
		specs = append(specs, metering.WindowSpec{StartMin: s, EndMin: e, BasisPoints: bp})
	}
	return specs, nil
}

// staticPricingView 生成管理面读侧的 yaml 来源行视图。
func staticPricingView(defs map[string]staticPricing) map[string]admin.StaticEntry {
	out := make(map[string]admin.StaticEntry, len(defs))
	for alias, sp := range defs {
		wins := make([]admin.PricingWindowView, 0, len(sp.specs))
		for _, spec := range sp.specs {
			wins = append(wins, admin.PricingWindowView{
				Window:     formatClockMinutes(spec.StartMin) + "-" + formatClockMinutes(spec.EndMin),
				Multiplier: metering.FormatMultiplier(spec.BasisPoints),
			})
		}
		out[alias] = admin.StaticEntry{Def: sp.def, Timezone: sp.zone.String(), TimeWindows: wins}
	}
	return out
}

// newSemanticCache 构造语义缓存；未启用时返回零值（late 仍可用，cleanup 会关闭它）。
func newSemanticCache(
	cfg *config.Config, db *sql.DB, logger *slog.Logger,
) (*semantic.Cache, string, *lateEmbedder, error) {
	late := &lateEmbedder{}
	if !cfg.Semantic.Enabled {
		return nil, "", late, nil
	}
	cache, err := semantic.NewCache(db, semantic.Options{
		Dim: cfg.Semantic.Dim, Threshold: cfg.Semantic.Threshold,
		TTL: time.Duration(cfg.Semantic.TTLHours) * time.Hour, Capacity: cfg.Semantic.Capacity,
		EmbeddingModel: cfg.Semantic.EmbeddingModel,
	}, late)
	if err != nil {
		return nil, "", nil, fmt.Errorf("初始化语义缓存: %w", err)
	}
	embedKey := ensureInternalEmbedKey(db, logger)
	if embedKey == "" {
		return nil, "", nil, errors.New("内部嵌入 key 签发失败")
	}
	return cache, embedKey, late, nil
}

// tenantSwitch 租户语义缓存开关（租户行缺失或查询失败按关闭处理）。
func tenantSwitch(db *sql.DB) func(string) bool {
	return func(tenantID string) bool {
		var en int64
		if err := db.QueryRow(`SELECT semantic_cache_enabled FROM tenants WHERE id = ?`, tenantID).Scan(&en); err != nil {
			return false
		}
		return en == 1
	}
}

// newPluginRegistry 注册 YAML 静态声明的 WASM 插件。
// 注册即校验：wasm magic → 编译 → 探测实例 → ABI 版本 → plugin_start，任一失败启动即断。
func newPluginRegistry(cfg *config.Config, logger *slog.Logger) (*plugin.Registry, error) {
	// 恒建：管理面运行态注册端点依赖，空表快照零堆分配
	reg := plugin.NewRegistry()
	for _, pc := range cfg.Plugins {
		if err := registerPlugin(reg, pc, logger); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

// registerPlugin 读取插件二进制并登记。
func registerPlugin(reg *plugin.Registry, pc config.PluginCfg, logger *slog.Logger) error {
	bin, err := os.ReadFile(pc.Path)
	if err != nil {
		return fmt.Errorf("插件 %s 读取失败: %w", pc.Name, err)
	}
	mib := pc.MemoryLimitMiB
	if mib == 0 {
		mib = 64 // 与 plugin.ResourceDefaults 对齐，config 层保持零值语义
	}
	if _, _, err := reg.Register(plugin.Options{
		Name:             pc.Name,
		Binary:           bin,
		Config:           pc.Config,
		Enabled:          pc.Enabled != nil && *pc.Enabled,
		FailOpen:         pc.FailOpen,
		TimeoutMs:        pc.TimeoutMs,
		MemoryLimitPages: uint32(mib) * 16, // 64KiB/页
		PoolSize:         pc.PoolSize,
		Logger:           logger,
	}); err != nil {
		return fmt.Errorf("插件 %s 注册失败: %w", pc.Name, err)
	}
	logger.Info("WASM 插件已注册", "plugin", pc.Name, "fail_open", pc.FailOpen)
	return nil
}

// newHealthManager 构造上游健康观测；显式关闭时返回 nil。
func newHealthManager(
	cfg *config.Config, alerts *alerting.Notifier, logger *slog.Logger,
	proxyRef func() *proxy.Proxy,
) *health.Manager {
	if cfg.Health.Enabled != nil && !*cfg.Health.Enabled {
		return nil
	}
	base := healthChannelBase(cfg)
	mgr := health.NewManager(health.Options{
		Interval:         time.Duration(cfg.Health.ProbeIntervalSec) * time.Second,
		Timeout:          time.Duration(cfg.Health.ProbeTimeoutSec) * time.Second,
		FailureThreshold: cfg.Health.FailureThreshold,
		OpenFor:          time.Duration(cfg.Health.OpenSeconds) * time.Second,
		SwitchWindow:     time.Duration(cfg.Health.SwitchWindowSec) * time.Second,
		SwitchMinSamples: cfg.Health.SwitchMinSamples,
		SwitchRateMilli:  cfg.Health.SwitchRateMilli,
		Logger:           logger,
		Sources:          healthSources(base, proxyRef, logger),
		Emit:             healthEmitter(alerts),
	})
	logger.Info("上游健康观测已启用", "free_probe", cfg.Health.FreeProbeEnabled == nil || *cfg.Health.FreeProbeEnabled)
	return mgr
}

// healthChannelBase 按 YAML 声明构造渠道探测基线（全渠道入 Sources）。
//
// 无 key 渠道零探针载体，但被动统计与状态推进（open 计时到期转半开）依赖周期 tick
// 触达——排除在外会让被动降级永不进入试探期，即永久熔断。
func healthChannelBase(cfg *config.Config) map[string]health.Channel {
	base := make(map[string]health.Channel, len(cfg.Providers))
	for _, p := range cfg.Providers {
		ch := health.Channel{Name: p.Name, Adapter: p.Adapter, BaseURL: p.BaseURL, Auth: p.Auth}
		ch.FreeProbe = freeProbeEnabled(cfg, p) && health.ProbeSupported(p.Adapter) && len(p.Keys) > 0
		if p.Health != nil && p.Health.ProbeModel != "" && len(p.Keys) > 0 && health.ProbeSupported(p.Adapter) {
			ch.ProbeModel = p.Health.ProbeModel
		}
		base[p.Name] = ch
	}
	return base
}

// freeProbeEnabled 渠道级退订优先于全局缺省。
func freeProbeEnabled(cfg *config.Config, p config.Provider) bool {
	free := cfg.Health.FreeProbeEnabled == nil || *cfg.Health.FreeProbeEnabled
	if p.Health != nil && p.Health.FreeProbe != nil {
		free = *p.Health.FreeProbe
	}
	return free
}

// healthSources 运行期按拓扑快照映射探针目标；首轮早于 proxy 装配完成时返回空集。
func healthSources(
	base map[string]health.Channel, proxyRef func() *proxy.Proxy, logger *slog.Logger,
) func() map[string]health.Channel {
	return func() map[string]health.Channel {
		px := proxyRef()
		if px == nil {
			return nil // 首轮探针早于 proxy 装配完成:空集跳过,下一轮拓扑已就绪
		}
		providers, pools := px.Topology()
		out := make(map[string]health.Channel, len(base))
		for _, p := range providers {
			ch, ok := healthChannelSnapshot(base, p, pools[p.Name].Snapshot(), logger)
			if ok {
				out[p.Name] = ch
			}
		}
		return out
	}
}

// healthChannelSnapshot 生成单渠道探针快照；热替换新增渠道无基线时跳过。
func healthChannelSnapshot(
	base map[string]health.Channel, p config.Provider, keys []keypool.KeyView, logger *slog.Logger,
) (health.Channel, bool) {
	ch, ok := base[p.Name]
	if !ok {
		return health.Channel{}, false // 热替换新增渠道的探针声明需重启生效(与余额源同口径)
	}
	if p.ProxyURL != "" {
		// 渠道声明出站代理:探针与推理同路出站(代理不可达计入探针失败,与数据面同口径)
		if cl, err := outboundClient(p.ProxyURL, outboundDial); err == nil {
			ch.Outbound = cl
		} else {
			logger.Warn("渠道出站代理解析失败,探针走默认出站", "provider", p.Name, "error", err.Error())
		}
	}
	for _, kv := range keys {
		ch.Keys = append(ch.Keys, health.ChannelKey{ID: kv.ID, Value: kv.Value})
	}
	return ch, true
}

// healthEmitter 健康事件 → 告警。
func healthEmitter(alerts *alerting.Notifier) func(health.Event) {
	return func(e health.Event) {
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
	}
}

// pollInterval 控制面轮询周期：<=0 按 60s 兜底。
func pollInterval(seconds int) time.Duration {
	interval := time.Duration(seconds) * time.Second
	if interval <= 0 {
		interval = 60 * time.Second
	}
	return interval
}

// startTopologyPoll 控制面拓扑热替换轮询：启动即拉，ETag 条件请求（304 跳过），
// 失败或损坏保持当前快照并告警降级；池构建复用启动闭包（env 解析与 adapter 注册）。
func startTopologyPoll(
	cfg *config.Config, logger *slog.Logger, px *proxy.Proxy,
	build func(config.Provider) (*keypool.Pool, adapter.Adapter, error),
) {
	if cfg.ControlPlane.TopologyURL == "" {
		return
	}
	interval := pollInterval(cfg.ControlPlane.PollIntervalSec)
	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		var etag string
		sync := func() { etag = syncTopologyOnce(client, cfg.ControlPlane.TopologyURL, etag, logger, px, build) }
		sync() // 启动即拉，不等首个周期
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			sync()
		}
	}()
}

// syncTopologyOnce 拉取一次控制面拓扑并原子替换；返回下次条件请求的 ETag。
func syncTopologyOnce(
	client *http.Client, url, etag string, logger *slog.Logger, px *proxy.Proxy,
	build func(config.Provider) (*keypool.Pool, adapter.Adapter, error),
) string {
	payload, newETag, notModified, err := config.FetchTopology(client, url, etag)
	if err != nil {
		logger.Warn("控制面拓扑拉取失败,保持当前快照", "error_detail", err.Error())
		return etag
	}
	if notModified {
		return etag
	}
	providers, routes, models := payload.ToTopology()
	stats, err := px.ReplaceTopology(proxy.Config{
		Providers:    providers,
		Routes:       routes,
		Capabilities: models,
		InheritProxy: true, // 控制面载荷无 proxy_url 字段:沿用旧声明(评审 R1)
	}, build)
	if err != nil {
		logger.Warn("拓扑替换被拒绝,保持当前快照", "error_detail", err.Error())
		return etag
	}
	logger.Info("控制面拓扑已热替换", "reused", stats.Reused,
		"created", stats.Created, "removed", stats.Removed)
	return newETag
}

// startSighupReload 监听 SIGHUP 重读配置。只有拓扑域热更；auth/metrics/刷盘参数/
// 管理面/静态价目/告警/日志与超时变更需重启。
func startSighupReload(
	cfgPath string, logger *slog.Logger, px *proxy.Proxy,
	build func(config.Provider) (*keypool.Pool, adapter.Adapter, error),
) {
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	go func() {
		for range sighup {
			reloadTopology(cfgPath, logger, px, build)
		}
	}()
}

// reloadTopology 重读配置并替换拓扑；校验或替换失败保持旧态并告警。
func reloadTopology(
	cfgPath string, logger *slog.Logger, px *proxy.Proxy,
	build func(config.Provider) (*keypool.Pool, adapter.Adapter, error),
) {
	newCfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Warn("SIGHUP 重载失败,保持当前配置", "error_detail", err.Error())
		return
	}
	stats, err := px.ReplaceTopology(proxy.Config{
		Providers:    append([]config.Provider(nil), newCfg.Providers...),
		Routes:       newCfg.ModelRoutes,
		Capabilities: newCfg.Models,
	}, build)
	if err != nil {
		logger.Warn("SIGHUP 拓扑替换被拒绝,保持当前快照", "error_detail", err.Error())
		return
	}
	logger.Info("SIGHUP 配置已热加载", "reused", stats.Reused,
		"created", stats.Created, "removed", stats.Removed)
}

// newBalanceService 构造并启动余额观测；未声明任何余额源时返回 nil。
//
// 凭证解析与查询器构造一次性完成于装配期（缺凭证启动即失败，与 key env 同纪律）；
// 运行期经拓扑快照动态映射 key（热替换感知），余额查询不触 Pick/Report。
func newBalanceService(cfg *config.Config, db *sql.DB, alerts *alerting.Notifier, logger *slog.Logger,
	proxyRef func() *proxy.Proxy, settlement string) (*balance.Service, error) {
	if cfg.Balance.Enabled != nil && !*cfg.Balance.Enabled {
		return nil, nil
	}
	base, err := balanceBindings(cfg)
	if err != nil {
		return nil, err
	}
	if len(base) == 0 {
		return nil, nil
	}
	svc := balance.NewService(balance.Options{
		DB: db, Notifier: alerts, Logger: logger,
		OnKeyEvaluated:   balanceKeyEvaluated(proxyRef),
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
		Sources:          balanceSources(base, proxyRef, logger),
	})
	svc.Start()
	logger.Info("余额观测已启用", "providers", len(base))
	return svc, nil
}

// balanceBindings 按渠道声明构造余额绑定基线。
func balanceBindings(cfg *config.Config) (map[string]balance.ChannelBinding, error) {
	out := make(map[string]balance.ChannelBinding, len(cfg.Providers))
	for _, p := range cfg.Providers {
		binding, ok, err := balanceBinding(p)
		if err != nil {
			return nil, err
		}
		if ok {
			out[p.Name] = binding
		}
	}
	return out, nil
}

// balanceBinding 构造单渠道余额绑定；未声明余额源时 ok=false。
func balanceBinding(p config.Provider) (balance.ChannelBinding, bool, error) {
	pb := p.Balance
	if pb == nil || pb.Source == "" || pb.Source == balance.SourceNone {
		return balance.ChannelBinding{}, false, nil
	}
	binding := balance.ChannelBinding{Kind: pb.Source}
	if pb.Below != "" {
		v, err := metering.ParsePrice(pb.Below)
		if err != nil { // config 校验已保证,防御分支
			return balance.ChannelBinding{}, false, fmt.Errorf("渠道 %s balance.below 解析失败: %w", p.Name, err)
		}
		binding.BelowCredits = &v
	}
	// 渠道声明出站代理:余额查询与推理/探针同路出站,统一经 outboundClient 缓存
	//(同 URL 单实例,连接池共用);解析失败启动即失败,不让渠道悄悄直连。
	var bcl *http.Client
	if p.ProxyURL != "" {
		cl, err := outboundClient(p.ProxyURL, outboundDial)
		if err != nil {
			return balance.ChannelBinding{}, false, fmt.Errorf("渠道 %s 出站代理解析失败: %w", p.Name, err)
		}
		bcl = cl
	}
	if err := fillBalanceQuerier(&binding, p, pb, bcl); err != nil {
		return balance.ChannelBinding{}, false, err
	}
	return binding, true, nil
}

// fillBalanceQuerier 按余额源类型补齐查询器或币种。
func fillBalanceQuerier(
	binding *balance.ChannelBinding, p config.Provider, pb *config.ProviderBalance, client *http.Client,
) error {
	switch {
	case balance.IsBearer(pb.Source):
		// billing-compat 的币种不可自描述(new-api 随站点显示配置而变),经配置显式声明;
		// 其余 Bearer 源忽略该字段(config 校验已拒绝)。
		q, err := balance.ForWithClient(pb.Source, balance.SourceConfig{
			BaseURL:  p.BaseURL,
			Currency: strings.ToUpper(pb.Currency),
		}, client)
		if err != nil {
			return fmt.Errorf("渠道 %s 余额源构造失败: %w", p.Name, err)
		}
		binding.Querier = q
	case pb.Source == balance.SourceManual:
		binding.Currency = strings.ToUpper(pb.Currency)
	default: // 云账务:凭证只经环境变量进内存,不入日志与配置态
		ak, sk := os.Getenv(pb.AccessKeyEnv), os.Getenv(pb.SecretKeyEnv)
		if ak == "" || sk == "" {
			return fmt.Errorf("渠道 %s 的余额凭证环境变量未设置(%s/%s)", p.Name, pb.AccessKeyEnv, pb.SecretKeyEnv)
		}
		q, err := balance.ForWithClient(pb.Source, balance.SourceConfig{AccessKey: ak, SecretKey: sk}, client)
		if err != nil {
			return fmt.Errorf("渠道 %s 余额源构造失败: %w", p.Name, err)
		}
		binding.Querier = q
	}
	return nil
}

// balanceKeyEvaluated 逐 key 余额回写(credit 桶联动):欠费 → 池冷却让位(幂等),
// 充值 → 提前释放;metrics 双记,与 upstream 侧 cause 分列。
func balanceKeyEvaluated(proxyRef func() *proxy.Proxy) func(string, string, bool, time.Time) {
	return func(provider, keyID string, exhausted bool, now time.Time) {
		if exhausted {
			proxy.CreditEvents.Inc("balance", "enter")
		} else {
			proxy.CreditEvents.Inc("balance", "release")
		}
		px := proxyRef()
		if px == nil {
			return
		}
		_, pools := px.Topology()
		if pool := pools[provider]; pool != nil {
			pool.RecordCreditExhausted(keyID, exhausted, now)
		}
	}
}

// balanceSources 运行期按拓扑快照映射余额绑定。
func balanceSources(
	base map[string]balance.ChannelBinding, proxyRef func() *proxy.Proxy, logger *slog.Logger,
) func() map[string]balance.ChannelBinding {
	return func() map[string]balance.ChannelBinding {
		providers, pools := proxyRef().Topology()
		out := make(map[string]balance.ChannelBinding, len(base))
		for _, p := range providers {
			binding, ok := balanceSnapshot(base, p, pools[p.Name].Snapshot(), logger)
			if ok {
				out[p.Name] = binding
			}
		}
		return out
	}
}

// balanceSnapshot 生成单渠道余额快照；热替换新增渠道的余额源需重启生效。
func balanceSnapshot(
	base map[string]balance.ChannelBinding, p config.Provider, keys []keypool.KeyView, logger *slog.Logger,
) (balance.ChannelBinding, bool) {
	binding, ok := base[p.Name]
	if !ok {
		// 未声明余额源的渠道属常态,静默跳过不产生日志噪音
		if pb := p.Balance; pb != nil && pb.Source != "" && pb.Source != balance.SourceNone {
			logger.Warn("渠道余额源声明需重启生效", "provider", p.Name)
		}
		return balance.ChannelBinding{}, false
	}
	if !balance.IsBearer(binding.Kind) {
		binding.Keys = []balance.ChannelKey{{ID: store.AccountKeyID}}
		return binding, true
	}
	for _, kv := range keys {
		binding.Keys = append(binding.Keys, balance.ChannelKey{ID: kv.ID, Value: kv.Value, Disabled: kv.Disabled})
	}
	return binding, true
}

// newVerifier 构造业务鉴权器（平台 key 解析 + 撤销列表拉取）。
func newVerifier(cfg *config.Config, db *sql.DB, logger *slog.Logger) *auth.Verifier {
	return auth.NewVerifier(db, auth.Options{
		PlatformSecrets:     decodePlatformSecrets(cfg.Auth.PlatformKeySecrets),
		RevocationsURL:      cfg.Auth.RevocationsURL,
		RevocationsInterval: time.Duration(cfg.Auth.RevocationsIntervalSec) * time.Second,
	}, logger)
}

// decodePlatformSecrets 解码平台 key 摘要；非法 base64 或过短条目静默丢弃（既不匹配也不报错）。
func decodePlatformSecrets(secrets []string) [][]byte {
	out := make([][]byte, 0, len(secrets))
	for _, sec := range secrets {
		if raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sec)); err == nil && len(raw) >= 16 {
			out = append(out, raw)
		}
	}
	return out
}

// newGate 业务配额与限流闸。
func newGate(tpm *ratelimit.TPM, db *sql.DB, logger *slog.Logger, shared ratelimit.SharedBackend) *ratelimit.Gate {
	return &ratelimit.Gate{
		RPM:    ratelimit.New(),
		TPM:    tpm,
		Quota:  ratelimit.NewQuota(db, ratelimit.Options{}, logger),
		Logger: logger,
		Shared: shared,
	}
}

// buildRoutes 业务路由表：模式在此只写一次，注册循环与漂移门禁（assemble 返回的
// declared）同源消费，多处手写清单必然失步。中间件分组：数据面全链（auth+配额+
// 限流）、模型发现（仅鉴权）、探活（轻链）。中间件顺序敏感：
// request_id → recover → access_log → auth → quota → ratelimit。
func buildRoutes(px *proxy.Proxy, verifier *auth.Verifier, gate *ratelimit.Gate, logger *slog.Logger) []routeEntry {
	dataPlaneMW := []server.Middleware{
		server.RequestID, server.Recover(logger), server.AccessLog(logger),
		verifier.Middleware, gate.Middleware,
	}
	readonlyMW := []server.Middleware{
		server.RequestID, server.Recover(logger), server.AccessLog(logger),
		verifier.Middleware,
	}
	// Anthropic/Gemini 入站的鉴权头适配仅挂各自路由，先于 verifier。
	inboundMW := []server.Middleware{
		server.RequestID, server.Recover(logger), server.AccessLog(logger),
	}
	return []routeEntry{
		{"POST /v1/chat/completions", server.Chain(px.Handler(), dataPlaneMW...)},
		{"POST /v1/images/generations", server.Chain(px.HandlerImages(), dataPlaneMW...)},
		{"POST /v1/embeddings", server.Chain(px.HandlerEmbeddings(), dataPlaneMW...)},
		{"POST /api/v1/services/embeddings/multimodal-embedding/multimodal-embedding",
			server.Chain(px.HandlerMultimodalEmbeddings(), dataPlaneMW...)},
		// Responses 入站：翻译后进同一管道，鉴权/配额/限流与 chat 同规。
		{"POST /v1/responses", server.Chain(px.HandlerResponses(), dataPlaneMW...)},
		{"POST /v1/messages",
			server.Chain(px.HandlerMessages(), withInboundMW(inboundMW, server.AdaptAnthropicKey, verifier, gate)...)},
		// Gemini 原生入站：鉴权头与查询参数适配仅挂本路由，先于 verifier，
		// ?key= 读后剥离以防访问日志泄漏；{modelMethod} 整段通配，handler 内解析
		// {model}:{method} 后缀，mux 通配符不支持段内混合。
		{"POST /v1beta/models/{modelMethod}",
			server.Chain(px.HandlerGemini(), withInboundMW(inboundMW, server.AdaptGeminiKey, verifier, gate)...)},
		// 无状态边界：不存储 response，恒 404；挂鉴权链，不泄漏给未鉴权方。
		{"GET /v1/responses/{id}", server.Chain(px.HandlerResponsesStateless(), dataPlaneMW...)},
		{"DELETE /v1/responses/{id}", server.Chain(px.HandlerResponsesStateless(), dataPlaneMW...)},
		// 模型目录只读且无上游成本；保留鉴权，不走配额、限流、计量或插件。
		{"GET /v1/models", server.Chain(px.HandlerModels(), readonlyMW...)},
		{"GET /v1/models/{model}", server.Chain(px.HandlerModels(), readonlyMW...)},
		{"GET /healthz",
			server.Chain(http.HandlerFunc(healthz), server.RequestID, server.Recover(logger), server.AccessLog(logger))},
	}
}

// withInboundMW 组装入站翻译路由的中间件链：头适配 → 鉴权 → 限流。
func withInboundMW(
	base []server.Middleware, adapt server.Middleware, verifier *auth.Verifier, gate *ratelimit.Gate,
) []server.Middleware {
	out := make([]server.Middleware, 0, len(base)+3)
	out = append(out, base...)
	out = append(out, adapt, verifier.Middleware, gate.Middleware)
	return out
}

// healthz 轻量探活。
func healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version.Version})
}

// startPricingPoll 控制面价目轮询：启动即拉，失败保持原目录，下轮重试。
func startPricingPoll(cfg *config.Config, logger *slog.Logger, db *sql.DB, refresh func() error) {
	if cfg.ControlPlane.PricingURL == "" {
		return
	}
	interval := pollInterval(cfg.ControlPlane.PollIntervalSec)
	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		pending := false // 上轮刷新失败的待重试标记，比对跳过不得短路刷新
		sync := func() {
			pending = syncPricingOnce(client, cfg.ControlPlane.PricingURL, db, logger, refresh, pending)
		}
		sync() // 启动即拉，不等首个周期
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			sync()
		}
	}()
}

// syncPricingOnce 拉取一轮价目并返回更新后的待重试标记。
func syncPricingOnce(
	client *http.Client, url string, db *sql.DB, logger *slog.Logger, refresh func() error, pending bool,
) bool {
	resp, err := client.Get(url)
	if err != nil {
		logger.Warn("控制面价目拉取失败", "error_detail", err.Error())
		return pending
	}
	var body struct {
		Pricing []admin.PricingView `json:"pricing"`
	}
	err = json.NewDecoder(resp.Body).Decode(&body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil {
		logger.Warn("控制面价目响应非法", "status", resp.StatusCode)
		return pending
	}
	changed := applyPricingItems(db, body.Pricing, time.Now(), logger)
	if !changed && !pending {
		return false
	}
	if err := refresh(); err != nil {
		logger.Warn("控制面价目刷新失败(下轮重试)", "error_detail", err.Error())
		return true
	}
	logger.Info("控制面价目已同步生效", "entries", len(body.Pricing))
	return false
}

// applyPricingItems 逐条 upsert 价目，返回是否有任何落库变更。
func applyPricingItems(db *sql.DB, items []admin.PricingView, now time.Time, logger *slog.Logger) bool {
	changed := false
	for _, item := range items {
		if applyPricingItem(db, item, now, logger) {
			changed = true
		}
	}
	return changed
}

// applyPricingItem 落库单条价目；空别名/非法条目/写失败跳过并留痕。
func applyPricingItem(db *sql.DB, item admin.PricingView, now time.Time, logger *slog.Logger) bool {
	if item.Alias == "" {
		return false
	}
	rec, err := admin.ParsePricingRecord(item.Alias, item, now)
	if err != nil {
		logger.Warn("控制面价目条目非法,跳过", "alias", item.Alias, "error_detail", err.Error())
		return false
	}
	// 内容未变化则跳过，避免 updated_at 失去最后真实变更语义并产生无谓写放大
	if existing, err := store.GetPricing(db, rec.Alias); err == nil && pricingRecordEqual(existing, rec) {
		return false
	}
	if err := store.UpsertPricing(db, rec); err != nil {
		logger.Warn("控制面价目落库失败", "alias", item.Alias, "error_detail", err.Error())
		return false
	}
	return true
}

// pricingRecordEqual 价目内容比对（不含 updated_at）。
func pricingRecordEqual(a *store.PricingRecord, b store.PricingRecord) bool {
	return a.Currency == b.Currency &&
		a.InputCredits == b.InputCredits &&
		a.CachedInputCredits == b.CachedInputCredits &&
		a.VisionInputCredits == b.VisionInputCredits &&
		a.OutputCredits == b.OutputCredits &&
		a.Timezone == b.Timezone &&
		windowsEqual(a.Windows, b.Windows)
}

// newMetrics 按开关与管理 token 启停 /metrics 并注册仪表。
func newMetrics(cfg *config.Config, adminToken string, db *sql.DB, pipeline *metering.Pipeline,
	px *proxy.Proxy, logger *slog.Logger) bool {
	enabled := cfg.Metrics.Enabled && adminToken != ""
	if cfg.Metrics.Enabled && adminToken == "" {
		logger.Warn("metrics.enabled=true 但 MUNIU_ADMIN_TOKEN 未配置,/metrics 不挂载(ADR-0009 管理面鉴权纪律)")
	}
	metrics.SetEnabled(enabled)
	if !enabled {
		return false
	}
	registerMetricsGauges(db, pipeline, px)
	return true
}

// registerMetricsGauges 注册计量水位与池状态仪表。
func registerMetricsGauges(db *sql.DB, pipeline *metering.Pipeline, px *proxy.Proxy) {
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
		_, currentPools := px.Topology()
		for name, pool := range currentPools {
			for state, n := range pool.StateCounts(now) {
				out = append(out, metrics.Sample{Labels: [][2]string{{"provider", name}, {"state", state}}, Value: float64(n)})
			}
		}
		return out
	})
}
