// fake 上游自身的行为测试(fake 也是代码):剧本字节完整性、半帧切分、中途掐断、请求记录。
package testkit

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// readAll 从 URL 读全响应 body。
func readAll(t *testing.T, url string) (string, error) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func TestFakeStream_normal剧本字节完整(t *testing.T) {
	events := NormalEvents()
	u := StartStreamUpstream(t, events)

	got, err := readAll(t, u.URL())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	var want strings.Builder
	for _, ev := range events {
		want.WriteString(ev.Data)
	}
	if got != want.String() {
		t.Fatalf("剧本字节不完整:\n got=%q\nwant=%q", got, want.String())
	}
	if !strings.HasSuffix(got, doneEvent) {
		t.Fatalf("正常流必须以 [DONE] 收尾: %q", got)
	}
}

func TestFakeStream_halfFrame切分后字节仍完整(t *testing.T) {
	events := HalfFrameEvents()
	// 自检:剧本必须真的发生了切分
	if events[1].SplitAt <= 0 || events[1].SplitAt >= len(events[1].Data) {
		t.Fatalf("HalfFrameEvents 切分点配置错误: %d / %d", events[1].SplitAt, len(events[1].Data))
	}
	u := StartStreamUpstream(t, events)

	got, err := readAll(t, u.URL())
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	var want strings.Builder
	for _, ev := range events {
		want.WriteString(ev.Data)
	}
	if got != want.String() {
		t.Fatalf("半帧补齐后字节不完整:\n got=%q\nwant=%q", got, want.String())
	}
}

func TestFakeStream_closeAfter客户端读到意外EOF(t *testing.T) {
	u := StartStreamUpstream(t, MidStreamCutEvents())

	_, err := readAll(t, u.URL())
	if err == nil {
		t.Fatal("中途掐断后客户端必须读到错误(非优雅 EOF)")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) && !strings.Contains(err.Error(), "unexpected EOF") {
		t.Fatalf("期望 unexpected EOF,实际: %v", err)
	}
}

func TestFakeStream_慢帧按时到达(t *testing.T) {
	const d = 30 * time.Millisecond
	u := StartStreamUpstream(t, SlowChunkEvents(d))
	start := time.Now()
	if _, err := readAll(t, u.URL()); err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if elapsed := time.Since(start); elapsed < d*2 {
		t.Fatalf("慢帧延时未生效(耗时 %v)", elapsed)
	}
}

func TestFakeJSON_状态与body透传(t *testing.T) {
	u := StartJSONUpstream(t, http.StatusInternalServerError, JSON500Body)
	resp, err := http.Get(u.URL())
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError || string(b) != JSON500Body {
		t.Fatalf("非流式剧本不符: %d %q", resp.StatusCode, b)
	}
}

func TestFakeUpstream_请求记录(t *testing.T) {
	u := StartJSONUpstream(t, http.StatusOK, JSONOKBody)
	req, _ := http.NewRequest("POST", u.URL(), strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer sk-test")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	_ = resp.Body.Close()

	if u.RequestCount() != 1 {
		t.Fatalf("请求计数不符: %d", u.RequestCount())
	}
	if string(u.LastBody()) != `{"model":"m"}` {
		t.Fatalf("body 记录不符: %q", u.LastBody())
	}
	if u.LastHeader("Authorization") != "Bearer sk-test" {
		t.Fatalf("头记录不符: %q", u.LastHeader("Authorization"))
	}
}
