// Package alerting 通过 webhook 主动通知运营关键事件：事件经去重窗口、退避重试后 POST JSON。
// 发射异步且带超时，通知失败不干扰数据面主路径；未配置 webhook_url 时 Notifier 为 nil，Emit 零开销关闭。
package alerting

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Options 通知器装配参数，测试可注入时钟、退避与客户端。
type Options struct {
	WebhookURL  string          // 空 = 关闭，调用方也可直接不构造 Notifier
	DedupWindow time.Duration   // 同事件键静默窗口；缺省 5m
	Backoff     []time.Duration // 重试退避序列；缺省 {100ms, 400ms, 1.6s}
	Timeout     time.Duration   // 单次 POST 超时；缺省 3s
	Logger      *slog.Logger
	Now         func() time.Time
}

// Notifier 进程内通知器，并发安全；零值不可用，须经 New 构造。
type Notifier struct {
	url      string
	window   time.Duration
	backoff  []time.Duration
	timeout  time.Duration
	logger   *slog.Logger
	now      func() time.Time
	mu       sync.Mutex
	lastSeen map[string]time.Time
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
		url:      opts.WebhookURL,
		window:   window,
		backoff:  backoff,
		timeout:  timeout,
		logger:   logger,
		now:      now,
		lastSeen: map[string]time.Time{},
	}
}

// Emit 异步发射事件，同事件键在去重窗口内静默；永不 panic、不阻塞调用方。
func (n *Notifier) Emit(eventKey, severity, summary string) {
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
	go n.deliver(eventKey, severity, summary, now)
}

// deliver 退避重试投递；全部失败则放弃并留日志，告警丢失不能拖垮网关。
func (n *Notifier) deliver(eventKey, severity, summary string, at time.Time) {
	payload, err := json.Marshal(map[string]any{
		"event": eventKey, "severity": severity, "summary": summary,
		"occurred_at": at.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	client := &http.Client{Timeout: n.timeout}
	for i, wait := range append([]time.Duration{0}, n.backoff...) {
		if wait > 0 {
			time.Sleep(wait)
		}
		resp, err := client.Post(n.url, "application/json", bytes.NewReader(payload))
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return
			}
			err = &statusCodeError{code: resp.StatusCode}
		}
		if i == len(n.backoff) { // 追加首零后，末次尝试下标为 len(backoff)
			n.logger.Warn("告警通知最终失败(已放弃)", "event", eventKey, "error_detail", err.Error())
			return
		}
	}
}

type statusCodeError struct{ code int }

func (e *statusCodeError) Error() string { return http.StatusText(e.code) }
