//go:build smoke

// 真上游冒烟(手动档,CI 禁跑):make smoke PROVIDER=<名>
// 环境变量:
//
//	MUNIU_SMOKE_KEY      必填,真 key(ollama 等本地实现可用任意占位值)
//	MUNIU_SMOKE_BASE_URL 必填,如 https://api.deepseek.com/v1
//	MUNIU_SMOKE_PROVIDER 适配器形态(openai/deepseek/moonshot/glm,缺省 openai)
//	MUNIU_SMOKE_MODEL    上游模型名,缺省请求体自带
package adapter_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/adapter/bridge"
	"github.com/solvela/muniu/internal/protocol"
)

// blank import:接线 anthropic 桥接工厂
var _ = bridge.NewAnthropic

// smokeEnv 读取冒烟环境变量;缺配置则跳过(不 fail CI)。
func smokeEnv(t *testing.T) (adapter.Adapter, string) {
	t.Helper()
	key := os.Getenv("MUNIU_SMOKE_KEY")
	base := os.Getenv("MUNIU_SMOKE_BASE_URL")
	if key == "" || base == "" {
		t.Skip("MUNIU_SMOKE_KEY / MUNIU_SMOKE_BASE_URL 未设置,跳过真上游冒烟")
	}
	provider := os.Getenv("MUNIU_SMOKE_PROVIDER")
	if provider == "" {
		provider = "openai"
	}
	ad, err := adapter.For(provider, base, key)
	if err != nil {
		t.Fatalf("构造适配器: %v", err)
	}
	return ad, os.Getenv("MUNIU_SMOKE_MODEL")
}

func TestSmoke_Env(t *testing.T) {
	ad, model := smokeEnv(t)
	smokeKey := os.Getenv("MUNIU_SMOKE_KEY")
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"只回复:pong"}]}`, model)
	req, err := protocol.ParseChatRequest([]byte(body))
	if err != nil {
		t.Fatal(err)
	}

	out, err := ad.Prepare(req, req.Model, os.Getenv("MUNIU_SMOKE_KEY"))
	if err != nil {
		t.Fatal(err)
	}

	// 非流式
	httpReq, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, out.URL, bytes.NewReader(out.Body))
	httpReq.Header = out.Header
	start := time.Now()
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("非流式请求失败: %v", err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	t.Logf("非流式: status=%d 耗时=%s body=%s", resp.StatusCode, time.Since(start).Round(time.Millisecond), truncateSmk(b))
	if resp.StatusCode != 200 {
		t.Fatalf("非流式状态码 %d", resp.StatusCode)
	}
	var chatResp protocol.ChatResponse
	if err := json.Unmarshal(b, &chatResp); err != nil {
		t.Fatalf("非流式响应非 JSON: %v", err)
	}
	if usage, err := ad.NormalizeUsage(chatResp.Usage); err != nil {
		t.Logf("usage 归一失败(登记 quirk): %v", err)
	} else {
		t.Logf("非流式 usage: %+v", usage)
	}

	// 流式
	req.Stream = true
	out, err = ad.Prepare(req, req.Model, smokeKey)
	if err != nil {
		t.Fatal(err)
	}
	out.Body, _ = json.Marshal(func() any {
		var m map[string]json.RawMessage
		_ = json.Unmarshal(req.Raw, &m)
		m["stream"] = json.RawMessage("true")
		if model != "" {
			m["model"], _ = json.Marshal(model)
		}
		return m
	}())
	httpReq, _ = http.NewRequestWithContext(t.Context(), http.MethodPost, out.URL, bytes.NewReader(out.Body))
	httpReq.Header = out.Header
	resp, err = http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("流式请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		t.Fatalf("流式状态码 %d", resp.StatusCode)
	}
	events, done := 0, false
	br := bufio.NewScanner(resp.Body)
	var lastPayload string
	for br.Scan() {
		line := strings.TrimSpace(br.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			done = true
			break
		}
		events++
		lastPayload = payload
	}
	t.Logf("流式: events=%d done=%v 末帧=%s", events, done, truncateSmk([]byte(lastPayload)))
	if events == 0 || !done {
		t.Fatalf("流式不完整: events=%d done=%v", events, done)
	}
}

func truncateSmk(b []byte) string {
	if len(b) > 300 {
		return string(b[:300]) + "…"
	}
	return string(b)
}

// 按供应商入口:make smoke PROVIDER=deepseek → -run TestSmoke_deepseek(共用 TestSmoke_Env)。
func TestSmoke_deepseek(t *testing.T) { TestSmoke_Env(t) }
func TestSmoke_moonshot(t *testing.T) { TestSmoke_Env(t) }
func TestSmoke_glm(t *testing.T)      { TestSmoke_Env(t) }
func TestSmoke_ollama(t *testing.T)   { TestSmoke_Env(t) }
func TestSmoke_openai(t *testing.T)   { TestSmoke_Env(t) }
