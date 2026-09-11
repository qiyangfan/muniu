// Package server 装配 HTTP 入口:标准库 http.ServeMux + 约 30 行 chain 辅助(ADR-0002,
// 不引第三方路由/中间件库)与中间件链;优雅退出预算 15s(architecture「优雅退出」)。
package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Middleware 中间件形态:包裹 next Handler。
type Middleware func(next http.Handler) http.Handler

// Chain 依序包裹:mw1(mw2(handler)),即声明顺序=执行顺序(architecture「中间件链」)。
func Chain(handler http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		handler = mws[i](handler)
	}
	return handler
}

// New 构造 http.Server。刻意不设全局 Read/WriteTimeout:流式响应是长连接,
// 超时语义由 proxy 对上游的三层超时承担;这里只设头部读超时与空闲回收。
func New(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Listen 绑定监听(与 Run 分离,便于测试在启动前拿到真实地址)。
func Listen(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}

// shutdownBudget 优雅退出预算;architecture 定为 15s。
const shutdownBudget = 15 * time.Second

// Run 阻塞服务直到 ctx 取消(信号触发)或监听失败;取消后进入优雅退出:
// 停收新请求,在途请求跑完或超时强关。
func Run(ctx context.Context, srv *http.Server, ln net.Listener, logger *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(ln) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownBudget)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("优雅退出超时,强制关闭", "error_detail", err.Error())
		_ = srv.Close()
		return err
	}
	logger.Info("HTTP 服务已优雅退出")
	return nil
}
