// Gemini 原生入站 handler：
// POST /v1beta/models/{model}:generateContent 与 :streamGenerateContent?alt=sse，
// 边缘翻译为规范 chat 请求后委托既有管道；非流式缓冲后改写为 GenerateContentResponse，
// 流式经 SSE 编码器映射为增量帧+终帧；翻译失败在进入管道前 fail-fast，返回 Gemini 错误信封；
// 路径别名不存在与 chat 同规，信封为 Gemini 形态。直通：原始体经 context 传入管道，候选为
// gemini 适配器时出站用客户端原始字节（模型在 URL），writer 切直通模式恒等透传，零反向映射；
// 流式语义在端点而非请求体，故直通出站 URL 由 NativeRequest.Stream 决定。
// 惰性拒绝：fileData/音视频等翻译失格部件在别名含直通候选时放行直通，翻译路径实际被需要时
// 升格既有 400。鉴权适配在装配层 server.AdaptGeminiKey：x-goog-api-key 头或 ?key= 查询参数
// 映射为 Bearer，?key= 读后剥离。
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
// 方法后缀解析见 geminiMethodOf，双形态翻译与惰性拒绝分流见 geminiInbound。
func (p *Proxy) HandlerGemini() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathModel, stream, ok := geminiMethodOf(w, r)
		if !ok {
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxInboundBody))
		if err != nil {
			writeGeminiError(w, http.StatusBadRequest, 400, "请求体过大或不可读", "INVALID_ARGUMENT", "input_invalid")
			return
		}
		r2, ok := p.geminiInbound(w, r, pathModel, body, stream)
		if !ok {
			return
		}
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

// geminiMethodOf 解析 {model}:{method}：generateContent 与非 SSE 的 streamGenerateContent 非法形态
// 即写 Gemini 信封并返回 ok=false。
func geminiMethodOf(w http.ResponseWriter, r *http.Request) (model string, stream, ok bool) {
	seg := r.PathValue("modelMethod")
	idx := strings.LastIndex(seg, ":")
	if idx <= 0 {
		writeGeminiError(w, http.StatusNotFound, 404,
			"模型路径非法(须为 /v1beta/models/{model}:generateContent)",
			"NOT_FOUND", "model_not_found")
		return "", false, false
	}
	pathModel, method := seg[:idx], seg[idx+1:]
	switch method {
	case "generateContent":
		return pathModel, false, true
	case "streamGenerateContent":
		if r.URL.Query().Get("alt") != "sse" {
			writeGeminiError(w, http.StatusBadRequest, 400,
				"流式仅支持 alt=sse(array-form 不支持,主流 SDK 均用 alt=sse)", "INVALID_ARGUMENT", protocol.CodeGeminiStreamFormUnsupported)
			return "", false, false
		}
		return pathModel, true, true
	default:
		writeGeminiError(w, http.StatusNotFound, 404, "未知方法: "+method, "NOT_FOUND", "input_invalid")
		return "", false, false
	}
}

