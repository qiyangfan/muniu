// Responses 入站 handler(responses-api-inbound,ADR-0012):
// POST /v1/responses 边缘翻译为规范 chat 请求后委托既有管道;
// 非流式响应整体改写为 Responses 对象,流式经 SSE 编码器映射为最小事件集。
// 翻译失败(危险字段/不支持项)在进入管道前 fail-fast,不触达上游。
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

// HandlerResponses POST /v1/responses(须挂全链:auth → quota → ratelimit → 本 handler → chat 管道)。
func (p *Proxy) HandlerResponses() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInboundBody))
		if err != nil {
			p.writeError(w, r, protocol.TypeBadRequest, "bad_request", "请求体过大或不可读")
			return
		}
		req, terr := protocol.TranslateResponsesRequest(body)
		if terr != nil {
			var ue *protocol.ResponsesUnsupportedError
			if errors.As(terr, &ue) {
				p.writeError(w, r, protocol.TypeBadRequest, ue.Code, responsesRejectMessage(ue.Code))
				return
			}
			p.writeError(w, r, protocol.TypeBadRequest, "input_invalid", "请求不符合 OpenAI Responses 格式")
			return
		}
		// 用 canonical chat 字节替换入站体:下游(治理 Raw 层/缓存指纹/适配器透传)与等价 chat 完全同构
		r2 := r.Clone(r.Context())
		r2.Body = io.NopCloser(bytes.NewReader(req.Raw))
		r2.ContentLength = int64(len(req.Raw))
		r2.Header.Set("Content-Length", strconv.Itoa(len(req.Raw)))

		respID := "resp_" + logging.RequestIDFrom(r.Context())
		if req.Stream {
			sw := &responsesStreamWriter{ResponseWriter: w, enc: protocol.NewResponsesStreamEncoder(respID, req.Model)}
			defer sw.finish()
			p.handleChat(sw, r2)
			return
		}
		nw := &responsesNonStreamWriter{ResponseWriter: w, respID: respID}
		defer nw.flush()
		p.handleChat(nw, r2)
	})
}

// HandlerResponsesStateless GET /v1/responses/{id}:恒 404(无状态网关不存储 response)。
func (p *Proxy) HandlerResponsesStateless() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(protocol.StatusForType(protocol.TypeNotFound))
		_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
			protocol.TypeNotFound, "response_not_found", "无状态网关不存储 response", logging.RequestIDFrom(r.Context())))
	})
}

func responsesRejectMessage(code string) string {
	switch code {
	case protocol.CodePreviousResponseIDUnsupported:
		return "无状态网关不支持 previous_response_id(服务端不存储会话),请自行回传完整上下文"
	case protocol.CodeBackgroundUnsupported:
		return "不支持 background 后台模式"
	case protocol.CodeInputItemUnsupported:
		return "v1 仅支持文本与函数调用项(不支持 input_image 等多模态输入)"
	default:
		return "请求不符合 OpenAI Responses 格式"
	}
}

// ---- 非流式:缓冲 → 200 成功体改写为 Responses 对象;错误信封原样透传 ----

type responsesNonStreamWriter struct {
	http.ResponseWriter
	respID  string
	status  int
	body    bytes.Buffer
	flushed bool
}

func (w *responsesNonStreamWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
	w.flushed = true
}

func (w *responsesNonStreamWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
		w.ResponseWriter.WriteHeader(http.StatusOK)
	}
	if w.status != http.StatusOK {
		return w.ResponseWriter.Write(b) // 错误信封直写,不缓冲
	}
	return w.body.Write(b)
}

// flush 委托返回后执行:200 成功体改写为 Responses 对象;解析失败宁原样透传。
func (w *responsesNonStreamWriter) flush() {
	if w.status != http.StatusOK {
		return // 错误信封已在 Write 直写
	}
	out := w.body.Bytes()
	if rewritten, err := protocol.ResponsesObjectFromChat(out, w.respID); err == nil {
		out = rewritten
	}
	_, _ = w.ResponseWriter.Write(out)
}

// ---- 流式:SSE 帧翻译为 Responses 事件([DONE] → completed;流中错误帧 → failed) ----

type responsesStreamWriter struct {
	http.ResponseWriter
	enc      *protocol.ResponsesStreamEncoder
	created  bool
	finished bool
	tail     []byte // 半帧残留(SSE 以 \n\n 分隔,Write 边界不保证对齐)
}

func (w *responsesStreamWriter) Write(b []byte) (int, error) {
	if w.finished {
		return len(b), nil // completed 已写出:吞掉后续([DONE] 等)
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

// feedFrame 处理一帧完整 SSE data:OpenAI chunk → Responses 事件;[DONE] → completed。
func (w *responsesStreamWriter) feedFrame(line string) error {
	if line == "" {
		return nil
	}
	if line == "[DONE]" {
		if !w.created { // 空内容流(无任何内容帧):created 首帧不变式仍须成立
			w.emit(appendSSE(w.enc.Init()))
			w.created = true
		}
		w.emit(appendSSE(w.enc.Complete()))
		w.finished = true
		return nil
	}
	// 流中错误帧:诚实失败,不伪造 completed
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
		if !w.created { // 防御对称:错误帧先于任何内容帧时仍保证 created 首帧
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

// finish 委托返回后兜底(域B 审查 high#1):
// ①管道在流开始前失败(writeError 单行 JSON 信封,无 \n\n 分隔)→ tail 残留即错误信封,
//
//	原样冲刷给客户端(状态码已由 WriteHeader 直写,正确);
//
// ②流已开始但无 [DONE] 收尾(上游断流)→ response.failed,不伪造 completed。
func (w *responsesStreamWriter) finish() {
	if w.finished {
		return
	}
	if !w.created {
		if len(w.tail) > 0 {
			_, _ = w.ResponseWriter.Write(w.tail)
			w.tail = nil
		}
		return
	}
	w.emit(appendSSE(w.enc.Failed("上游流中断,已生成部分可能不完整")))
	w.finished = true
}

func (w *responsesStreamWriter) emit(frame []byte) {
	_, _ = w.ResponseWriter.Write(frame)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// appendSSE 事件对象 → SSE 帧(data: 前缀 + 空行)。
func appendSSE(event []byte) []byte {
	out := make([]byte, 0, len(event)+8)
	out = append(out, "data: "...)
	out = append(out, event...)
	out = append(out, '\n', '\n')
	return out
}

// Flush 穿透 http.Flusher 探测(trackWriter/逐事件冲刷契约依赖)。
func (w *responsesStreamWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Flush 穿透(非流式场景无人调用,保持接口完整)。
func (w *responsesNonStreamWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
