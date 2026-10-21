// Anthropic Messages 入站 handler：
// POST /v1/messages 边缘翻译为规范 chat 请求后委托既有管道；非流式整体改写为 message
// 对象，流式经 SSE 编码器映射为 message_start/content_block_*/message_delta/message_stop
// 事件序列，此为翻译路径。直通：原始体经 context 传入管道，候选为 anthropic 适配器时
// 出站用客户端原始字节，writer 切直通模式恒等透传，零反向映射，真实 usage 直达。
// 惰性拒绝：翻译失格在别名含直通候选时放行直通，翻译路径实际被需要时升格既有 400。
// 鉴权适配在装配层 server.AdaptAnthropicKey：x-api-key 映射为 Bearer，先于 verifier 生效。
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

// HandlerMessages POST /v1/messages，须挂全链：auth → quota → ratelimit → 本 handler → chat 管道。
func (p *Proxy) HandlerMessages() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInboundBody))
		if err != nil {
			writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", "请求体过大或不可读")
			return
		}
		res := protocol.TranslateMessagesRequestEx(body)
		if res.Fatal != nil {
			var ue *protocol.MessagesUnsupportedError
			if errors.As(res.Fatal, &ue) {
				writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", messagesRejectMessage(ue.Code))
				return
			}
			writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", "请求不符合 Anthropic Messages 格式")
			return
		}
		// 惰性拒绝别名级预判：无直通资格候选的别名即时 400，时序与形态保持一致
		if res.Ineligible != nil && !p.aliasNative(protocol.InboundMessages, res.Model) {
			var ue *protocol.MessagesUnsupportedError
			code := ""
			if errors.As(res.Ineligible, &ue) {
				code = ue.Code
			}
			writeMessagesError(w, http.StatusBadRequest, "invalid_request_error", messagesRejectMessage(code))
			return
		}
		nat := &inboundNative{proto: protocol.InboundMessages, raw: body, model: res.Model, stream: res.Stream}
		if res.Ineligible != nil {
			var ue *protocol.MessagesUnsupportedError
			if errors.As(res.Ineligible, &ue) {
				nat.ineligCode = ue.Code
				nat.ineligMsg = messagesRejectMessage(ue.Code)
			}
		}
		// canonical chat 字节替换入站体，失格请求无 canonical、原始体直通：
		// 治理 Raw 层、缓存指纹、适配器透传等下游与等价 chat 完全同构
		payload := body
		if res.Chat != nil {
			payload = res.Chat.Raw
		}
		r2 := withInbound(r.Clone(r.Context()), nat)
		r2.Body = io.NopCloser(bytes.NewReader(payload))
		r2.ContentLength = int64(len(payload))
		r2.Header.Set("Content-Length", strconv.Itoa(len(payload)))

		msgID := "msg_" + logging.RequestIDFrom(r.Context())
		if res.Stream {
			sw := &messagesStreamWriter{ResponseWriter: w, enc: protocol.NewMessagesStreamEncoder(msgID, res.Model)}
			defer sw.finish()
			p.handleChat(sw, r2)
			return
		}
		nw := &messagesNonStreamWriter{ResponseWriter: w, msgID: msgID, model: res.Model}
		defer nw.flush()
		p.handleChat(nw, r2)
	})
}

// aliasNative 别名级直通资格预判，作为惰性拒绝的即时 400 判据。
func (p *Proxy) aliasNative(proto protocol.InboundProtocol, alias string) bool {
	snap := p.topo.Load()
	cands, ok := snap.routes[alias]
	if !ok {
		return false
	}
	return aliasHasNative(snap, proto, cands)
}

func messagesRejectMessage(code string) string {
	switch code {
	case protocol.CodeContentBlockUnsupported:
		return "v1 仅支持 text/tool_use/tool_result 内容块(不支持 image 等多模态输入)"
	default:
		return "请求不符合 Anthropic Messages 格式"
	}
}

// writeMessagesError 写 Anthropic 错误形态，翻译期失败专用。
func writeMessagesError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": typ, "message": msg},
	})
}

// ---- 非流式：缓冲后 200 成功体改写为 message 对象；错误信封原样透传 ----
// 直通模式位：恒等写，零缓冲零改写，升格 400、错误透传、直通响应均经此保真。

type messagesNonStreamWriter struct {
	http.ResponseWriter
	msgID   string
	model   string
	status  int
	body    bytes.Buffer
	flushed bool
	native  bool
}

func (w *messagesNonStreamWriter) setNativeMode(n bool) {
	if w.flushed {
		return // 已提交绝不切换，既有不变式
	}
	w.native = n
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
	if w.native || w.status != http.StatusOK {
		return w.ResponseWriter.Write(b) // 直通恒等写或错误信封直写，不缓冲
	}
	return w.body.Write(b)
}

// flush 在委托返回后执行：翻译路径 200 成功体改写为 message 对象，直通恒等写无缓冲。
func (w *messagesNonStreamWriter) flush() {
	if w.native || w.status != http.StatusOK {
		return // 直通已恒等直写，错误信封已在 Write 直写
	}
	out := w.body.Bytes()
	if rewritten, err := protocol.MessageFromChat(out, w.msgID, w.model); err == nil {
		out = rewritten
	}
	_, _ = w.ResponseWriter.Write(out)
}

