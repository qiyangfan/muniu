// 控制面消费侧装配：客户端/身份、快照应用、注册心跳、命令执行与本地命令。
// 控制面不可达只影响控制能力，不阻塞数据面。
package main

import (
	"context"
	"crypto/rsa"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"uuid"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/admin"
	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/control"
	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/metering"
	"github.com/solvela/muniu/internal/proxy"
	"github.com/solvela/muniu/internal/ratelimit"
	"github.com/solvela/muniu/internal/semantic"
	"github.com/solvela/muniu/internal/store"
	"github.com/solvela/muniu/internal/version"
)

// controlWiring 控制面运行时装配输入。
type controlWiring struct {
	cfg        *config.Config
	cfgPath    string
	db         *sql.DB
	logger     *slog.Logger
	client     *control.Client
	publicKey  *rsa.PublicKey
	px         *proxy.Proxy
	buildPool  func(config.Provider) (*keypool.Pool, adapter.Adapter, error)
	pricingDir *metering.PricingDirectory
	settlement string
	fxMilli    int64
	verifier   *auth.Verifier
	quotas     *ratelimit.MemoryTenantQuotas
	cache      *semantic.Cache
	startedAt  time.Time
}

// newControlClient managed 下构造服务身份客户端与验签公钥；standalone 返回 nil。
func newControlClient(cfg *config.Config, logger *slog.Logger) (*control.Client, *rsa.PublicKey, error) {
	if !cfg.ControlPlane.Managed() {
		return nil, nil, nil
	}
	cp := cfg.ControlPlane
	var token control.TokenSource
	if cp.TokenURL != "" {
		token = &control.ClientCredentials{
			TokenURL: cp.TokenURL, ClientID: cp.ClientID, ClientSecret: cp.ClientSecret,
		}
	} else {
		token = control.StaticToken(cp.ServiceToken)
	}
	pub, err := control.LoadPublicKey(cp.SnapshotPublicKeyFile)
	if err != nil {
		return nil, nil, err
	}
	timeout := time.Duration(cp.RequestTimeoutSec) * time.Second
	client := control.NewClient(cp.BaseURL, token, timeout)
	logger.Info("控制面客户端已配置", "base_url", cp.BaseURL, "mode", cp.Mode)
	return client, pub, nil
}

// defaultInstanceID 主机名+进程号；重启需稳定时应显式配置 control_plane.instance_id。
func defaultInstanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "muniu"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

// controlAssembly 装配层传入的控制运行时输入（字段较多，单独收口便于 assemble 保持精简）。
type controlAssembly struct {
	cfg        *config.Config
	cfgPath    string
	db         *sql.DB
	logger     *slog.Logger
	client     *control.Client
	publicKey  *rsa.PublicKey
	px         *proxy.Proxy
	buildPool  func(config.Provider) (*keypool.Pool, adapter.Adapter, error)
	pricingDir *metering.PricingDirectory
	settlement string
	fxMilli    int64
	verifier   *auth.Verifier
	quotas     *ratelimit.MemoryTenantQuotas
	cache      *semantic.Cache
	startedAt  time.Time
}

// buildControlRuntime 由装配输入构造控制运行时。
func buildControlRuntime(ctx context.Context, a controlAssembly) (*control.Runtime, error) {
	return newControlRuntime(ctx, controlWiring(a))
}

