package protocol

import "testing"

// 错误信封契约测试:类型→状态映射与未知类型兜底(conventions「错误信封」)。
func TestStatusForType(t *testing.T) {
	cases := []struct {
		typ  string
		want int
	}{
		{TypeBadRequest, 400},
		{TypeAuthFailed, 401},
		{TypeQuotaExceeded, 402},
		{TypeForbidden, 403},
		{TypeRateLimited, 429},
		{TypeNotFound, 404},
		{TypeTimeout, 504},
		{TypeUpstreamError, 502},
		{TypeInternal, 500},
		{"unknown_type", 500}, // 未知类型按 internal 兜底,不得 panic/零值
		{"", 500},
	}
	for _, c := range cases {
		if got := StatusForType(c.typ); got != c.want {
			t.Fatalf("StatusForType(%q) = %d, 期望 %d", c.typ, got, c.want)
		}
	}
}

// not_found 信封形态:404 + code 透传 + request_id(模型发现 404 契约)。
func TestNewErrorEnvelope_NotFound(t *testing.T) {
	env := NewErrorEnvelope(TypeNotFound, "model_not_found", "模型不存在", "req-1")
	if env.Error.Type != "not_found" || env.Error.Code != "model_not_found" || env.Error.RequestID != "req-1" {
		t.Fatalf("信封字段不符: %+v", env.Error)
	}
}
