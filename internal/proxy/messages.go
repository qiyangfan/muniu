// Anthropic Messages 入站 handler(ADR-0013):
// POST /v1/messages 边缘翻译为规范 chat 请求后委托既有管道;非流式整体改写为 message
// 对象,流式经 SSE 编码器映射为 message_start/content_block_*/message_delta/message_stop
// 事件序列。翻译失败在进入管道前 fail-fast,不触达上游。
// 鉴权适配(x-api-key → Bearer)在装配层(server.AdaptAnthropicKey),先于 verifier 生效。
package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/protocol"
)

// HandlerMessages POST /v1/messages(须挂全链:auth → quota → ratelimit → 本 handler → chat 管道)。
func (p *Proxy) HandlerMessages() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInboundBody))
		if err != nil {
			writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", "请求体过大或不可读")
			return
		}
		req, terr := protocol.TranslateMessagesRequest(body)
		if terr != nil {
			var ue *protocol.MessagesUnsupportedError
			if errors.As(terr, &ue) {
				writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", messagesRejectMessage(ue.Code))
				return
			}
			writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", "请求不符合 Anthropic Messages 格式")
			return
		}
		// 用 canonical chat 字节替换入站体:下游(治理 Raw 层/缓存指纹/适配器透传)与等价 chat 完全同构
		r2 := r.Clone(r.Context())
		r2.Body = io.NopCloser(bytes.NewReader(req.Raw))
		r2.ContentLength = int64(len(req.Raw))
		r2.Header.Set("Content-Length", strconv.Itoa(len(req.Raw)))

		msgID := "msg_" + logging.RequestIDFrom(r.Context())
		if req.Stream {
			sw := &messagesStreamWriter{ResponseWriter: w, enc: protocol.NewMessagesStreamEncoder(msgID, req.Model)}
			defer sw.finish()
			p.handleChat(sw, r2)
			return
		}
		nw := &messagesNonStreamWriter{ResponseWriter: w, msgID: msgID, model: req.Model}
		defer nw.flush()
		p.handleChat(nw, r2)
	})
}

func messagesRejectMessage(code string) string {
	switch code {
	case protocol.CodeContentBlockUnsupported:
		return "v1 仅支持 text/tool_use/tool_result 内容块(不支持 image 等多模态输入)"
	default:
		return "请求不符合 Anthropic Messages 格式"
	}
}

// writeMessagesError Anthropic 错误形态(翻译期失败专用;管道内错误的信封形态见设计文档登记)。
func writeMessagesError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": typ, "message": msg},
	})
}

// ---- 非流式:缓冲 → 200 成功体改写为 message 对象;错误信封原样透传 ----

type messagesNonStreamWriter struct {
	http.ResponseWriter
	msgID   string
	model   string
	status  int
	body    bytes.Buffer
	flushed bool
}

func (w *messagesNonStreamWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
	w.flushed = true
}

func (w *messagesNonStreamWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
		w.ResponseWriter.WriteHeader(http.StatusOK)
	}
	if w.status != http.StatusOK {
		return w.ResponseWriter.Write(b) // 错误信封直写,不缓冲
	}
	return w.body.Write(b)
}

// flush 委托返回后执行:200 成功体改写为 message 对象;解析失败宁原样透传。
func (w *messagesNonStreamWriter) flush() {
	if w.status != http.StatusOK {
		return // 错误信封已在 Write 直写
	}
	out := w.body.Bytes()
	if rewritten, err := protocol.MessageFromChat(out, w.msgID, w.model); err == nil {
		out = rewritten
	}
	_, _ = w.ResponseWriter.Write(out)
}

// ---- 流式:SSE 帧翻译为 Anthropic 事件([DONE] → message_delta+message_stop;流中错误帧 → error) ----

type messagesStreamWriter struct {
	http.ResponseWriter
	enc      *protocol.MessagesStreamEncoder
	created  bool
	finished bool
	tail     []byte // 半帧残留(SSE 以 \n\n 分隔,Write 边界不保证对齐)
}

func (w *messagesStreamWriter) Write(b []byte) (int, error) {
	if w.finished {
		return len(b), nil // message_stop 已写出:吞掉后续([DONE] 等)
	}
	w.tail = append(w.tail, b...)
	for {
		idx := bytes.Index(w.tail, []byte("\n\n"))
		if idx < 0 {
			break
		}
		line := strings.TrimPrefix(string(w.tail[:idx]), "data: ")
		w.tail = w.tail[idx+2:]
		if err := w.feedFrame(strings.TrimSpace(line)); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}

// feedFrame 处理一帧完整 SSE data:OpenAI chunk → Anthropic 事件;[DONE] → 终态两连发。
func (w *messagesStreamWriter) feedFrame(line string) error {
	if line == "" {
		return nil
	}
	if line == "[DONE]" {
		if !w.created { // 空内容流:首帧不变式仍须成立
			w.emit(appendSSE(w.enc.Init()))
			w.created = true
		}
		for _, ev := range w.enc.Complete() {
			w.emit(appendSSE(ev))
		}
		w.finished = true
		return nil
	}
	// 流中错误帧:诚实失败,不伪造 message_stop
	var probe struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(line), &probe) == nil && probe.Error != nil {
		msg := probe.Error.Message
		if msg == "" {
			msg = "上游流中断,已生成部分可能不完整"
		}
		if !w.created { // 防御对称:错误帧先于任何内容帧时仍保证首帧
			w.emit(appendSSE(w.enc.Init()))
			w.created = true
		}
		w.emit(appendSSE(w.enc.Failed(msg)))
		w.finished = true
		return nil
	}
	if !w.created {
		w.emit(appendSSE(w.enc.Init()))
		w.created = true
	}
	for _, ev := range w.enc.Feed([]byte(line)) {
		w.emit(appendSSE(ev))
	}
	return nil
}

// finish 委托返回后兜底:
// ①管道在流开始前失败(writeError 单行 JSON 信封,无 \n\n 分隔)→ tail 残留即信封,
// 改写为 Anthropic error JSON(解析失败宁原样透传;状态码已由 WriteHeader 直写);
// ②流已开始但无 [DONE] 收尾(上游断流)→ error 事件,不伪造 message_stop。
func (w *messagesStreamWriter) finish() {
	if w.finished {
		return
	}
	if !w.created {
		if len(w.tail) > 0 {
			var probe struct {
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(bytes.TrimSpace(w.tail), &probe) == nil && probe.Error != nil {
				msg := probe.Error.Message
				if msg == "" {
					msg = "上游处理失败"
				}
				// 状态码已由管道 WriteHeader 写出(不可重复),此处仅改写 body 形态
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"type":  "error",
					"error": map[string]any{"type": "api_error", "message": msg},
				})
			} else {
				_, _ = w.ResponseWriter.Write(w.tail)
			}
			w.tail = nil
		}
		return
	}
	w.emit(appendSSE(w.enc.Failed("上游流中断,已生成部分可能不完整")))
	w.finished = true
}

func (w *messagesStreamWriter) emit(frame []byte) {
	_, _ = w.ResponseWriter.Write(frame)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Flush 穿透 http.Flusher 探测(trackWriter/逐事件冲刷契约依赖)。
func (w *messagesStreamWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Flush 穿透(非流式场景无人调用,保持接口完整)。
func (w *messagesNonStreamWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
