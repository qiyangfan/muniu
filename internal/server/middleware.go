// 中间件实现(顺序即契约,architecture「中间件链」):request_id → recover → access_log → …。
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/google/uuid"

	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
)

// RequestIDHeader 响应回带 request_id 的头名。
const RequestIDHeader = "X-Muniu-Request-Id"

// RequestID 签发 UUIDv7(与 DB 主键同源),写 ctx + 回带响应头。
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := uuid.NewV7()
		if err != nil {
			// v7 依赖单调时钟,失败概率极低;退化为 v4 保住链路可用性
			id = uuid.New()
		}
		w.Header().Set(RequestIDHeader, id.String())
		next.ServeHTTP(w, r.WithContext(logging.WithRequestID(r.Context(), id.String())))
	})
}

// Recover 兜底 panic → 500 internal 信封 + 堆栈日志,绝不裸崩进程。
// http.ErrAbortHandler 是 net/http 内部控制流约定,必须原样放行。
func Recover(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if rec == http.ErrAbortHandler {
						panic(rec)
					}
					logger.Error("panic 已兜底",
						"request_id", logging.RequestIDFrom(r.Context()),
						"error_detail", tostring(rec),
						"stack", string(debug.Stack()),
					)
					env := protocol.NewErrorEnvelope(protocol.TypeInternal, "internal", "服务内部错误", logging.RequestIDFrom(r.Context()))
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_ = json.NewEncoder(w).Encode(env)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// statusWriter 捕获 handler 实际写出的状态码(供访问日志)。
// 必须透传 Flush:包裹后若方法集缺失 http.Flusher,下游 SSE 逐事件冲刷会静默退化为缓冲写。
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Flush 转发底层 Flusher(net/http 场景恒可用;测试 ResponseWriter 亦实现)。
func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap 支持 http.ResponseController 穿透包装定位底层能力。
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// AccessLog 进入/退出各一条 slog;退出条含状态与耗时(conventions 字段表)。
func AccessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			requestID := logging.RequestIDFrom(r.Context())
			logger.LogAttrs(r.Context(), slog.LevelInfo, "请求进入",
				slog.String("request_id", requestID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
			)
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r)
			logger.LogAttrs(r.Context(), slog.LevelInfo, "请求退出",
				slog.String("request_id", requestID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", sw.status),
				slog.Int64("latency_total_ms", time.Since(start).Milliseconds()),
			)
		})
	}
}

// tostring panic 值转可读字串(error/stringer 兜底)。
func tostring(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case error:
		return x.Error()
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return "不可序列化的 panic 值"
		}
		return string(b)
	}
}

// loggingRequestID ctx 取 request_id 的包内别名(测试与后续中间件共用)。
func loggingRequestID(ctx context.Context) string {
	return logging.RequestIDFrom(ctx)
}
