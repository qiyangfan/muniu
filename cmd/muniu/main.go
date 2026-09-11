// muniu 主进程入口:只做装配(配置→存储→日志→适配器→计量→中间件链→代理→优雅退出),
// 禁止业务逻辑(architecture「包结构」)。
package main

import (
	"context"
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
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/proxy"
	"github.com/solvela/muniu/internal/ratelimit"
	"github.com/solvela/muniu/internal/semantic"
	"github.com/solvela/muniu/internal/server"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/version"
	"github.com/solvela/muniu/web"
)

func main() {
	// keys 管理子命令与网关服务分流(同一配置定位与存储)
	if ka, ok := keysSubArgs(os.Args); ok {
		os.Exit(runKeys(flagConfOf(os.Args), ka))
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

	handler, _, cleanup, err := assemble(cfg, logger)
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
func assemble(cfg *config.Config, logger *slog.Logger) (http.Handler, []string, func(), error) {
	// SQLite:运行态(api_keys/usage_logs);迁移随打开执行
	db, err := store.Open(cfg.Storage.SQLite.Path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("打开存储: %w", err)
	}
	// 刷盘参数接线(缺省值已在 config.validate 兜底):配置-行为不可漂移
	pipeline := metering.NewPipeline(db, metering.Options{
		Interval: time.Duration(cfg.Metering.FlushIntervalMs) * time.Millisecond,
		Batch:    cfg.Metering.FlushBatch,
	}, logger)
	tpm := ratelimit.NewTPM()

	// 适配器注册:Phase 2 仅 openai 基座+quirks;key 取 provider 第一把 key 的环境变量值
	registry := adapter.NewRegistry()
	pools := make(map[string]*keypool.Pool, len(cfg.Providers))
	for _, p := range cfg.Providers {
		if len(p.Keys) == 0 {
			return nil, nil, nil, errors.New("provider " + p.Name + " 无可用 key 引用")
		}
		// key 池:全部 key 环境变量注入;任一缺失即启动失败(静态拓扑 fail-fast)
		inputs := make([]keypool.KeyInput, 0, len(p.Keys))
		first := ""
		for i, kr := range p.Keys {
			v := os.Getenv(kr.Env)
			if v == "" {
				return nil, nil, nil, fmt.Errorf("provider %s 的 key 环境变量 %s 未设置", p.Name, kr.Env)
			}
			if i == 0 {
				first = v
			}
			inputs = append(inputs, keypool.KeyInput{ID: kr.Env, Value: v, Weight: kr.Weight})
		}
		pools[p.Name] = keypool.NewPool(inputs)
		ad, err := adapter.For(p.Adapter, p.BaseURL, first)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("provider %s: %w", p.Name, err)
		}
		registry.Register(p.Name, ad)
	}

	// 价目:人类可读 → μ 整数(加载期一次性换算,全链路零浮点)
	pricing := make(map[string]metering.Pricing, len(cfg.Pricing))
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
		pricing[alias] = metering.Pricing{
			Currency:             strings.ToUpper(pr.Currency),
			InputMicroPerM:       inMicro,
			CachedInputMicroPerM: cachedMicro,
			OutputMicroPerM:      outMicro,
		}
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

	chatProxy := proxy.New(proxy.Config{
		Registry: registry,
		Routes:   cfg.ModelRoutes,
		Pricing:  pricing,
		Timeouts: proxy.Timeouts{
			Dial:       cfg.Server.Timeouts.DialDuration(),
			FirstToken: cfg.Server.Timeouts.FirstTokenDuration(),
			ChunkIdle:  cfg.Server.Timeouts.ChunkIdleDuration(),
		},
		Pipeline:     pipeline,
		Pools:        pools,
		TPM:          tpm,
		Cache:        cacheInst,
		TenantSwitch: tenantSw,
		Logger:       logger,
	})

	// 业务路由全链:顺序即契约 request_id → recover → access_log → auth → quota → ratelimit
	verifier := auth.NewVerifier(db, auth.Options{}, logger)
	gate := &ratelimit.Gate{
		RPM:    ratelimit.New(),
		TPM:    tpm,
		Quota:  ratelimit.NewQuota(db, ratelimit.Options{}, logger),
		Logger: logger,
	}
	chatMux := http.NewServeMux()
	chatMux.Handle("POST /v1/chat/completions", chatProxy.Handler())
	chatMux.Handle("POST /v1/embeddings", chatProxy.HandlerEmbeddings())
	chat := server.Chain(chatMux,
		server.RequestID, server.Recover(logger), server.AccessLog(logger),
		verifier.Middleware, gate.Middleware)

	// 管理面(T4.1):MUNIU_ADMIN_TOKEN 未配置则整体关闭(含控制台——无后端的空壳页
	// 不该对外暴露,与告警文案保持一致)
	adminToken := os.Getenv("MUNIU_ADMIN_TOKEN")
	if adminToken == "" {
		logger.Warn("MUNIU_ADMIN_TOKEN 未配置,管理面与控制台关闭")
	}
	adminDeps := admin.Deps{
		DB: db, Token: adminToken, Providers: cfg.Providers, Pools: pools,
		Verifier: verifier, Logger: logger,
	}
	if cacheInst != nil {
		adminDeps.CacheStats = cacheInst.Stats
	}
	adminHandler := admin.New(adminDeps)

	// 探活路由轻链(不鉴权)
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "version": version.Version})
	})
	health := server.Chain(healthMux, server.RequestID, server.Recover(logger), server.AccessLog(logger))

	root := http.NewServeMux()
	root.Handle("POST /v1/chat/completions", chat)
	root.Handle("POST /v1/embeddings", chat)
	root.Handle("GET /healthz", health)
	routes := []string{"POST /v1/chat/completions", "POST /v1/embeddings", "GET /healthz"}
	if adminHandler != nil {
		root.Handle("/admin/v1/", adminHandler)
		root.Handle("GET /console/", web.Handler())
		routes = append(routes, admin.Routes()...)
	}

	// 嵌入通道绑定:HTTP 链就绪后进程内自调用 /v1/embeddings
	if cacheInst != nil {
		bindEmbedder(late, root, embedKey)
	}

	cleanup := func() {
		late.set(func(context.Context, string, string) ([]float32, error) {
			return nil, errors.New("已关闭")
		})
		_ = pipeline.Close() // 退出前排干计量存量
	}
	return root, routes, cleanup, nil
}
