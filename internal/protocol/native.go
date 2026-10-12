// 入站协议标签与边缘翻译双形态结果(protocol-native-passthrough):
// canonical 仍是控制面单源(治理/缓存指纹/计量/插件);原始体仅供同构直通保真出站,
// 不携带任何控制面语义。翻译失败二分:Fatal=结构性非法(恒 400,与直通无关);
// Ineligible=不可翻译内容(失格标记,直通候选可服务,翻译路径需要时升格 400)。
package protocol

import (
	"encoding/json"
)

// InboundProtocol 入站协议标签(直通路径判定载体;随请求传入管道)。
type InboundProtocol string

// 入站协议标签值域(chat 无直通增益;messages/responses 具同构直通语义)。
const (
	InboundChat      InboundProtocol = "chat" // 原生 chat:基座本就透传,无直通增益
	InboundMessages  InboundProtocol = "messages"
	InboundResponses InboundProtocol = "responses"
)

// TranslationResult 边缘翻译结果双形态。不变式:Chat != nil ⇔ Fatal == nil ∧ Ineligible == nil。
type TranslationResult struct {
	Chat       *ChatRequest    // canonical 控制面单源;失格时 nil
	Raw        []byte          // 客户端原始请求体(逐字节引用,零改写)
	Proto      InboundProtocol // 入站协议标签
	Model      string          // 路由别名(失格时经轻量解析仍可得)
	Stream     bool
	PrevID     string // responses 专用:previous_response_id(有状态粘性路由载体;翻译路径拒绝语义由 legacy wrapper 维持)
	Fatal      error  // 结构性非法:恒 400(危险清单/非法结构,行为与直通无关)
	Ineligible error  // 失格标记:翻译不可服务,直通可服务;换道落翻译候选时升格 400
}

// TranslateMessagesRequestEx Anthropic Messages 入站双形态翻译。
func TranslateMessagesRequestEx(body []byte) *TranslationResult {
	res := &TranslationResult{Raw: body, Proto: InboundMessages}
	req, err := translateMessagesRequest(body)
	switch {
	case err == nil:
		res.Chat, res.Model, res.Stream = req, req.Model, req.Stream
	case isIneligible(err, CodeContentBlockUnsupported):
		res.Ineligible = err
		res.Model, res.Stream = lightRouting(body)
	default:
		res.Fatal = err
	}
	return res
}

// TranslateMessagesRequest 翻译路径契约(失格即时报错):既有调用方与测试零改动。
func TranslateMessagesRequest(body []byte) (*ChatRequest, error) {
	res := TranslateMessagesRequestEx(body)
	if res.Fatal != nil {
		return nil, res.Fatal
	}
	if res.Ineligible != nil {
		return nil, res.Ineligible
	}
	return res.Chat, nil
}

// TranslateResponsesRequestEx Responses 入站双形态翻译。
// previous_response_id 在此仅提取不拒绝:原生 lane 的有状态粘性路由判定在 proxy 路径决策;
// background 维持 Fatal(拒绝清单零改动)。
func TranslateResponsesRequestEx(body []byte) *TranslationResult {
	res := &TranslationResult{Raw: body, Proto: InboundResponses}
	req, prevID, err := translateResponsesRequest(body)
	switch {
	case err == nil:
		res.Chat, res.Model, res.Stream = req, req.Model, req.Stream
		res.PrevID = prevID
	case isIneligible(err, CodeInputItemUnsupported):
		res.Ineligible = err
		res.Model, res.Stream = lightRouting(body)
		res.PrevID = lightPrevID(body)
	default:
		res.Fatal = err
	}
	return res
}

// TranslateResponsesRequest 翻译路径契约:previous_response_id/失格维持即时拒绝,既有调用方零改动。
func TranslateResponsesRequest(body []byte) (*ChatRequest, error) {
	res := TranslateResponsesRequestEx(body)
	if res.Fatal != nil {
		return nil, res.Fatal
	}
	if res.Ineligible != nil {
		return nil, res.Ineligible
	}
	if res.PrevID != "" {
		return nil, &ResponsesUnsupportedError{Code: CodePreviousResponseIDUnsupported}
	}
	return res.Chat, nil
}

// NativeRequest 直通出站入参:客户端原始请求体 + 入站协议标签。
// 适配器按标签选原生端点;Raw 仅允许 model 字段原位手术(RewriteModelInPlace)。
type NativeRequest struct {
	Proto InboundProtocol
	Raw   []byte
}

// isIneligible 机器码级失格判定(unsupported 内容类错误 = 直通可服务)。
func isIneligible(err error, code string) bool {
	switch e := err.(type) {
	case *MessagesUnsupportedError:
		return e.Code == code
	case *ResponsesUnsupportedError:
		return e.Code == code
	default:
		return false
	}
}

// lightRouting 失格请求的轻量路由解析(model/stream;canonical 不可得但路由仍需)。
func lightRouting(body []byte) (model string, stream bool) {
	var probe struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if json.Unmarshal(body, &probe) == nil {
		return probe.Model, probe.Stream
	}
	return "", false
}

// lightPrevID 失格状态请求的轻量 previous_response_id 提取。
func lightPrevID(body []byte) string {
	var probe struct {
		PreviousResponseID string `json:"previous_response_id"`
	}
	if json.Unmarshal(body, &probe) == nil {
		return probe.PreviousResponseID
	}
	return ""
}
