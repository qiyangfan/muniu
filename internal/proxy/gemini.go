// Gemini 原生入站 handler：
// POST /v1beta/models/{model}:generateContent 与 :streamGenerateContent?alt=sse，
// 边缘翻译为规范 chat 请求后委托既有管道；非流式缓冲后改写为 GenerateContentResponse，
// 流式经 SSE 编码器映射为增量帧+终帧；翻译失败在进入管道前 fail-fast，返回 Gemini 错误信封；
// 路径别名不存在与 chat 同规，信封为 Gemini 形态。
// 鉴权适配在装配层 server.AdaptGeminiKey：x-goog-api-key 头或 ?key= 查询参数映射为 Bearer，?key= 读后剥离。
package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/solvela/muniu/internal/protocol"
)

// HandlerGemini Gemini 原生入站，须挂全链：auth → quota → ratelimit → 本 handler → chat 管道。
// 路由段为 {model}:{method} 整段通配，因 net/http mux 通配符不支持段内混合；
// handler 内解析方法后缀 generateContent 或 streamGenerateContent，后者须 alt=sse。
func (p *Proxy) HandlerGemini() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seg := r.PathValue("modelMethod")
		idx := strings.LastIndex(seg, ":")
		if idx <= 0 {
			writeGeminiError(w, http.StatusNotFound, 404, "模型路径非法(须为 /v1beta/models/{model}:generateContent)", "NOT_FOUND", "model_not_found")
			return
		}
		pathModel, method := seg[:idx], seg[idx+1:]
		stream := false
		switch method {
		case "generateContent":
		case "streamGenerateContent":
			if r.URL.Query().Get("alt") != "sse" {
				writeGeminiError(w, http.StatusBadRequest, 400,
					"流式仅支持 alt=sse(array-form 不支持,主流 SDK 均用 alt=sse)", "INVALID_ARGUMENT", protocol.CodeGeminiStreamFormUnsupported)
				return
			}
			stream = true
		default:
			writeGeminiError(w, http.StatusNotFound, 404, "未知方法: "+method, "NOT_FOUND", "input_invalid")
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInboundBody))
		if err != nil {
			writeGeminiError(w, http.StatusBadRequest, 400, "请求体过大或不可读", "INVALID_ARGUMENT", "input_invalid")
			return
		}
		req, terr := protocol.TranslateGeminiRequest(pathModel, body, stream)
		if terr != nil {
			var ge *protocol.GeminiUnsupportedError
			if errors.As(terr, &ge) {
				writeGeminiError(w, http.StatusBadRequest, 400, geminiRejectMessage(ge.Code), ge.Status, ge.Code)
				return
			}
			writeGeminiError(w, http.StatusBadRequest, 400, "请求不符合 Gemini generateContent 格式", "INVALID_ARGUMENT", protocol.CodeGeminiInputInvalid)
			return
		}
		// 用 canonical chat 字节替换入站体：治理 Raw 层、缓存指纹、适配器透传等下游与等价 chat 完全同构
		r2 := r.Clone(r.Context())
		r2.Body = io.NopCloser(bytes.NewReader(req.Raw))
		r2.ContentLength = int64(len(req.Raw))
		r2.Header.Set("Content-Length", strconv.Itoa(len(req.Raw)))
		// lane 身份标记:gemini 委托 chat 管道但不携带原生上下文,不标记则管道内与裸 chat
		// 不可区分(亲和范围/oauth lane 收窄两个判定都以 lane 为准)
		r2 = withLane(r2, protocol.InboundGemini)

		if stream {
			sw := &geminiStreamWriter{ResponseWriter: w, enc: protocol.NewGeminiStreamEncoder(pathModel)}
			defer sw.finish()
			p.handleChat(sw, r2)
			return
		}
		nw := &geminiNonStreamWriter{ResponseWriter: w, modelVersion: pathModel}
		defer nw.flush()
		p.handleChat(nw, r2)
	})
}

func geminiRejectMessage(code string) string {
	switch code {
	case protocol.CodeGeminiContentPartUnsupported:
		return "v1 仅支持 text/inlineData(image)/functionCall/functionResponse 部件(不支持 fileData、音频/视频)"
	case protocol.CodeGeminiStreamFormUnsupported:
		return "流式仅支持 alt=sse"
	default:
		return "请求不符合 Gemini generateContent 格式"
	}
}

// writeGeminiError 写 Gemini 错误信封 {error:{code,message,status}}，SDK 读 error.message 可读；
// muniu_code 为网关机器码扩展字段，在 Gemini 形态之上保留 muniu 追责口径，SDK 容忍附加字段。
func writeGeminiError(w http.ResponseWriter, status, code int, msg, glStatus, muniuCode string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"code": code, "message": msg, "status": glStatus, "muniu_code": muniuCode},
	})
}

// geminiStatusOf 管道错误类型映射为 Gemini status 字面量，供信封改写使用。
func geminiStatusOf(typ string) (int, string) {
	switch typ {
	case protocol.TypeBadRequest:
		return 400, "INVALID_ARGUMENT"
	case protocol.TypeAuthFailed:
		return 401, "UNAUTHENTICATED"
	case protocol.TypeForbidden:
		return 403, "PERMISSION_DENIED" // key 级模型白名单等治理语义，对应 Google API 标准 status
	case protocol.TypeRateLimited:
		return 429, "RESOURCE_EXHAUSTED"
	case protocol.TypeTimeout:
		return 504, "DEADLINE_EXCEEDED"
	case protocol.TypeNotFound:
		return 404, "NOT_FOUND"
	default: // upstream_error / internal
		return 500, "INTERNAL"
	}
}

