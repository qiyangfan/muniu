package protocol

import (
	"bytes"
	"testing"
)

func TestImages_请求保真与校验(t *testing.T) {
	raw := []byte(`{"model":"paint","prompt":"画木牛","size":"1024x1024","quality":"hd","extra":{"x":1}}`)
	r, err := ParseImageRequest(raw)
	if err != nil || r.N != 1 || r.TierKey() != "1024x1024|hd" || !bytes.Equal(r.Raw, raw) {
		t.Fatalf("解析保真: %+v %v", r, err)
	}
	r, err = ParseImageRequest([]byte(`{"model":"paint","prompt":"x"}`))
	if err != nil || r.TierKey() != "|" {
		t.Fatalf("缺省档位不猜测: %+v %v", r, err)
	}
	for _, s := range []string{`null`, `{}`, `{"model":"paint"}`, `{"model":"paint","prompt":""}`, `{"model":"paint","prompt":"x","n":0}`, `{"model":"paint","prompt":"x","n":1.5}`} {
		if _, err := ParseImageRequest([]byte(s)); err == nil {
			t.Errorf("非法请求未拒: %s", s)
		}
	}
}

func TestImages_计价只消费标准字段(t *testing.T) {
	raw := []byte(`{"model":"paint","prompt":"x","n":1,"N":9,"Size":"1024","Quality":"hd"}`)
	req, err := ParseImageRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if req.N != 1 || req.TierKey() != "|" || !bytes.Equal(req.Raw, raw) {
		t.Fatalf("未知大小写字段污染计价: n=%d tier=%s", req.N, req.TierKey())
	}
	if _, err := ParseImageRequest([]byte(`{"Model":"paint","Prompt":"x"}`)); err == nil {
		t.Fatal("缺少标准 model/prompt 字段未拒绝")
	}
}
