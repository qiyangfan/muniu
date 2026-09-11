// Package logging 装配 slog 与跨层公共字段(conventions「日志」节)。
// 结构化字段一律 snake_case;request_id 是每请求必带字段,经 context 传导。
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// ctxKey 非导出键类型,防跨包键冲突。
type ctxKey struct{}

// New 按配置装配 slog Logger:level ∈ debug|info|warn|error,format ∈ json|text。
// 非法值已在 config 校验拦截,此处按 info/json 兜底(防御性,不 panic)。
func New(level, format string) *slog.Logger {
	var lv slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lv}
	var handler slog.Handler
	if strings.EqualFold(format, "text") {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

// WithRequestID 把 request_id 写入 context(生命周期第 1 步签发后全程携带)。
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, requestID)
}

// RequestIDFrom 从 context 取 request_id;缺失返回空串(启动期/系统日志无请求上下文)。
func RequestIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}