// ---- 流式：SSE 帧翻译为 Anthropic 事件，[DONE] 转 message_delta+message_stop，流中错误帧转 error ----
// 直通模式位：恒等透传上游原字节，含真实 message_start usage 与上游错误 JSON，零反向映射。

type messagesStreamWriter struct {
	http.ResponseWriter
	enc      *protocol.MessagesStreamEncoder
	created  bool
	finished bool
	native   bool   // 直通模式位，setNativeMode 仅首字节提交前生效
	tail     []byte // 半帧残留：SSE 以 \n\n 分隔，Write 边界不保证对齐
}

func (w *messagesStreamWriter) setNativeMode(n bool) {
	if w.created || w.finished {
		return // 翻译已开闸或已提交，绝不切换形态
	}
	w.native = n
}

func (w *messagesStreamWriter) Write(b []byte) (int, error) {
	if w.native {
		// 直通：原字节透传并冲刷，与翻译路径逐事件节奏一致
		n, err := w.ResponseWriter.Write(b)
		if f, ok := w.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
		return n, err
	}
	if w.finished {
		return len(b), nil // message_stop 已写出，吞掉后续 [DONE] 等
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

// feedFrame 处理一帧完整 SSE data：OpenAI chunk 转 Anthropic 事件，[DONE] 转终态两连发。
func (w *messagesStreamWriter) feedFrame(line string) error {
	if line == "" {
		return nil
	}
	if line == "[DONE]" {
		if !w.created { // 空内容流：首帧不变式仍须成立
			w.emit(appendSSE(w.enc.Init()))
			w.created = true
		}
		for _, ev := range w.enc.Complete() {
			w.emit(appendSSE(ev))
		}
		w.finished = true
		return nil
	}
	// 流中错误帧：诚实失败，不伪造 message_stop
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
		if !w.created { // 防御对称：错误帧先于任何内容帧时仍保证首帧
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

// finishClean 无终止标记协议的干净收尾：由 streamFinalizer 在上游 StreamCloseDone 关流时触发，
// 与 [DONE] 同语义，终态两连发后吞掉后续。
func (w *messagesStreamWriter) finishClean() {
	if w.finished {
		return
	}
	if !w.created { // 空内容流：首帧不变式仍须成立
		w.emit(appendSSE(w.enc.Init()))
		w.created = true
	}
	for _, ev := range w.enc.Complete() {
		w.emit(appendSSE(ev))
	}
	w.finished = true
}

// finish 委托返回后兜底，直通模式零兜底：上游形态已原样转发，不合成任何事件。
// ①管道在流开始前失败：writeError 写单行 JSON 信封、无 \n\n 分隔，tail 残留即信封，
// 改写为 Anthropic error JSON，解析失败宁原样透传，状态码已由 WriteHeader 直写。
// ②流已开始但上游断流无 [DONE] 收尾：发 error 事件，不伪造 message_stop。
func (w *messagesStreamWriter) finish() {
	if w.finished || w.native {
		return
	}
	if !w.created {
		if len(w.tail) > 0 {
			var probe struct {
				Error *struct {
					Message string `json:"message"`
					Type    string `json:"type"`
				} `json:"error"`
			}
			if json.Unmarshal(bytes.TrimSpace(w.tail), &probe) == nil && probe.Error != nil {
				msg := probe.Error.Message
				if msg == "" {
					msg = "上游处理失败"
				}
				// 管道错误类型映射为 Anthropic 原生类型，词汇表与翻译期 writeMessagesError 一致；
				// 无对应物的类型维持 api_error 兜底，语义由 HTTP 状态码承载
				typ := messagesPipelineErrorTypeOf(probe.Error.Type)
				// 状态码已由管道 WriteHeader 写出，不可重复，此处仅改写 body 形态。
				// 必须直写底层 ResponseWriter：经 wrapper 会再次进 tail 被丢弃，finish 是最后一步
				w.Header().Set("Content-Type", "application/json")
				b, _ := json.Marshal(map[string]any{
					"type":  "error",
					"error": map[string]any{"type": typ, "message": msg},
				})
				_, _ = w.ResponseWriter.Write(append(b, '\n'))
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

// Flush 穿透 http.Flusher 探测，trackWriter 与逐事件冲刷契约依赖此方法。
func (w *messagesStreamWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Flush 穿透，非流式场景无人调用，保持接口完整。
func (w *messagesNonStreamWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// messagesPipelineErrorTypeOf 管道错误类型映射为 Anthropic 原生错误类型，供流式 finish 改写使用。
// 词汇表与翻译期 writeMessagesError 一致：同一协议路径只有一套错误形态。
// 有官方对应物的五类精确映射；quota_exceeded/timeout/upstream_error/internal 及未知类型
// 维持 api_error 兜底，语义由 HTTP 状态码承载，不发明 Anthropic 不存在的类型。
func messagesPipelineErrorTypeOf(typ string) string {
	switch typ {
	case protocol.TypeBadRequest:
		return "invalid_request_error"
	case protocol.TypeAuthFailed:
		return "authentication_error"
	case protocol.TypeForbidden:
		return "permission_error"
	case protocol.TypeNotFound:
		return "not_found_error"
	case protocol.TypeRateLimited:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}