// newControlRuntime 构造控制面运行时；standalone 只装配本地命令执行器。
func newControlRuntime(ctx context.Context, w controlWiring) (*control.Runtime, error) {
	instanceID := w.cfg.ControlPlane.InstanceID
	if instanceID == "" {
		instanceID = defaultInstanceID()
	}
	targets := &commandTargets{
		cache: w.cache, cfgPath: w.cfgPath, managed: w.cfg.ControlPlane.Managed(),
		logger: w.logger, px: w.px, buildPool: w.buildPool,
	}
	if !w.cfg.ControlPlane.Managed() {
		return control.NewLocalRuntime(w.db, instanceID, targets, w.logger), nil
	}
	rt, err := control.NewRuntime(control.RuntimeOptions{
		ServiceID: control.ServiceID, InstanceID: instanceID, Version: version.Version,
		StartedAt: w.startedAt,
		Client:    w.client, PublicKey: w.publicKey, DB: w.db,
		Store:             controlSnapshotStore{db: w.db},
		Applier:           &snapshotApplier{wiring: w},
		Targets:           targets,
		Health:            controlHealth(w.db, w.cache != nil, w.startedAt),
		Audit:             controlCommandAudit(w.db, w.logger),
		RegistrationAudit: controlRegistrationAudit(w.db, w.logger),
		Logger:            w.logger,
		SnapshotInterval:  time.Duration(w.cfg.ControlPlane.PollIntervalSec) * time.Second,
		HeartbeatInterval: time.Duration(w.cfg.ControlPlane.HeartbeatIntervalSec) * time.Second,
		CommandInterval:   time.Duration(w.cfg.ControlPlane.CommandPollIntervalSec) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	if err := rt.LoadLastKnown(ctx); err != nil {
		// last-known 损坏不阻塞启动：安全类由配额/吊销检查 fail-closed，数据面本地 key 可继续。
		w.logger.Warn("控制快照 last-known 不可用,等待控制面恢复", "error_detail", err.Error())
	}
	return rt, nil
}

// controlSnapshotStore last-known 读取适配。
type controlSnapshotStore struct{ db *sql.DB }

// Load 读取最近成功快照。
func (s controlSnapshotStore) Load(ctx context.Context, serviceID string) (*store.ControlSnapshotRecord, error) {
	return store.LoadControlSnapshot(ctx, s.db, serviceID)
}

// snapshotApplier 原子应用快照：先构建派生数据，再单事务落库，最后切换内存目录。
type snapshotApplier struct{ wiring controlWiring }

// ApplySnapshot 实现 control.Applier。
func (a *snapshotApplier) ApplySnapshot(ctx context.Context, in control.ApplyInput) error {
	p := in.Envelope.Payload
	if err := control.ValidatePayload(p); err != nil {
		return err
	}
	now := time.Now().UTC()
	records, tables, replacePricing, err := a.pricingRecords(p.Pricing, now)
	if err != nil {
		return err
	}
	prep, err := a.prepareTopology(p.Topology)
	if err != nil {
		return err
	}
	quotas, tenants := tenantProjection(p.Tenants)
	state := store.ControlState{
		Snapshot: store.ControlSnapshotRecord{
			ServiceID: control.ServiceID, Version: in.Version, ETag: in.ETag,
			ObservedAt: in.ObservedAt.UTC().Format(time.RFC3339), Signature: in.Signature,
			Payload: in.Raw, UpdatedAt: now.Format(time.RFC3339),
		},
		ReplacePricing: replacePricing, Pricing: records, Tenants: tenants,
		Audit: a.auditRow(in, len(records), len(tenants), prep != nil),
	}
	if err := store.ApplyControlState(ctx, a.wiring.db, state); err != nil {
		a.wiring.px.DiscardTopology(prep)
		return err
	}
	a.wiring.px.CommitTopology(prep)
	if replacePricing {
		a.wiring.pricingDir.Replace(tables)
	}
	if a.wiring.verifier != nil {
		a.wiring.verifier.ApplyRevocations(p.Revocations.Version, p.Revocations.Revoked)
	}
	if a.wiring.quotas != nil {
		a.wiring.quotas.Replace(quotas)
	}
	a.wiring.logger.Info("共享控制快照已应用", "version", in.Version,
		"pricing", len(records), "tenants", len(tenants), "topology", prep != nil)
	return nil
}

// pricingRecords 价目投影：nil=缺席（保留现状），空数组=显式空。先建表后落库。
func (a *snapshotApplier) pricingRecords(entries []control.PricingEntry, now time.Time) (
	[]store.PricingRecord, map[string]*metering.PricingTable, bool, error,
) {
	if entries == nil {
		return nil, nil, false, nil
	}
	records := make([]store.PricingRecord, 0, len(entries))
	tables := make(map[string]*metering.PricingTable, len(entries))
	for _, e := range entries {
		view := admin.PricingView{
			Alias: e.Alias, Currency: e.Currency, Input: e.Input, CachedInput: e.CachedInput,
			VisionInput: e.VisionInput, Output: e.Output, Timezone: e.Timezone,
			TimeWindows: pricingWindowsView(e.TimeWindows),
		}
		rec, err := admin.ParsePricingRecord(e.Alias, view, now)
		if err != nil {
			return nil, nil, false, fmt.Errorf("价目 %s 非法: %w", e.Alias, err)
		}
		tbl, err := pricingTableFromRecord(rec, a.wiring.settlement, a.wiring.fxMilli)
		if err != nil {
			return nil, nil, false, err
		}
		records = append(records, rec)
		tables[e.Alias] = tbl
	}
	return records, tables, true, nil
}

// pricingWindowsView 快照窗口转管理面视图。
func pricingWindowsView(windows []control.PricingWindow) []admin.PricingWindowView {
	out := make([]admin.PricingWindowView, 0, len(windows))
	for _, w := range windows {
		out = append(out, admin.PricingWindowView{Window: w.Window, Multiplier: w.Multiplier})
	}
	return out
}

// prepareTopology 构建候选拓扑；nil=缺席保留现状。
func (a *snapshotApplier) prepareTopology(t *control.Topology) (*proxy.PreparedTopology, error) {
	if t == nil {
		return nil, nil
	}
	providers := make([]config.Provider, 0, len(t.Providers))
	for _, p := range t.Providers {
		keys := make([]config.KeyRef, 0, len(p.Keys))
		for _, k := range p.Keys {
			keys = append(keys, config.KeyRef{Env: k.Env, Account: k.Account, Weight: k.Weight})
		}
		providers = append(providers, config.Provider{
			Name: p.Name, Adapter: p.Adapter, BaseURL: p.BaseURL, Auth: p.Auth,
			NativeMultimodalEmbeddingsURL: p.NativeMultimodalEmbeddingsURL,
			Keys:                          keys, Weight: p.Weight, Priority: p.Priority,
		})
	}
	routes := make(map[string][]config.Candidate, len(t.ModelRoutes))
	for alias, cands := range t.ModelRoutes {
		for _, c := range cands {
			routes[alias] = append(routes[alias], config.Candidate{
				Provider: c.Provider, Model: c.Model, Priority: c.Priority, Weight: c.Weight,
			})
		}
	}
	models := make([]config.ModelCapability, 0, len(t.Models))
	for _, m := range t.Models {
		models = append(models, config.ModelCapability{
			Provider: m.Provider, Model: m.Model, DropParams: m.DropParams,
			ThinkingLock: m.ThinkingLock, MaxOutputTokens: m.MaxOutputTokens,
		})
	}
	return a.wiring.px.PrepareTopology(proxy.Config{
		Providers: providers, Routes: routes, Capabilities: models, InheritProxy: true,
	}, a.wiring.buildPool)
}

// tenantProjection 租户目录投影为内存配额与本地镜像。
func tenantProjection(tenants []control.Tenant) (map[string]ratelimit.TenantQuota, []store.ControlTenantRecord) {
	quotas := make(map[string]ratelimit.TenantQuota, len(tenants))
	rows := make([]store.ControlTenantRecord, 0, len(tenants))
	for _, t := range tenants {
		quotas[t.ID] = ratelimit.TenantQuota{
			RPM: t.RPMLimit, TPM: t.TPMLimit, DailyQuotaCredits: t.DailyQuotaCredits,
		}
		rows = append(rows, store.ControlTenantRecord{
			ID: t.ID, Name: t.Name, Status: t.Status,
			DailyQuotaCredits: t.DailyQuotaCredits, TPMLimit: t.TPMLimit,
		})
	}
	return quotas, rows
}

// auditRow 快照应用审计，与投影同事务落库。
func (a *snapshotApplier) auditRow(in control.ApplyInput, pricing, tenants int, topology bool) *store.AdminAudit {
	id := uuid.NewV7().String()
	return &store.AdminAudit{
		ID: id, RequestID: id, ActorKind: "system", ActorName: "control-snapshot",
		Action: "snapshot.apply", TargetID: fmt.Sprintf("v%d", in.Version), Outcome: "ok",
		Detail:    fmt.Sprintf("pricing=%d tenants=%d topology=%t", pricing, tenants, topology),
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
}

// commandTargets 本地/跨服务命令的可执行动作。
type commandTargets struct {
	cache     *semantic.Cache
	cfgPath   string
	managed   bool
	logger    *slog.Logger
	px        *proxy.Proxy
	buildPool func(config.Provider) (*keypool.Pool, adapter.Adapter, error)
}

// PurgeCache 清理语义缓存；未启用时零动作成功。
func (t *commandTargets) PurgeCache(ctx context.Context, tenant string, all bool) (int, error) {
	if t.cache == nil {
		return 0, nil
	}
	if all {
		tenant = ""
	}
	n, err := t.cache.Clear(ctx, tenant)
	return int(n), err
}

// ReloadTopology standalone 重读配置拓扑；managed 拓扑归控制面，本地不放行。
func (t *commandTargets) ReloadTopology(ctx context.Context) error {
	if t.managed {
		return errors.New("managed 模式下拓扑由控制面快照管理")
	}
	return reloadTopology(t.cfgPath, t.logger, t.px, t.buildPool)
}

// controlHealth 真实健康探针：DB 可达 + 资源水位，不伪造 ready。
func controlHealth(db *sql.DB, cacheEnabled bool, started time.Time) func(context.Context) control.Health {
	return func(ctx context.Context) control.Health {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		status := "ok"
		dbOK := true
		if err := db.PingContext(ctx); err != nil {
			status = "degraded"
			dbOK = false
		}
		return control.Health{
			Status: status,
			Metrics: map[string]any{
				"db_ok": dbOK, "semantic_cache": cacheEnabled,
				"uptime_seconds": int64(time.Since(started).Seconds()),
			},
		}
	}
}

// controlRegistrationAudit 注册/心跳状态迁移审计。
func controlRegistrationAudit(db *sql.DB, logger *slog.Logger) control.RegistrationAudit {
	return func(ctx context.Context, outcome, detail string) {
		id := uuid.NewV7().String()
		a := store.AdminAudit{
			ID: id, RequestID: id, ActorKind: "system", ActorName: "control-registry",
			Action: "registry.register", Outcome: outcome, Detail: detail,
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if err := store.AppendAdminAudit(ctx, db, a); err != nil {
			logger.Error("注册状态审计写库失败", "error_detail", err.Error())
		}
	}
}

// controlCommandAudit 跨服务命令审计落库；失败只留痕不阻断回执。
func controlCommandAudit(db *sql.DB, logger *slog.Logger) control.CommandAudit {
	return func(ctx context.Context, actor, action, target, outcome, detail string) {
		if actor == "" {
			actor = "control"
		}
		id := uuid.NewV7().String()
		a := store.AdminAudit{
			ID: id, RequestID: id, ActorKind: "system", ActorName: actor,
			Action: "command." + action, TargetID: target, Outcome: outcome,
			Detail: detail, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if err := store.AppendAdminAudit(ctx, db, a); err != nil {
			logger.Error("跨服务命令审计写库失败", "command_id", target, "error_detail", err.Error())
		}
	}
}
