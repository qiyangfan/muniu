// Responses 入站 handler(responses-api-inbound,ADR-0012;直通扩展 protocol-native-passthrough):
// POST /v1/responses 边缘翻译为规范 chat 请求后委托既有管道;非流式响应整体改写为
// Responses 对象,流式经 SSE 编码器映射为最小事件集(翻译路径)。直通:候选声明
// native_responses 时出站用客户端原始字节(writer 直通模式恒等透传),input_image 等
// 部件原生支持;有状态请求(previous_response_id)走粘性路由(affinity.go)。
// 翻译失败(危险字段)在进入管道前 fail-fast;失格在无直通候选别名上即时 400。
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/solvela/muniu/internal/adapter"
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
		res := protocol.TranslateResponsesRequestEx(body)
		if res.Fatal != nil {
			var ue *protocol.ResponsesUnsupportedError
			if errors.As(res.Fatal, &ue) {
				p.writeError(w, r, protocol.TypeBadRequest, ue.Code, responsesRejectMessage(ue.Code))
				return
			}
			p.writeError(w, r, protocol.TypeBadRequest, "input_invalid", "请求不符合 OpenAI Responses 格式")
			return
		}
		// 惰性拒绝别名级预判:无 native_responses 声明候选的别名维持现状即时 400
		if res.Ineligible != nil && !p.aliasNative(protocol.InboundResponses, res.Model) {
			var ue *protocol.ResponsesUnsupportedError
			code := "input_invalid"
			if errors.As(res.Ineligible, &ue) {
				code = ue.Code
			}
			p.writeError(w, r, protocol.TypeBadRequest, code, responsesRejectMessage(code))
			return
		}
		nat := &inboundNative{proto: protocol.InboundResponses, raw: body, model: res.Model, stream: res.Stream, prevID: res.PrevID}
		if res.Ineligible != nil {
			var ue *protocol.ResponsesUnsupportedError
			if errors.As(res.Ineligible, &ue) {
				nat.ineligCode = ue.Code
				nat.ineligMsg = responsesRejectMessage(ue.Code)
			}
		}
		// canonical chat 字节替换入站体(失格/状态请求无 canonical 或旁路缓存,原始体直通):
		// 下游(治理 Raw 层/缓存指纹/适配器透传)与等价 chat 完全同构
		payload := body
		if res.Chat != nil {
			payload = res.Chat.Raw
		}
		r2 := withInbound(r.Clone(r.Context()), nat)
		r2.Body = io.NopCloser(bytes.NewReader(payload))
		r2.ContentLength = int64(len(payload))
		r2.Header.Set("Content-Length", strconv.Itoa(len(payload)))

		respID := "resp_" + logging.RequestIDFrom(r.Context())
		if res.Stream {
			sw := &responsesStreamWriter{ResponseWriter: w, enc: protocol.NewResponsesStreamEncoder(respID, res.Model)}
			defer sw.finish()
			p.handleChat(sw, r2)
			return
		}
		nw := &responsesNonStreamWriter{ResponseWriter: w, respID: respID}
		defer nw.flush()
		p.handleChat(nw, r2)
	})
}

// HandlerResponsesStateless GET /v1/responses/{id}(直通扩展后按亲和分域):
// 亲和已知 → 代理转发亲和 provider 原样返回(有状态 lane 的状态查询);
// 亲和未知 → 恒 404 自解释(与既有无状态语义逐字节一致,不因曾经代理过而可查)。
func (p *Proxy) HandlerResponsesStateless() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respID := r.PathValue("id")
		if r.Method != http.MethodGet && r.Method != http.MethodDelete {
			p.writeError(w, r, protocol.TypeBadRequest, "bad_request", "状态 lane 仅支持 GET/DELETE")
			return
		}
		provider, ok := p.affinity.Get(respID)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(protocol.StatusForType(protocol.TypeNotFound))
			_ = json.NewEncoder(w).Encode(protocol.NewErrorEnvelope(
				protocol.TypeNotFound, "response_not_found", "无状态网关不存储 response", logging.RequestIDFrom(r.Context())))
			return
		}
		// 代理转发:取 key(域内可用性)→ NativeFetch GET → 原样透传状态/头/体
		snap := p.topo.Load()
		ad, ok := snap.adapters[provider]
		if !ok {
			p.writeError(w, r, protocol.TypeInternal, "adapter_missing", "内部配置错误: 适配器未注册")
			return
		}
		fetcher, ok := ad.(adapter.NativeFetcher)
		if !ok {
			p.writeError(w, r, protocol.TypeInternal, "adapter_missing", "内部配置错误: 适配器不支持状态查询")
			return
		}
		pool := snap.pools[provider]
		key := pool.Pick(time.Now())
		if key == nil {
			p.writeError(w, r, protocol.TypeRateLimited, "all_candidates_failed", "上游供应商暂时不可用(key 池不可用)")
			return
		}
		out, err := fetcher.PrepareNativeFetch(r.Method, respID, key.Value)
		if err != nil {
			p.writeError(w, r, protocol.TypeInternal, "internal", "出站请求构造失败")
			return
		}
		opCtx, cancel := context.WithTimeout(r.Context(), p.timeouts.FirstToken)
		defer cancel()
		upReq, err := http.NewRequestWithContext(opCtx, r.Method, out.URL, nil)
		if err != nil {
			p.writeError(w, r, protocol.TypeInternal, "internal", "出站请求构造失败")
			return
		}
		for k, vs := range out.Header {
			for _, v := range vs {
				upReq.Header.Add(k, v)
			}
		}
		resp, err := p.client.Do(upReq)
		if err != nil {
			p.writeError(w, r, protocol.TypeUpstreamError, "upstream_error", "上游查询失败")
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
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
	native  bool // 直通模式位:恒等写(升格 400/错误透传/直通响应保真)
}

func (w *responsesNonStreamWriter) setNativeMode(n bool) {
	if w.flushed {
		return
	}
	w.native = n
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
	if w.native || w.status != http.StatusOK {
		return w.ResponseWriter.Write(b) // 直通恒等写 / 错误信封直写,不缓冲
	}
	return w.body.Write(b)
}

// flush 委托返回后执行:翻译路径 200 成功体改写为 Responses 对象(直通恒等写无缓冲)。
func (w *responsesNonStreamWriter) flush() {
	if w.native || w.status != http.StatusOK {
		return // 直通已恒等直写 / 错误信封已在 Write 直写
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
	native   bool   // 直通模式位:恒等透传上游原字节(零事件翻译)
	tail     []byte // 半帧残留(SSE 以 \n\n 分隔,Write 边界不保证对齐)
}

func (w *responsesStreamWriter) setNativeMode(n bool) {
	if w.created || w.finished {
		return // 翻译已开闸/已提交:绝不切换形态
	}
	w.native = n
}

func (w *responsesStreamWriter) Write(b []byte) (int, error) {
	if w.native {
		n, err := w.ResponseWriter.Write(b)
		if f, ok := w.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
		return n, err
	}
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
	if w.finished || w.native {
		return // 直通:上游形态已原样转发,不合成任何事件
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
