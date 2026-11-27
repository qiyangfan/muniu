// 服务注册/心跳：managed 下以服务身份上报实例、能力、版本与健康；失败指数退避，
// 控制面恢复后自动补报。上报内容不含 endpoint、凭据或服务域配置。
package control

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Health 健康事件；status 只由真实探针给出，未知时不伪造 ok。
type Health struct {
	Source     string         `json:"source"`
	Status     string         `json:"status"`
	ObservedAt time.Time      `json:"observed_at"`
	Metrics    map[string]any `json:"metrics,omitempty"`
}

// Identity 注册身份与版本/能力单一来源。
type Identity struct {
	ServiceID   string
	InstanceID  string
	Environment string
	Version     string
	StartedAt   time.Time
}

// RegistrationAudit 注册/心跳状态迁移审计；不逐心跳写库，只在状态变化时落痕。
type RegistrationAudit func(ctx context.Context, outcome, detail string)

// RegistrationStatus 注册状态（控制台诊断）。
type RegistrationStatus struct {
	State       string    `json:"state"`
	LastAttempt time.Time `json:"last_attempt"`
	LastSuccess time.Time `json:"last_success"`
	Attempts    int       `json:"attempts"`
	LastError   string    `json:"last_error,omitempty"`
}

// Registrar 注册/心跳循环。
type Registrar struct {
	Client   *Client
	Identity Identity
	Interval time.Duration
	Backoff  time.Duration // 首次退避，指数翻倍至 60s；零值取 1s（测试可注入）
	Health   func(context.Context) Health
	Audit    RegistrationAudit
	Logger   *slog.Logger

	mu     sync.RWMutex
	status RegistrationStatus
}

// NewRegistrar 构造；缺省 30s 心跳。
func NewRegistrar(client *Client, id Identity, interval time.Duration, health func(context.Context) Health,
	logger *slog.Logger) *Registrar {
	backoff := time.Second
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	if backoff <= 0 {
		backoff = time.Second
	}
	return &Registrar{Client: client, Identity: id, Interval: interval, Backoff: backoff,
		Health: health, Logger: logger, status: RegistrationStatus{State: "starting"}}
}

// Run 启动即注册，成功后周期心跳；任何失败指数退避并重新注册（幂等）。
func (r *Registrar) Run(ctx context.Context) {
	backoff := r.Backoff
	registered := false
	for {
		if !registered {
			if err := r.register(ctx); err != nil {
				r.setFailure(ctx, err)
				r.Logger.Warn("服务注册失败,退避重试", "attempts", r.Attempts(), "retry_in", backoff,
					"error_detail", err.Error())
				if !sleepCtx(ctx, backoff) {
					return
				}
				backoff = nextBackoff(backoff)
				continue
			}
			registered = true
		}
		if err := r.heartbeat(ctx); err != nil {
			registered = false
			r.setFailure(ctx, err)
			r.Logger.Warn("服务心跳失败,重新注册后重试", "attempts", r.Attempts(), "retry_in", backoff,
				"error_detail", err.Error())
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		// 注册与心跳都成功才复位退避：心跳单独故障时不得回到 1s 紧循环。
		backoff = r.Backoff
		if !sleepCtx(ctx, r.Interval) {
			return
		}
	}
}

// register 幂等注册（同一 instanceId 重复上报只更新 last_seen）。
func (r *Registrar) register(ctx context.Context) error {
	environment := r.Identity.Environment
	if environment == "" {
		environment = "local"
	}
	payload := map[string]any{
		"serviceId":         r.Identity.ServiceID,
		"instanceId":        r.Identity.InstanceID,
		"environment":       environment,
		"version":           r.Identity.Version,
		"capabilities":      Capabilities(),
		"adminCapabilities": AdminAPIs(),
	}
	if _, _, err := r.Client.Post(ctx, r.Client.Paths.Register, payload); err != nil {
		return err
	}
	r.mu.Lock()
	wasOK := r.status.State == "ok"
	r.status.State = "ok"
	r.status.LastSuccess = time.Now().UTC()
	r.status.LastError = ""
	r.status.Attempts = 0
	r.mu.Unlock()
	if !wasOK {
		r.audit(ctx, "ok", "registered "+r.Identity.InstanceID)
	}
	r.reportHealth(ctx, r.health(ctx))
	r.Logger.Info("服务已注册", "service_id", r.Identity.ServiceID, "instance_id", r.Identity.InstanceID)
	return nil
}

// heartbeat 心跳只报告实例存活；健康状态走独立 /ingest/health，避免伪造 ready。
func (r *Registrar) heartbeat(ctx context.Context) error {
	payload := map[string]any{
		"serviceId":  r.Identity.ServiceID,
		"instanceId": r.Identity.InstanceID,
	}
	if _, _, err := r.Client.Post(ctx, r.Client.Paths.HeartbeatPath(r.Identity.InstanceID), payload); err != nil {
		return err
	}
	r.reportHealth(ctx, r.health(ctx))
	r.mu.Lock()
	recovered := r.status.State != "ok"
	r.status.State = "ok"
	r.status.LastSuccess = time.Now().UTC()
	r.status.LastError = ""
	r.mu.Unlock()
	if recovered {
		r.audit(ctx, "ok", "heartbeat recovered "+r.Identity.InstanceID)
	}
	return nil
}

// reportHealth 上报真实健康事件；未知状态按 DEGRADED 上报，不伪造 UP。
// 健康上报失败不改变注册状态，避免把控制面局部故障误判为数据面故障。
func (r *Registrar) reportHealth(ctx context.Context, h Health) {
	status := "DEGRADED"
	if h.Status == "ok" {
		status = "UP"
	}
	payload := map[string]any{
		"serviceId":  r.Identity.ServiceID,
		"instanceId": r.Identity.InstanceID,
		"status":     status,
		"observedAt": time.Now().UTC().Format(time.RFC3339),
	}
	if _, _, err := r.Client.Post(ctx, r.Client.Paths.Health, payload); err != nil {
		r.Logger.Warn("健康事件上报失败(不影响注册)", "error_detail", err.Error())
	}
}

// health 真实探针；未注入时返回 unknown，不伪造健康。
func (r *Registrar) health(ctx context.Context) Health {
	if r.Health == nil {
		return Health{Source: ServiceID, Status: "unknown", ObservedAt: time.Now().UTC()}
	}
	h := r.Health(ctx)
	if h.Source == "" {
		h.Source = ServiceID
	}
	if h.Status == "" {
		h.Status = "unknown"
	}
	h.ObservedAt = time.Now().UTC()
	return h
}

// setFailure 记录失败：attempts 累计，下一次循环先重新注册；状态迁移时审计一次。
func (r *Registrar) setFailure(ctx context.Context, err error) {
	r.mu.Lock()
	transition := r.status.State != "degraded"
	r.status.State = "degraded"
	r.status.LastAttempt = time.Now().UTC()
	r.status.Attempts++
	r.status.LastError = err.Error()
	r.mu.Unlock()
	if transition {
		r.audit(ctx, "degraded", err.Error())
	}
}

// audit 状态迁移审计回调；未注入时静默。
func (r *Registrar) audit(ctx context.Context, outcome, detail string) {
	if r.Audit == nil {
		return
	}
	r.Audit(ctx, outcome, detail)
}

// Status 当前注册状态。
func (r *Registrar) Status() RegistrationStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

// Attempts 当前连续失败次数。
func (r *Registrar) Attempts() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status.Attempts
}

// nextBackoff 指数退避，上限 60s。
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > time.Minute {
		return time.Minute
	}
	return d
}

// sleepCtx 可取消等待。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
