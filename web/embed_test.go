package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestHandlerRouting(t *testing.T) {
	t.Parallel()
	sub := fstest.MapFS{
		"index.html": {Data: []byte("<html>ok</html>")},
		"app.js":     {Data: []byte("console.log(1)")},
		"style.css":  {Data: []byte("body{}")},
		"icon.svg":   {Data: []byte("<svg/>")},
		"blob.bin":   {Data: []byte{0x00}},
	}
	h := handler(sub)
	tests := []struct {
		name     string
		path     string
		wantBody string
		wantType string
	}{
		{"根路径回退 index.html", "/", "<html>ok</html>", "text/html; charset=utf-8"},
		{"console 前缀剥离", "/console/app.js", "console.log(1)", "text/javascript; charset=utf-8"},
		{"css 类型", "/console/style.css", "body{}", "text/css; charset=utf-8"},
		{"svg 类型", "/console/icon.svg", "<svg/>", "image/svg+xml"},
		// 未知扩展名不由 handler 指派 Content-Type,交给 net/http 嗅探(二进制即 octet-stream)
		{"未知扩展名", "/console/blob.bin", "\x00", "application/octet-stream"},
		// 单页应用:未知路由回退 index.html,内容类型按 html 标注
		{"SPA 回退", "/console/settings", "<html>ok</html>", "text/html; charset=utf-8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
			if got := rec.Header().Get("Content-Type"); got != tt.wantType {
				t.Fatalf("Content-Type = %q, want %q", got, tt.wantType)
			}
		})
	}
}

// 未构建控制台时 dist 只有占位文件:回退链两级都读不到,必须 404 而不是空 200。
func TestHandlerMissingIndex(t *testing.T) {
	t.Parallel()
	h := handler(fstest.MapFS{"app.js": {Data: []byte("x")}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
