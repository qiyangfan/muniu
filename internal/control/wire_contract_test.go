// 控制面 wire 契约提案与本仓客户端常量/端点/必填字段对齐，防止单边漂移。
package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

type wireContract struct {
	Info struct {
		Version string `yaml:"version"`
	} `yaml:"info"`
	ServiceID       string                    `yaml:"x-service-id"`
	SignatureHeader string                    `yaml:"x-signature-header"`
	ProposalStatus  string                    `yaml:"x-proposal-status"`
	Paths           map[string]map[string]any `yaml:"paths"`
	Components      struct {
		Schemas map[string]struct {
			Required []string `yaml:"required"`
		} `yaml:"schemas"`
	} `yaml:"components"`
}

func loadWireContract(t *testing.T) wireContract {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "contracts", "control-plane-wire.v1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var c wireContract
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("契约 YAML 非法: %v", err)
	}
	return c
}

func TestWireContract_端点与常量对齐(t *testing.T) {
	c := loadWireContract(t)
	if c.Info.Version != "1" || c.ProposalStatus == "" {
		t.Fatalf("契约版本/提案状态缺失: %+v", c)
	}
	if c.ServiceID != ServiceID {
		t.Fatalf("service_id 漂移: 契约=%q 代码=%q", c.ServiceID, ServiceID)
	}
	if c.SignatureHeader != SignatureHeader {
		t.Fatalf("签名头漂移: 契约=%q 代码=%q", c.SignatureHeader, SignatureHeader)
	}
	p := DefaultPaths()
	cases := []struct{ path, method string }{
		{p.Snapshot, "GET"},
		{p.Register, "POST"},
		{p.Heartbeat, "POST"},
		{p.Usage, "POST"},
		{p.Claim, "POST"},
		{p.Ack, "POST"},
	}
	for _, tc := range cases {
		methods, ok := c.Paths[tc.path]
		if !ok {
			t.Fatalf("契约缺少端点 %s", tc.path)
		}
		if _, ok := methods[strings.ToLower(tc.method)]; !ok {
			t.Fatalf("契约 %s 缺少方法 %s", tc.path, tc.method)
		}
	}
}

func TestWireContract_关键必填字段(t *testing.T) {
	c := loadWireContract(t)
	required := map[string][]string{
		"SnapshotEnvelope":    {"version", "observed_at", "payload"},
		"SnapshotPayload":     {"revocations", "tenants"},
		"ImagePricingEntry":   {"alias", "currency", "default", "tiers"},
		"RegistrationRequest": {"service_id", "instance_id", "version", "capabilities", "started_at"},
		"HeartbeatRequest":    {"observed_at", "version", "capabilities", "health"},
		"UsageIngest":         {"rows", "source", "schema_version"},
		"ClaimRequest":        {"service_id", "instance_id", "limit"},
		"WireCommand":         {"id", "action", "action_schema_version", "params"},
		"AckRequest":          {"service_id", "instance_id", "status"},
	}
	for schema, fields := range required {
		entry, ok := c.Components.Schemas[schema]
		if !ok {
			t.Fatalf("契约缺少 schema %s", schema)
		}
		set := map[string]bool{}
		for _, f := range entry.Required {
			set[f] = true
		}
		for _, f := range fields {
			if !set[f] {
				t.Fatalf("schema %s 缺少必填字段 %s", schema, f)
			}
		}
	}
}
