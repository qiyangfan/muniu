package proxy

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/keypool"
	"github.com/solvela/muniu/internal/testkit"
)

func TestFailover_预算尝试上限(t *testing.T) {
	h := newFoHarness(t,
		testkit.StartJSONUpstream(t, 500, `{"error":{"message":"boom"}}`),
		500, `{"error":{"message":"boom"}}`, nil,
		Timeouts{Dial: time.Second, FirstToken: 10 * time.Second, ChunkIdle: 10 * time.Second, MaxAttempts: 1})
	code, body := foPost(t, h.ts, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	if code != 504 || !strings.Contains(body, "failover_budget_exhausted") {
		t.Fatalf("尝试上限应 504 预算信封: %d %s", code, body)
	}
}

func TestFailover_全池禁用_503(t *testing.T) {
	h := newFoHarness(t,
		testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody),
		200, testkit.JSONOKBody, nil)
	now := time.Now()
	h.poolA.Report(&keypool.Key{ID: "KA"}, keypool.OutcomeAuthFailed, now, 0)
	h.poolB.Report(&keypool.Key{ID: "KB"}, keypool.OutcomeAuthFailed, now, 0)
	code, body := foPost(t, h.ts, `{"model":"alias-a","messages":[{"role":"user","content":"hi"}]}`)
	if code != 503 || !strings.Contains(body, "keypool_exhausted") {
		t.Fatalf("全池禁用应 503 keypool_exhausted: %d %s", code, body)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	if got := parseRetryAfter("60"); got != 60*time.Second {
		t.Fatalf("秒数形态失真: %v", got)
	}
	future := time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(future); got < 80*time.Second || got > 100*time.Second {
		t.Fatalf("HTTP-date 形态失真: %v", got)
	}
	past := time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past); got != 0 {
		t.Fatalf("过去时刻应回落 0: %v", got)
	}
	if got := parseRetryAfter("nonsense"); got != 0 {
		t.Fatalf("非法值应回落 0: %v", got)
	}
}
