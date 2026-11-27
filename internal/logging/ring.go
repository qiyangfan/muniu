// 进程内日志环形缓冲：运行时控制台只读展示最近日志；敏感键一律脱敏。
package logging

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Entry 环形缓冲中的单条日志。
type Entry struct {
	Time    time.Time         `json:"time"`
	Level   string            `json:"level"`
	Message string            `json:"message"`
	Attrs   map[string]string `json:"attrs,omitempty"`
}

// Ring 有界日志环；Add 丢弃最旧条目。
type Ring struct {
	mu      sync.Mutex
	entries []Entry
	cap     int
}

// NewRing 构造；容量 ≤0 取 200。
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		capacity = 200
	}
	return &Ring{cap: capacity, entries: make([]Entry, 0, capacity)}
}

// Add 追加一条；敏感键值脱敏后入环。
func (r *Ring) Add(e Entry) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	e.Attrs = redactAttrs(e.Attrs)
	if len(r.entries) >= r.cap {
		copy(r.entries, r.entries[1:])
		r.entries[len(r.entries)-1] = e
		return
	}
	r.entries = append(r.entries, e)
}

// Recent 取最近 limit 条（时间正序）；limit ≤0 取全部。
func (r *Ring) Recent(limit int) []Entry {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	start := 0
	if limit > 0 && len(r.entries) > limit {
		start = len(r.entries) - limit
	}
	out := make([]Entry, len(r.entries)-start)
	copy(out, r.entries[start:])
	return out
}

// redactAttrs 敏感键脱敏。
func redactAttrs(attrs map[string]string) map[string]string {
	for k := range attrs {
		lower := strings.ToLower(k)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") ||
			strings.Contains(lower, "password") || strings.Contains(lower, "authorization") ||
			strings.Contains(lower, "credential") || strings.Contains(lower, "api_key") {
			attrs[k] = "[REDACTED]"
		}
	}
	return attrs
}

// TeeHandler 同时写基础 handler 与日志环。
type TeeHandler struct {
	base  slog.Handler
	ring  *Ring
	attrs []slog.Attr
}

// NewTeeHandler 包装基础 handler；ring 为 nil 时退化为 base。
func NewTeeHandler(base slog.Handler, ring *Ring) slog.Handler {
	if ring == nil {
		return base
	}
	return &TeeHandler{base: base, ring: ring}
}

// Enabled 透传基础 handler。
func (h *TeeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.base.Enabled(ctx, level)
}

// Handle 先入环再写基础 handler。
func (h *TeeHandler) Handle(ctx context.Context, rec slog.Record) error {
	attrs := make(map[string]string, len(h.attrs)+rec.NumAttrs())
	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.String()
	}
	rec.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.String()
		return true
	})
	h.ring.Add(Entry{
		Time: rec.Time, Level: levelName(rec.Level), Message: rec.Message, Attrs: attrs,
	})
	return h.base.Handle(ctx, rec)
}

// WithAttrs 保留到后续记录。
func (h *TeeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TeeHandler{base: h.base.WithAttrs(attrs), ring: h.ring,
		attrs: append(append([]slog.Attr(nil), h.attrs...), attrs...)}
}

// WithGroup 透传分组；环形缓冲仍按扁平键记录。
func (h *TeeHandler) WithGroup(name string) slog.Handler {
	return &TeeHandler{base: h.base.WithGroup(name), ring: h.ring, attrs: h.attrs}
}

// levelName 日志级别短名。
func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "error"
	case l >= slog.LevelWarn:
		return "warn"
	case l >= slog.LevelInfo:
		return "info"
	default:
		return "debug"
	}
}