// geminiInbound 双形态翻译与惰性拒绝分流，产出委托管道的入站请求；ok=false 表示已写错误信封。
// canonical chat 字节替换入站体：治理 Raw 层、缓存指纹、适配器透传等下游与等价 chat 完全同构；
// 失格请求无 canonical，原始体直通。lane 标记供亲和范围/oauth 收窄判定消费。
func (p *Proxy) geminiInbound(w http.ResponseWriter, r *http.Request,
	pathModel string, body []byte, stream bool) (*http.Request, bool) {
	res := protocol.TranslateGeminiRequestEx(pathModel, body, stream)
	if res.Fatal != nil {
		writeGeminiUnsupported(w, res.Fatal)
		return nil, false
	}
	// 惰性拒绝别名级预判：无直通资格候选的别名即时 400，时序与形态保持一致
	if res.Ineligible != nil && !p.aliasNative(protocol.InboundGemini, pathModel) {
		writeGeminiUnsupported(w, res.Ineligible)
		return nil, false
	}
	nat := &inboundNative{proto: protocol.InboundGemini, raw: body, model: pathModel, stream: stream}
	if res.Ineligible != nil {
		var ge *protocol.GeminiUnsupportedError
		if errors.As(res.Ineligible, &ge) {
			nat.ineligCode, nat.ineligMsg = ge.Code, geminiRejectMessage(ge.Code)
		}
	}
	payload := body
	if res.Chat != nil {
		payload = res.Chat.Raw
	}
	r2 := withInbound(r.Clone(r.Context()), nat)
	r2.Body = io.NopCloser(bytes.NewReader(payload))
	r2.ContentLength = int64(len(payload))
	r2.Header.Set("Content-Length", strconv.Itoa(len(payload)))
	return withLane(r2, protocol.InboundGemini), true
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

// writeGeminiUnsupported 翻译期拒绝与惰性拒绝共用同一信封形态。
func writeGeminiUnsupported(w http.ResponseWriter, err error) {
	var ge *protocol.GeminiUnsupportedError
	if errors.As(err, &ge) {
		writeGeminiError(w, http.StatusBadRequest, 400, geminiRejectMessage(ge.Code), ge.Status, ge.Code)
		return
	}
	writeGeminiError(w, http.StatusBadRequest, 400,
		"请求不符合 Gemini generateContent 格式",
		"INVALID_ARGUMENT", protocol.CodeGeminiInputInvalid)
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

// ---- 非流式：翻译路径缓冲后改写为 GenerateContentResponse 并把错误信封改写为 Gemini 形态；
// 直通模式位恒等直写，零缓冲零改写 ----

type geminiNonStreamWriter struct {
	http.ResponseWriter
	modelVersion string
	status       int
	body         bytes.Buffer
	flushed      bool
	native       bool // 直通模式位，setNativeMode 仅首字节提交前生效
}

// setNativeMode 直通模式位：首字节提交前唯一切换点，已提交绝不切换。
func (w *geminiNonStreamWriter) setNativeMode(n bool) {
	if w.flushed {
		return
	}
	w.native = n
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
	if w.native {
		return w.ResponseWriter.Write(b) // 直通恒等写：成功体、上游错误信封与升格 400 均不缓冲
	}
	return w.body.Write(b) // 200 成功体与错误信封都缓冲，flush 统一改写为 Gemini 形态
}

// flush 在委托返回后执行：翻译路径 200 成功体改写为 GenerateContentResponse，错误信封改写为
// Gemini 形态；直通已恒等直写，无缓冲。
func (w *geminiNonStreamWriter) flush() {
	if w.native {
		return
	}
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

// ---- 流式：翻译路径把 SSE 帧映射为 Gemini 流帧、增量收束为终帧；直通模式位恒等透传上游
// 原字节，不合成终帧、不改写错误信封 ----

type geminiStreamWriter struct {
	http.ResponseWriter
	enc      *protocol.GeminiStreamEncoder
	finished bool
	native   bool   // 直通模式位，setNativeMode 仅首字节提交前生效
	tail     []byte // 半帧残留：SSE 以 \n\n 分隔，Write 边界不保证对齐
}

// setNativeMode 直通模式位：首字节提交前唯一切换点，已提交绝不切换。
func (w *geminiStreamWriter) setNativeMode(n bool) {
	if w.finished {
		return
	}
	w.native = n
}

func (w *geminiStreamWriter) Write(b []byte) (int, error) {
	if w.native {
		// 直通：原字节透传并冲刷，与翻译路径逐事件节奏一致
		n, err := w.ResponseWriter.Write(b)
		if f, ok := w.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
		return n, err
	}
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

// complete 终态：增量收束并写终帧(usage/finishReason)；直通模式零合成。
func (w *geminiStreamWriter) complete() {
	if w.finished || w.native {
		return
	}
	for _, ev := range w.enc.Complete() {
		w.emit(appendSSE(ev))
	}
	w.finished = true
}

// finishClean 无终止标记协议的干净收尾：由 streamFinalizer 在上游 StreamCloseDone 关流时触发，
// 与 [DONE] 同语义，终帧收尾；直通上游已自带终帧，零合成。
func (w *geminiStreamWriter) finishClean() {
	if w.native {
		return
	}
	w.complete()
}

// finish 委托返回后兜底：直通模式零兜底（上游形态已原样转发，不合成任何帧）。
// 管道在流开始前失败时 writeError 写单行 JSON 信封、无 \n\n 分隔，tail 残留即信封，
// 改写为 Gemini error JSON，状态码已由 WriteHeader 直写。
// 流已开始的三态各有既定出口：断流走 writeStreamAbort 错误帧，干净关流走 finishClean 终帧，
// 客户端断连则对端不可达；经此函数时 finished 或 tail 已定型，无第三种合成。
func (w *geminiStreamWriter) finish() {
	if w.native || w.finished || len(w.tail) == 0 {
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
	_, _ = w.ResponseWriter.Write(frame) //nolint:gosec // 帧由适配器按协议编码后透传
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
