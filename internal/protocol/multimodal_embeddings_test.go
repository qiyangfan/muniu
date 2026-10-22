package protocol

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseMultimodalEmbeddingRequest_Fixtures(t *testing.T) {
	fixtures := []string{
		"../../conformance/qwen/native/multimodal-independent.json",
		"../../conformance/qwen/native/multimodal-fusion.json",
		"../../conformance/tongyi/native/multimodal-multi-images.json",
	}
	for _, fixture := range fixtures {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			body, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatal(err)
			}
			req, err := ParseMultimodalEmbeddingRequest(body)
			if err != nil {
				t.Fatalf("fixture 解析失败: %v", err)
			}
			if req.Model == "" || len(req.Contents) == 0 || string(req.Raw) != string(body) {
				t.Fatalf("路由字段或原始 body 未保留: %+v", req)
			}
		})
	}
}

func TestParseMultimodalEmbeddingRequest_RequiredFields(t *testing.T) {
	for name, body := range map[string]string{
		"invalid json":         `{`,
		"missing model":        `{"input":{"contents":[{"text":"hi"}]}}`,
		"empty model":          `{"model":" ","input":{"contents":[{"text":"hi"}]}}`,
		"missing input":        `{"model":"alias"}`,
		"null input":           `{"model":"alias","input":null}`,
		"missing contents":     `{"model":"alias","input":{}}`,
		"null contents":        `{"model":"alias","input":{"contents":null}}`,
		"empty contents":       `{"model":"alias","input":{"contents":[]}}`,
		"non-array contents":   `{"model":"alias","input":{"contents":"hi"}}`,
		"top-level non-object": `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseMultimodalEmbeddingRequest([]byte(body)); err == nil {
				t.Fatal("非法原生请求应拒绝")
			}
		})
	}
}
