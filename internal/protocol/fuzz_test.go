package protocol

import (
	"bytes"
	"errors"
	"testing"
)

// 入站请求解析是外部输入的边界:除零 panic,还要守住「解析成功即可安全消费」的不变量,
// 否则校验一旦放宽,下游会拿到半合法 DTO。种子覆盖合法/缺字段/非 JSON 三类形态。
func FuzzParseChatRequest(f *testing.F) {
	f.Add([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	f.Add([]byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`))
	f.Add([]byte(`{"model":"","messages":[]}`))
	f.Add([]byte(`{"messages":[{"role":"user"}]}`))
	f.Add([]byte(`not json`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req, err := ParseChatRequest(body)
		if err != nil {
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("拒绝原因未归类为 ErrInvalidRequest: %v", err)
			}
			if req != nil {
				t.Fatalf("错误路径返回了非 nil 请求")
			}
			return
		}
		if req.Model == "" || len(req.Messages) == 0 {
			t.Fatalf("解析成功但缺必填字段: model=%q messages=%d", req.Model, len(req.Messages))
		}
		for i, m := range req.Messages {
			if m.Role == "" {
				t.Fatalf("messages[%d] 缺 role 仍被接受", i)
			}
		}
		if !bytes.Equal(req.Raw, body) {
			t.Fatalf("Raw 未原样保留请求体")
		}
	})
}

// 流式帧解析不能因畸形 data 帧 panic;解析成功时 HasContent/IsUsageOnly 必须可安全求值
// (首 token 判定与 usage 终帧判定都在转发热路径上)。
func FuzzParseStreamChunk(f *testing.F) {
	f.Add([]byte(`{"choices":[{"delta":{"content":"hi"}}]}`))
	f.Add([]byte(`{"choices":[{"delta":{"reasoning_content":"think"}}]}`))
	f.Add([]byte(`{"choices":[{"delta":{"tool_calls":[{"id":"x"}]}}]}`))
	f.Add([]byte(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2}}`))
	f.Add([]byte(`{"choices":[{"delta":{},"index":0,"finish_reason":"stop"}]}`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := ParseStreamChunk(data)
		if err != nil {
			if c != nil {
				t.Fatalf("错误路径返回了非 nil chunk")
			}
			return
		}
		if c == nil {
			t.Fatalf("解析成功却返回 nil chunk")
		}
		_ = c.HasContent()
		_ = c.IsUsageOnly()
	})
}

// 向量请求:解析成功即代表可转发与计价,必须保证 model/input 双非空且 Raw 原样保留。
func FuzzParseEmbeddingRequest(f *testing.F) {
	f.Add([]byte(`{"model":"emb","input":"hello"}`))
	f.Add([]byte(`{"model":"emb","input":["a","b"]}`))
	f.Add([]byte(`{"model":"emb","input":null}`))
	f.Add([]byte(`{"model":"","input":"x"}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req, err := ParseEmbeddingRequest(body)
		if err != nil {
			if !errors.Is(err, ErrInvalidEmbeddingRequest) {
				t.Fatalf("拒绝原因未归类为 ErrInvalidEmbeddingRequest: %v", err)
			}
			return
		}
		if req.Model == "" || len(req.Input) == 0 || string(req.Input) == "null" {
			t.Fatalf("解析成功但缺必填字段: model=%q input=%s", req.Model, req.Input)
		}
		if !bytes.Equal(req.Raw, body) {
			t.Fatalf("Raw 未原样保留请求体")
		}
	})
}

// 图片请求:张数缺省为一,显式非法张数必须拒绝——转发张数与计价口径不能分叉;
// 字段名大小写敏感,未知字段透传不应影响校验结果。
func FuzzParseImageRequest(f *testing.F) {
	f.Add([]byte(`{"model":"img","prompt":"a cat"}`))
	f.Add([]byte(`{"model":"img","prompt":"a cat","n":2,"size":"1024x1024","quality":"hd"}`))
	f.Add([]byte(`{"model":"img","prompt":"a cat","n":0}`))
	f.Add([]byte(`{"model":"img","prompt":"a cat","n":null}`))
	f.Add([]byte(`{"Model":"img","Prompt":"a cat"}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req, err := ParseImageRequest(body)
		if err != nil {
			return
		}
		if req.Model == "" || req.Prompt == "" || req.N <= 0 {
			t.Fatalf("解析成功但校验不变量被破坏: model=%q prompt=%q n=%d", req.Model, req.Prompt, req.N)
		}
		if !bytes.Equal(req.Raw, body) {
			t.Fatalf("Raw 未原样保留请求体")
		}
	})
}

// 多模态向量请求:单个 JSON 值、model 无首尾空白、contents 非空数组三条约束缺一不可。
func FuzzParseMultimodalEmbeddingRequest(f *testing.F) {
	f.Add([]byte(`{"model":"mm","input":{"contents":[{"type":"text","text":"hi"}]}}`))
	f.Add([]byte(`{"model":" mm ","input":{"contents":[{"a":1}]}}`))
	f.Add([]byte(`{"model":"mm","input":{"contents":[]}}`))
	f.Add([]byte(`{"model":"mm","input":{"contents":[{"a":1}]}} extra`))
	f.Add([]byte(`{"model":"mm"}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req, err := ParseMultimodalEmbeddingRequest(body)
		if err != nil {
			if !errors.Is(err, ErrInvalidMultimodalEmbeddingRequest) {
				t.Fatalf("拒绝原因未归类为 ErrInvalidMultimodalEmbeddingRequest: %v", err)
			}
			return
		}
		if req.Model == "" || req.Model != string(bytes.TrimSpace([]byte(req.Model))) {
			t.Fatalf("解析成功但 model 非规范形态: %q", req.Model)
		}
		if len(req.Contents) == 0 {
			t.Fatalf("解析成功但 contents 为空")
		}
		if !bytes.Equal(req.Raw, body) {
			t.Fatalf("Raw 未原样保留请求体")
		}
	})
}
