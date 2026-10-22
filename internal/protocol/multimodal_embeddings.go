// 百炼原生多模态向量请求只解析路由字段；Raw 用于透传，仅顶层 model 可在出站时替换。
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// ErrInvalidMultimodalEmbeddingRequest 表示原生多模态向量请求结构不合法。
var ErrInvalidMultimodalEmbeddingRequest = errors.New("protocol: 非法多模态向量请求")

// MultimodalEmbeddingRequest 保留请求原文，仅提供 model 与 contents 路由校验所需字段。
type MultimodalEmbeddingRequest struct {
	Model    string            `json:"model"`
	Contents []json.RawMessage `json:"-"`
	Raw      []byte            `json:"-"`
}

// ParseMultimodalEmbeddingRequest 校验 model 与 input.contents，其余原生字段不解析也不丢弃。
func ParseMultimodalEmbeddingRequest(body []byte) (*MultimodalEmbeddingRequest, error) {
	var req struct {
		Model string `json:"model"`
		Input struct {
			Contents json.RawMessage `json:"contents"`
		} `json:"input"`
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&req); err != nil {
		return nil, errors.Join(ErrInvalidMultimodalEmbeddingRequest, err)
	}
	if req.Model == "" || strings.TrimSpace(req.Model) != req.Model {
		return nil, errors.Join(ErrInvalidMultimodalEmbeddingRequest, errors.New("model 必须为非空且无首尾空白字符串"))
	}
	var contents []json.RawMessage
	if len(req.Input.Contents) == 0 || string(req.Input.Contents) == "null" ||
		json.Unmarshal(req.Input.Contents, &contents) != nil || len(contents) == 0 {
		return nil, errors.Join(ErrInvalidMultimodalEmbeddingRequest, errors.New("input.contents 必须为非空数组"))
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return nil, errors.Join(ErrInvalidMultimodalEmbeddingRequest, errors.New("请求体包含多个 JSON 值"))
	}
	return &MultimodalEmbeddingRequest{
		Model: req.Model, Contents: contents, Raw: append([]byte(nil), body...),
	}, nil
}
