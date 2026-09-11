// server 契约测试:中间件链行为(panic 兜底/request_id 回带/访问日志)与优雅退出(在途请求跑完)。
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

func TestChain_顺序执行(t *testing.T) {
	var order []string
	mk := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}
	h := Chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { order = append(order, "handler") }), mk("a"), mk("b"))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
	if strings.Join(order, ",") != "a,b,handler" {
		t.Fatalf("链顺序不符: %v", order)
	}
}

func TestRequestID_回带响应头并写入ctx(t *testing.T) {
	var seenInHandler string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenInHandler = loggingRequestID(r.Context())
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	got := rec.Header().Get("X-Muniu-Request-Id")
	if got == "" {
		t.Fatal("X-Muniu-Request-Id 必须回带")
	}
	if got != seenInHandler {
		t.Fatalf("ctx 中的 request_id(%q)与响应头(%q)不一致", seenInHandler, got)
	}
	// UUIDv7 形态粗校验:36 位、含连字符
	if len(got) != 36 || strings.Count(got, "-") != 4 {
		t.Fatalf("request_id 非 UUID 形态: %q", got)
	}
}

func TestRecover_panic兜住并返回500信封(t *testing.T) {
	var buf bytes.Buffer
	logger := testLogger(&buf)
	h := Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("上游炸了测试用")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic 应映射 500,实际 %d", rec.Code)
	}
	var env struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是合法信封: %v (%s)", err, rec.Body.String())
	}
	if env.Error.Type != "internal" {
		t.Fatalf("panic 应归 internal,实际 %q", env.Error.Type)
	}
	if !strings.Contains(buf.String(), "上游炸了测试用") {
		t.Fatalf("日志必须含 panic 详情: %s", buf.String())
	}
}

func TestAccessLog_进出两条且含状态耗时(t *testing.T) {
	var buf bytes.Buffer
	logger := testLogger(&buf)
	h := AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(5 * time.Millisecond)
		w.WriteHeader(http.StatusTeapot)
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/chat/completions", nil))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("进出应各一条日志,实际 %d 条: %s", len(lines), buf.String())
	}
	var exit map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &exit); err != nil {
		t.Fatalf("退出日志非法: %v", err)
	}
	if exit["status"].(float64) != http.StatusTeapot {
		t.Fatalf("退出日志状态不符: %v", exit)
	}
	if _, ok := exit["latency_total_ms"]; !ok {
		t.Fatalf("退出日志缺 latency_total_ms: %v", exit)
	}
}

func TestRun_优雅退出在途请求跑完(t *testing.T) {
	var buf bytes.Buffer
	logger := testLogger(&buf)

	inFlightDone := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		close(inFlightDone)
	})

	ctx, cancel := context.WithCancel(context.Background())
	srv := New("127.0.0.1:0", mux)
	ln, err := Listen(srv.Addr)
	if err != nil {
		t.Fatalf("绑定监听失败: %v", err)
	}
	url := "http://" + ln.Addr().String()
	errCh := make(chan error, 1)
	go func() { errCh <- Run(ctx, srv, ln, logger) }()

	go func() {
		resp, err := http.Get(url + "/slow")
		if err != nil {
			t.Errorf("在途请求失败: %v", err)
			return
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("在途请求应 200,实际 %d", resp.StatusCode)
		}
	}()

	time.Sleep(100 * time.Millisecond) // 让在途请求进入 handler
	cancel()                           // 触发退出

	select {
	case <-inFlightDone:
		// 在途请求已完成
	case err := <-errCh:
		t.Fatalf("server 提前退出: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("优雅退出超时,在途请求未跑完")
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Run 应正常返回: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未在退出后返回")
	}
}
