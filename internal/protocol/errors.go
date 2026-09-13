// 错误信封(conventions 对外契约,改格式=契约级):统一出站错误形态与 HTTP 状态映射。
package protocol

// 错误类型常量(信封 error.type 取值,conventions 表)。
const (
	TypeBadRequest    = "bad_request"
	TypeAuthFailed    = "auth_failed"
	TypeQuotaExceeded = "quota_exceeded"
	TypeRateLimited   = "rate_limited"
	TypeNotFound      = "not_found"
	TypeTimeout       = "timeout"
	TypeUpstreamError = "upstream_error"
	TypeInternal      = "internal"
)

// ErrorEnvelope 对外错误信封:唯一合法形态,message 不得裸露顶层。
type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody 信封载荷:用户可读 message(中文优先)+ 机器码 code(snake_case)。
type ErrorBody struct {
	Message   string `json:"message"`
	Type      string `json:"type"`
	Code      string `json:"code"`
	RequestID string `json:"request_id,omitempty"`
}

// NewErrorEnvelope 构造信封;type 取上方 Type* 常量。
func NewErrorEnvelope(typ, code, message, requestID string) *ErrorEnvelope {
	return &ErrorEnvelope{Error: ErrorBody{
		Message:   message,
		Type:      typ,
		Code:      code,
		RequestID: requestID,
	}}
}

// statusByType conventions 错误类型 → HTTP 状态映射表(契约,禁止散落硬编码)。
var statusByType = map[string]int{
	TypeBadRequest:    400,
	TypeAuthFailed:    401,
	TypeQuotaExceeded: 402,
	TypeRateLimited:   429,
	TypeNotFound:      404,
	TypeTimeout:       504,
	TypeUpstreamError: 502,
	TypeInternal:      500,
}

// StatusForType 返回错误类型对应的 HTTP 状态;未知类型按 internal(500)兜底。
func StatusForType(typ string) int {
	if s, ok := statusByType[typ]; ok {
		return s
	}
	return 500
}
