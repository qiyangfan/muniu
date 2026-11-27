// 服务只读 API：身份校验、摘要输出与错误语义。
package serviceapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

const readToken = "read-token-0123456789abcdef"

type fakeVerifier struct {
	client string
	err    error
}

func (f fakeVerifier) Verify(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.client, nil
}

func newHandler(summary func(context.Context) (map[string]any, error)) http.Handler {
	return New(Deps{Token: readToken, Summary: summary, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func serviceReq() *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/service/v1/summary", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	return req
}

func TestServiceAPI_认证与摘要(t *testing.T) {
	h := newHandler(func(context.Context) (map[string]any, error) {
		return map[string]any{"source": "muniu", "service_id": "muniu", "domain": map[string]any{"api_keys": 1}}, nil
	})
	// 匿名 401
	req := serviceReq()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("匿名应 401: %d", rec.Code)
	}
	// 正确 token 200 + observed_at
	req = serviceReq()
	req.Header.Set("Authorization", "Bearer "+readToken)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确身份应 200: %d %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["source"] != "muniu" || body["observed_at"] == nil {
		t.Fatalf("摘要不符: %v", body)
	}
}

func TestServiceAPI_本地令牌仅限回环(t *testing.T) {
	h := newHandler(func(context.Context) (map[string]any, error) {
		return map[string]any{"source": "muniu"}, nil
	})
	req := serviceReq()
	req.RemoteAddr = "192.0.2.10:54321"
	req.Header.Set("Authorization", "Bearer "+readToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("非回环来源不得使用本地令牌: %d", rec.Code)
	}
}

func TestServiceAPI_Keycloak服务令牌(t *testing.T) {
	summary := func(context.Context) (map[string]any, error) {
		return map[string]any{"source": "muniu"}, nil
	}
	h := New(Deps{
		Verifier: fakeVerifier{client: "jinguan-read-muniu"},
		Summary:  summary,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	req := serviceReq()
	req.RemoteAddr = "192.0.2.10:54321"
	req.Header.Set("Authorization", "Bearer kc-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("Keycloak 服务令牌应 200: %d %s", rec.Code, rec.Body.String())
	}

	bad := New(Deps{
		Verifier: fakeVerifier{err: errors.New("bad token")},
		Summary:  summary,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	rec = httptest.NewRecorder()
	bad.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("校验失败应 401: %d", rec.Code)
	}
}

func TestServiceAPI_摘要错误与未装配(t *testing.T) {
	h := newHandler(func(context.Context) (map[string]any, error) {
		return nil, errors.New("db down")
	})
	req := serviceReq()
	req.Header.Set("Authorization", "Bearer "+readToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("摘要失败应 500: %d", rec.Code)
	}
	// Summary 未装配 → 404（不返回空壳）
	h2 := newHandler(nil)
	req = serviceReq()
	req.Header.Set("Authorization", "Bearer "+readToken)
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未装配摘要应 404: %d", rec.Code)
	}
}
