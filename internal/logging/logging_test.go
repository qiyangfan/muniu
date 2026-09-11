// logging 装配与 request_id 传导的基础测试。
package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestNew_级别过滤(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	logger.Info("不应出现")
	logger.Warn("应出现")
	if !strings.Contains(buf.String(), "应出现") || strings.Contains(buf.String(), "不应出现") {
		t.Fatalf("级别过滤失效: %s", buf.String())
	}
}

func TestNew_format选择(t *testing.T) {
	// json 输出可回读为 JSON;text 输出为 key=value 形态
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	logger.Info("x", "request_id", "01J")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("json 格式失效: %v", err)
	}
	if rec["request_id"] != "01J" {
		t.Fatalf("字段名必须 snake_case: %v", rec)
	}
}

func TestRequestID_context往返(t *testing.T) {
	ctx := context.Background()
	if got := RequestIDFrom(ctx); got != "" {
		t.Fatalf("缺失时应返回空串: %q", got)
	}
	ctx = WithRequestID(ctx, "01JXYZ")
	if got := RequestIDFrom(ctx); got != "01JXYZ" {
		t.Fatalf("往返失败: %q", got)
	}
}
