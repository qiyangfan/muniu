// embeddings 入站 DTO(T4.3):OpenAI /v1/embeddings 兼容。
// 只解析路由所需的 model 与 input 存在性;Raw 完整持有(改写仅换 model,零字段丢失)。
package protocol

import (
	"encoding/json"
	"errors"
)

// ErrInvalidEmbeddingRequest embeddings 入站不合法哨兵。
var ErrInvalidEmbeddingRequest = errors.New("protocol: 非法 embeddings 请求")

// EmbeddingRequest 入站 embeddings 请求。
type EmbeddingRequest struct {
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
	Raw   json.RawMessage `json:"-"`
}

// ParseEmbeddingRequest 解析校验:model 非空、input 存在(字符串或数组均接受)。
func ParseEmbeddingRequest(body []byte) (*EmbeddingRequest, error) {
	var req EmbeddingRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, errors.Join(ErrInvalidEmbeddingRequest, err)
	}
	if req.Model == "" {
		return nil, errors.Join(ErrInvalidEmbeddingRequest, errors.New("model 不能为空"))
	}
	if len(req.Input) == 0 || string(req.Input) == "null" {
		return nil, errors.Join(ErrInvalidEmbeddingRequest, errors.New("input 不能为空"))
	}
	req.Raw = append(json.RawMessage(nil), body...)
	return &req, nil
}