// ---- 非流式：缓冲后 200 成功体改写为 GenerateContentResponse；错误信封改写为 Gemini 形态 ----

type geminiNonStreamWriter struct {
	http.ResponseWriter
	modelVersion string
	status       int
	body         bytes.Buffer
	flushed      bool
}

func (w *geminiNonStreamWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
	w.flushed = true
}

func (w *geminiNonStreamWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
		w.ResponseWriter.WriteHeader(http.StatusOK)
	}
	return w.body.Write(b) // 200 成功体与错误信封都缓冲，flush 统一改写为 Gemini 形态
}

// flush 在委托返回后执行：200 成功体改写为 GenerateContentResponse，错误信封改写为 Gemini 形态。
func (w *geminiNonStreamWriter) flush() {
	out := w.body.Bytes()
	if w.status == http.StatusOK {
		if rewritten, err := protocol.GenerateContentResponseFromChat(out, w.modelVersion); err == nil {
			out = rewritten
		}
		_, _ = w.ResponseWriter.Write(out)
		return
	}
	// 错误信封 OpenAI 形态改写为 Gemini 形态：SDK error.message 可读，解析失败宁原样透传
	var probe struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(bytes.TrimSpace(out), &probe) == nil && probe.Error.Message != "" {
		code, status := geminiStatusOf(probe.Error.Type)
		writeGeminiErrorRaw(w.ResponseWriter, code, probe.Error.Message, status)
		return
	}
	_, _ = w.ResponseWriter.Write(out)
}

// writeGeminiErrorRaw 已提交状态码后的 body 改写，不可再重复 WriteHeader。
// 必须直写底层 ResponseWriter：经 wrapper 会再次进入 tail 缓冲被丢弃，finish 语义即最后一步。
func writeGeminiErrorRaw(w http.ResponseWriter, code int, msg, glStatus string) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(map[string]any{
		"error": map[string]any{"code": code, "message": msg, "status": glStatus},
	})
	_, _ = w.Write(append(b, '\n'))
}

// ---- 流式：SSE 帧翻译为 Gemini 流帧，增量帧收束为终帧，[DONE] 转终态，流中错误帧转 error 信封 ----

type geminiStreamWriter struct {
	http.ResponseWriter
	enc      *protocol.GeminiStreamEncoder
	finished bool
	tail     []byte // 半帧残留：SSE 以 \n\n 分隔，Write 边界不保证对齐
}

func (w *geminiStreamWriter) Write(b []byte) (int, error) {
	if w.finished {
		return len(b), nil // 终帧已写出，吞掉后续 [DONE] 等
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

// feedFrame 处理一帧完整 SSE data：OpenAI chunk 转 Gemini 帧，[DONE] 转终态，错误信封帧转 error。
func (w *geminiStreamWriter) feedFrame(line string) error {
	if line == "" {
		return nil
	}
	if line == "[DONE]" {
		w.complete()
		return nil
	}
	// 流中错误帧信封：诚实失败，不伪造正常终帧
	var probe struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(line), &probe) == nil && probe.Error != nil {
		msg := probe.Error.Message
		if msg == "" {
			msg = "上游流中断,已生成部分可能不完整"
		}
		w.emit(appendSSE(w.enc.Failed(msg)))
		w.finished = true
		return nil
	}
	for _, ev := range w.enc.Feed([]byte(line)) {
		w.emit(appendSSE(ev))
	}
	return nil
}

// complete 终态：增量收束并写终帧(usage/finishReason)。
func (w *geminiStreamWriter) complete() {
	if w.finished {
		return
	}
	for _, ev := range w.enc.Complete() {
		w.emit(appendSSE(ev))
	}
	w.finished = true
}

// finishClean 无终止标记协议的干净收尾：由 streamFinalizer 在上游 StreamCloseDone 关流时触发，
// 与 [DONE] 同语义，终帧收尾。
func (w *geminiStreamWriter) finishClean() {
	w.complete()
}

// finish 委托返回后兜底：管道在流开始前失败时 writeError 写单行 JSON 信封、无 \n\n 分隔，
// tail 残留即信封，改写为 Gemini error JSON，状态码已由 WriteHeader 直写。
// 流已开始的三态各有既定出口：断流走 writeStreamAbort 错误帧，干净关流走 finishClean 终帧，
// 客户端断连则对端不可达；经此函数时 finished 或 tail 已定型，无第三种合成。
func (w *geminiStreamWriter) finish() {
	if w.finished || len(w.tail) == 0 {
		return
	}
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
		code, status := geminiStatusOf(probe.Error.Type)
		// 状态码已由管道 WriteHeader 写出，不可重复；此处仅改写 body 形态
		writeGeminiErrorRaw(w.ResponseWriter, code, msg, status)
	} else {
		_, _ = w.ResponseWriter.Write(w.tail)
	}
	w.tail = nil
}

func (w *geminiStreamWriter) emit(frame []byte) {
	_, _ = w.ResponseWriter.Write(frame)
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Flush 穿透 http.Flusher 探测，trackWriter 与逐事件冲刷契约依赖此方法。
func (w *geminiStreamWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Flush 穿透，非流式场景无人调用，保持接口完整。
func (w *geminiNonStreamWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
