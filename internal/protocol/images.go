package protocol

import (
	"encoding/json"
	"errors"
	"strings"
)

// ImageRequest 仅解析路由与计价字段，未知参数完整保留供上游消费。
type ImageRequest struct {
	Model   string          `json:"model"`
	Prompt  string          `json:"prompt"`
	N       int64           `json:"n"`
	Size    string          `json:"size"`
	Quality string          `json:"quality"`
	Raw     json.RawMessage `json:"-"`
}

// ParseImageRequest 缺省张数为一；显式非法张数拒绝，避免转发与计价口径分离。
func ParseImageRequest(body []byte) (*ImageRequest, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	r := ImageRequest{N: 1}
	// JSON 字段名区分大小写；未知参数仍透传，不能进入本地路由或计价。
	for _, field := range []struct {
		name   string
		target any
	}{{"model", &r.Model}, {"prompt", &r.Prompt}, {"n", &r.N}, {"size", &r.Size}, {"quality", &r.Quality}} {
		if raw, ok := fields[field.name]; ok {
			if err := json.Unmarshal(raw, field.target); err != nil {
				return nil, err
			}
		}
	}
	if strings.TrimSpace(r.Model) == "" || strings.TrimSpace(r.Prompt) == "" || r.N <= 0 {
		return nil, errors.New("images: model/prompt 必填，n 须为正整数")
	}
	if string(fields["n"]) == "null" {
		return nil, errors.New("images: n 不可为 null")
	}
	r.Raw = append(json.RawMessage(nil), body...)
	return &r, nil
}

// TierKey 不猜测厂商缺省值；缺省字段自然回落兜底价。
func (r *ImageRequest) TierKey() string { return r.Size + "|" + r.Quality }
