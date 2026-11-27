// Runtime 聚合控制面消费侧生命周期：快照、注册心跳、命令 claim；Close 可重复调用。
package control

import (
	"context"
	"crypto/rsa"
	"database/sql"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/solvela/muniu/internal/store"
)

// RuntimeOptions 装配输入。
type RuntimeOptions struct {
	ServiceID  string
	InstanceID string
	Version    string
	StartedAt  time.Time

	Client            *Client
	PublicKey         *rsa.PublicKey
	DB                *sql.DB
	Store             SnapshotStore
	Applier           Applier
	Targets           CommandTargets
	Health            func(context.Context) Health
	Audit             CommandAudit
	RegistrationAudit RegistrationAudit
	Logger            *slog.Logger

	SnapshotInterval  time.Duration
	HeartbeatInterval time.Duration
	CommandInterval   time.Duration
}

// Runtime 控制面消费侧聚合。
type Runtime struct {
	opts      RuntimeOptions
	snapshot  *SnapshotConsumer
	registrar *Registrar
	commands  *CommandExecutor

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewRuntime 构造；Applier 必填。
func NewRuntime(opts RuntimeOptions) (*Runtime, error) {
	if opts.Applier == nil {
		return nil, errors.New("control: 快照 Applier 未装配")
	}
	if opts.ServiceID == "" {
		opts.ServiceID = ServiceID
	}
	if opts.StartedAt.IsZero() {
		opts.StartedAt = time.Now()
	}
	snap := NewSnapshotConsumer(opts.Client, opts.PublicKey, opts.Applier, opts.Store,
		opts.ServiceID, opts.SnapshotInterval, opts.Logger)
	id := Identity{
		ServiceID: opts.ServiceID, InstanceID: opts.InstanceID, Version: opts.Version,
		StartedAt: opts.StartedAt,
	}
	reg := NewRegistrar(opts.Client, id, opts.HeartbeatInterval, opts.Health, opts.Logger)
	reg.Audit = opts.RegistrationAudit
	cmd := NewCommandExecutor(opts.DB, opts.Client, opts.InstanceID, opts.Targets, opts.Audit,
		opts.CommandInterval, opts.Logger)
	return &Runtime{opts: opts, snapshot: snap, registrar: reg, commands: cmd}, nil
}

// LoadLastKnown 启动时应用本地快照；失败不阻塞启动（安全类由配额/吊销检查 fail-closed）。
func (r *Runtime) LoadLastKnown(ctx context.Context) error {
	return r.snapshot.LoadLastKnown(ctx)
}

// NewLocalRuntime standalone 模式：只有本地命令执行器，快照/注册/claim 全部关闭。
func NewLocalRuntime(db *sql.DB, instanceID string, targets CommandTargets, logger *slog.Logger) *Runtime {
	if logger == nil {
		logger = slog.Default()
	}
	cmd := NewCommandExecutor(db, nil, instanceID, targets, nil, time.Minute, logger)
	return &Runtime{opts: RuntimeOptions{
		ServiceID: ServiceID, InstanceID: instanceID, Version: "local", StartedAt: time.Now(),
		DB: db, Logger: logger,
	}, commands: cmd}
}

// Start 启动后台循环；managed 三个循环，standalone 无网络循环。
func (r *Runtime) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	if r.snapshot != nil {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); r.snapshot.Run(ctx) }()
	}
	if r.registrar != nil {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); r.registrar.Run(ctx) }()
	}
	if r.commands != nil && r.commands.Client != nil {
		r.wg.Add(1)
		go func() { defer r.wg.Done(); r.commands.Run(ctx) }()
	}
}

// Close 停止后台循环并等待退出；重复调用安全。
func (r *Runtime) Close() {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
}

// SnapshotStatus 快照状态；standalone 返回未就绪空态。
func (r *Runtime) SnapshotStatus() SnapshotStatus {
	if r.snapshot == nil {
		return SnapshotStatus{}
	}
	return r.snapshot.Status()
}

// RegistrationStatus 注册状态；standalone 返回 disabled。
func (r *Runtime) RegistrationStatus() RegistrationStatus {
	if r.registrar == nil {
		return RegistrationStatus{State: "disabled"}
	}
	return r.registrar.Status()
}

// LastSnapshot 最近应用成功的快照。
func (r *Runtime) LastSnapshot() *SnapshotEnvelope {
	if r.snapshot == nil {
		return nil
	}
	return r.snapshot.LastSnapshot()
}

// Commands 最近命令台账。
func (r *Runtime) Commands(ctx context.Context, limit int) ([]CommandView, error) {
	if r.commands == nil {
		return []CommandView{}, nil
	}
	return r.commands.Commands(ctx, limit)
}

// CommandStats 台账状态计数。
func (r *Runtime) CommandStats(ctx context.Context) (map[string]int64, error) {
	if r.opts.DB == nil {
		return map[string]int64{"pending": 0, "acked": 0, "failed": 0}, nil
	}
	return store.CountControlCommandsByStatus(ctx, r.opts.DB)
}

// ExecuteLocal 本地命令直接执行。
func (r *Runtime) ExecuteLocal(ctx context.Context, actor, reason, action string,
	params []byte) (CommandView, error) {
	if r.commands == nil {
		return CommandView{}, errors.New("control: 命令执行器未装配")
	}
	return r.commands.ExecuteLocal(ctx, actor, reason, action, params)
}

// InstanceID 实例标识。
func (r *Runtime) InstanceID() string { return r.opts.InstanceID }

// ServiceID 服务标识。
func (r *Runtime) ServiceID() string { return r.opts.ServiceID }

// StartedAt 进程启动时刻。
func (r *Runtime) StartedAt() time.Time { return r.opts.StartedAt }
