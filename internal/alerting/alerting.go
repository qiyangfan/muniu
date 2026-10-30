// Package alerting 通过 webhook 主动通知运营关键事件：事件经去重窗口、退避重试后 POST JSON。
// 发射异步且带超时，通知失败不干扰数据面主路径；未配置 webhook_url 时 Notifier 为 nil，Emit 零开销关闭。
package alerting

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// DeliveryFailure 投递终局失败的回调事实(装配层落库自监控);ErrorClass 归一为
// http_status_<code> 或 network。回调在投递 goroutine 内执行,不得用于外发(通道已断,外发即递归)。
type DeliveryFailure struct {
	EventKey   string
	Severity   string
	Summary    string
	ErrorClass string
	At         time.Time
}

// Options 通知器装配参数，测试可注入时钟、退避与客户端。
type Options struct {
	WebhookURL  string          // 空 = 关闭，调用方也可直接不构造 Notifier
	DedupWindow time.Duration   // 同事件键静默窗口；缺省 5m
	Backoff     []time.Duration // 重试退避序列；缺省 {100ms, 400ms, 1.6s}
	Timeout     time.Duration   // 单次 POST 超时；缺省 3s
	Logger      *slog.Logger
	Now         func() time.Time
	// 投递终局回调(告警通道自监控,装配层接 store 落库);nil = 关闭。
	// 在投递 goroutine 内执行并经 recover 防御——自监控失败不得反噬投递路径。
	OnDeliveryFailure func(DeliveryFailure)
	OnDeliverySuccess func(eventKey string, at time.Time)
}

// Notifier 进程内通知器，并发安全；零值不可用，须经 New 构造。
type Notifier struct {
	url       string
	window    time.Duration
	backoff   []time.Duration
	timeout   time.Duration
	logger    *slog.Logger
	now       func() time.Time
	onFail    func(DeliveryFailure)
	onSuccess func(eventKey string, at time.Time)
	mu        sync.Mutex
	lastSeen  map[string]time.Time
}

// New 构造通知器；webhook_url 为空时返回 nil，Emit 对 nil 接收者零开销。
func New(opts Options) *Notifier {
	if opts.WebhookURL == "" {
		return nil
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	backoff := opts.Backoff
	if len(backoff) == 0 {
		backoff = []time.Duration{100 * time.Millisecond, 400 * time.Millisecond, 1600 * time.Millisecond}
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	window := opts.DedupWindow
	if window <= 0 {
		window = 5 * time.Minute
	}
	return &Notifier{
		url:       opts.WebhookURL,
		window:    window,
		backoff:   backoff,
		timeout:   timeout,
		logger:    logger,
		now:       now,
		onFail:    opts.OnDeliveryFailure,
		onSuccess: opts.OnDeliverySuccess,
		lastSeen:  map[string]time.Time{},
	}
}

// Emit 异步发射事件，同事件键在去重窗口内静默；永不 panic、不阻塞调用方。
func (n *Notifier) Emit(eventKey, severity, summary string) {
	n.EmitContext(eventKey, severity, summary, nil)
}

// EmitContext 带结构化上下文的发射:payload 增补可选 context 对象(渠道名/余额/估算等),
// 事件键去重窗口语义不变;context 为 nil 时载荷与 Emit 完全一致(既有接入点零变化)。
func (n *Notifier) EmitContext(eventKey, severity, summary string, context map[string]any) {
	if n == nil {
		return
	}
	now := n.now()
	n.mu.Lock()
	if last, ok := n.lastSeen[eventKey]; ok && now.Sub(last) < n.window {
		n.mu.Unlock()
		return
	}
	n.lastSeen[eventKey] = now
	n.mu.Unlock()
	go n.deliver(eventKey, severity, summary, context, now)
}

// deliver 退避重试投递；全部失败则放弃并留日志，告警丢失不能拖垮网关。
func (n *Notifier) deliver(eventKey, severity, summary string, context map[string]any, at time.Time) {
	payload := map[string]any{
		"event": eventKey, "severity": severity, "summary": summary,
		"occurred_at": at.UTC().Format(time.RFC3339),
	}
	if context != nil {
		payload["context"] = context
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	client := &http.Client{Timeout: n.timeout}
	for i, wait := range append([]time.Duration{0}, n.backoff...) {
		if wait > 0 {
			time.Sleep(wait)
		}
		resp, err := client.Post(n.url, "application/json", bytes.NewReader(encoded))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				if n.onSuccess != nil {
					safeCall(func() { n.onSuccess(eventKey, n.now()) })
				}
				return
			}
			err = &statusCodeError{code: resp.StatusCode}
		}
		if i == len(n.backoff) { // 追加首零后，末次尝试下标为 len(backoff)
			n.logger.Warn("告警通知最终失败(已放弃)", "event", eventKey, "error_detail", err.Error())
			if n.onFail != nil {
				safeCall(func() {
					n.onFail(DeliveryFailure{
						EventKey: eventKey, Severity: severity, Summary: summary,
						ErrorClass: errorClass(err), At: at,
					})
				})
			}
			return
		}
	}
}

// errorClass 投递失败归类:状态码类与网络类二分,自监控落库与告警载荷共用同一口径。
func errorClass(err error) string {
	if sc, ok := err.(*statusCodeError); ok {
		return "http_status_" + strconv.Itoa(sc.code)
	}
	return "network"
}

// safeCall 自监控回调的 recover 防御:回调 panic 不得反噬投递 goroutine。
func safeCall(f func()) {
	defer func() { _ = recover() }()
	f()
}

type statusCodeError struct{ code int }

func (e *statusCodeError) Error() string { return http.StatusText(e.code) }
