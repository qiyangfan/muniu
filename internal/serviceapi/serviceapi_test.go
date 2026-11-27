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

func newHandler(summary func(context.Context) (map[string]any, error)) http.Handler {
	return New(Deps{Token: readToken, Summary: summary, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func TestServiceAPI_认证与摘要(t *testing.T) {
	h := newHandler(func(context.Context) (map[string]any, error) {
		return map[string]any{"source": "muniu", "service_id": "muniu", "domain": map[string]any{"api_keys": 1}}, nil
	})
	// 匿名 401
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/service/v1/summary", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("匿名应 401: %d", rec.Code)
	}
	// 正确 token 200 + observed_at
	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/service/v1/summary", nil)
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

func TestServiceAPI_摘要错误与未装配(t *testing.T) {
	h := newHandler(func(context.Context) (map[string]any, error) {
		return nil, errors.New("db down")
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/service/v1/summary", nil)
	req.Header.Set("Authorization", "Bearer "+readToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("摘要失败应 500: %d", rec.Code)
	}
	// Summary 未装配 → 404（不返回空壳）
	h2 := newHandler(nil)
	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/service/v1/summary", nil)
	req.Header.Set("Authorization", "Bearer "+readToken)
	rec = httptest.NewRecorder()
	h2.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未装配摘要应 404: %d", rec.Code)
	}
}
