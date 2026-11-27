// 日志环与 Tee handler：容量淘汰、最近读取、敏感键脱敏与 WithAttrs 传播。
package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestRing_容量淘汰与Recent(t *testing.T) {
	r := NewRing(3)
	for i := 0; i < 5; i++ {
		r.Add(Entry{Time: time.Unix(int64(i), 0), Level: "info", Message: "m"})
	}
	all := r.Recent(0)
	if len(all) != 3 {
		t.Fatalf("容量应为 3: %d", len(all))
	}
	if all[2].Time.Unix() != 4 {
		t.Fatalf("应保留最新条目: %+v", all)
	}
	last := r.Recent(2)
	if len(last) != 2 || last[1].Time.Unix() != 4 {
		t.Fatalf("Recent(2) 不符: %+v", last)
	}
}

func TestRing_敏感键脱敏(t *testing.T) {
	r := NewRing(5)
	r.Add(Entry{Message: "x", Attrs: map[string]string{
		"access_token": "secret", "password": "p", "api_key": "k",
		"authorization": "a", "credential": "c", "tenant_id": "t1",
	}})
	e := r.Recent(1)[0]
	for _, k := range []string{"access_token", "password", "api_key", "authorization", "credential"} {
		if e.Attrs[k] != "[REDACTED]" {
			t.Fatalf("%s 应脱敏: %v", k, e.Attrs)
		}
	}
	if e.Attrs["tenant_id"] != "t1" {
		t.Fatalf("普通字段不得改动: %v", e.Attrs)
	}
}

func TestTeeHandler_入环与基础输出(t *testing.T) {
	var buf bytes.Buffer
	base := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	r := NewRing(10)
	logger := slog.New(NewTeeHandler(base, r))
	logger.With("request_id", "r1").WithGroup("g").Info("hello", "k", "v")
	entries := r.Recent(1)
	if len(entries) != 1 || entries[0].Message != "hello" {
		t.Fatalf("环中应有日志: %+v", entries)
	}
	if entries[0].Attrs["request_id"] != "r1" || entries[0].Attrs["k"] != "v" {
		t.Fatalf("WithAttrs 应传播到环: %+v", entries[0].Attrs)
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Fatal("基础 handler 应继续输出")
	}
	if got := levelName(slog.LevelWarn); got != "warn" {
		t.Fatalf("levelName 不符: %s", got)
	}
}

func TestNewWithRing(t *testing.T) {
	r := NewRing(5)
	logger := NewWithRing("debug", "text", r)
	logger.DebugContext(context.Background(), "debug-msg")
	if len(r.Recent(0)) != 1 {
		t.Fatal("NewWithRing 应写入日志环")
	}
	if NewTeeHandler(slog.NewJSONHandler(&bytes.Buffer{}, nil), nil) == nil {
		t.Fatal("nil ring 应退化为基础 handler")
	}
}
