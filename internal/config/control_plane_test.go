// 控制面模式/身份配置校验：greenfield 退役旧键、managed 必需身份与验签、standalone 拒绝残留。
package config

import (
	"strings"
	"testing"
)

func loadYAML(t *testing.T, extra string) (*Config, error) {
	t.Helper()
	content := minimalYAML("info") + extra
	return Load(writeTemp(t, content))
}

func TestConfig_退役的分散控制键拒绝(t *testing.T) {
	cases := []string{
		"control_plane:\n  pricing_url: \"http://x/control/v1/pricing\"\n",
		"control_plane:\n  topology_url: \"http://x/control/v1/topology\"\n",
		"auth:\n  revocations_url: \"http://x/control/v1/key-revocations\"\n",
		"metering:\n  sink: ingest\n",
	}
	for _, extra := range cases {
		if _, err := loadYAML(t, extra); err == nil || !strings.Contains(err.Error(), "已退役") {
			t.Fatalf("旧控制键必须拒绝: %q err=%v", extra, err)
		}
	}
}

func TestConfig_managed身份必填与JWKS缺省(t *testing.T) {
	base := "control_plane:\n  mode: managed\n  base_url: \"http://127.0.0.1:8090\"\n"
	if _, err := loadYAML(t, base); err == nil || !strings.Contains(err.Error(), "服务身份") {
		t.Fatalf("缺服务身份必须拒绝: %v", err)
	}
	withToken := base + "  service_token: \"token-0123456789abcdef\"\n"
	cfg, err := loadYAML(t, withToken)
	if err != nil {
		t.Fatalf("无静态公钥时应由 JWKS 派生并通过: %v", err)
	}
	if cfg.Metering.Sink != "control" {
		t.Fatalf("managed 计量 sink 缺省应为 control: %q", cfg.Metering.Sink)
	}
	if cfg.ControlPlane.SnapshotJWKSURL != "" {
		t.Fatalf("JWKS URL 缺省应留空由 base_url 派生: %q", cfg.ControlPlane.SnapshotJWKSURL)
	}
}

func TestConfig_静态令牌仅限loopback(t *testing.T) {
	extra := "control_plane:\n  mode: managed\n  base_url: \"https://jinguan.example.com\"\n" +
		"  service_token: \"token-0123456789abcdef\"\n" +
		"  snapshot_public_key_file: \"/run/secrets/muniu-control.pem\"\n"
	if _, err := loadYAML(t, extra); err == nil || !strings.Contains(err.Error(), "仅限 loopback") {
		t.Fatalf("非 loopback 静态令牌必须拒绝: %v", err)
	}
	client := "control_plane:\n  mode: managed\n  base_url: \"https://jinguan.example.com\"\n" +
		"  token_url: \"https://keycloak.example.com/token\"\n  client_id: \"muniu-control\"\n" +
		"  client_secret: \"secret-0123456789abcdef\"\n" +
		"  snapshot_public_key_file: \"/run/secrets/muniu-control.pem\"\n"
	if _, err := loadYAML(t, client); err != nil {
		t.Fatalf("client credentials 形态应通过: %v", err)
	}
}

func TestConfig_standalone拒绝控制面残留(t *testing.T) {
	extra := "control_plane:\n  mode: standalone\n  base_url: \"http://127.0.0.1:8090\"\n"
	if _, err := loadYAML(t, extra); err == nil || !strings.Contains(err.Error(), "standalone") {
		t.Fatalf("standalone 残留控制面字段必须拒绝: %v", err)
	}
}

func TestConfig_计量sink模式约束与serviceAPItoken(t *testing.T) {
	if _, err := loadYAML(t, "metering:\n  sink: control\n"); err == nil {
		t.Fatal("standalone 不得使用 control sink")
	}
	shortToken := "service_api:\n  token: \"short\"\n"
	if _, err := loadYAML(t, shortToken); err == nil || !strings.Contains(err.Error(), "过短") {
		t.Fatalf("服务只读 token 过短必须拒绝: %v", err)
	}
}

func TestConfig_服务只读Keycloak校验(t *testing.T) {
	// 只声明 audience 而没有 issuer：拒绝，避免静默退化为未校验。
	partial := "service_api:\n  audience: \"muniu\"\n"
	if _, err := loadYAML(t, partial); err == nil || !strings.Contains(err.Error(), "成套") {
		t.Fatalf("缺 issuer 的 Keycloak 片段必须拒绝: %v", err)
	}
	// issuer 必须为绝对 URL，且 clients 白名单必填。
	badURL := "service_api:\n  issuer: \"keycloak\"\n  clients: [\"jinguan-read-muniu\"]\n"
	if _, err := loadYAML(t, badURL); err == nil || !strings.Contains(err.Error(), "绝对 URL") {
		t.Fatalf("非法 issuer 必须拒绝: %v", err)
	}
	noClients := "service_api:\n  issuer: \"http://127.0.0.1:8080/realms/longzhong\"\n"
	if _, err := loadYAML(t, noClients); err == nil || !strings.Contains(err.Error(), "clients") {
		t.Fatalf("缺 clients 白名单必须拒绝: %v", err)
	}
	// 完整声明通过，并物化缺省 audience/scope。
	valid := noClients + "  clients: [\"jinguan-read-muniu\"]\n"
	cfg, err := loadYAML(t, valid)
	if err != nil {
		t.Fatalf("完整 Keycloak 服务段应通过: %v", err)
	}
	if !cfg.ServiceAPI.Enabled() || !cfg.ServiceAPI.Keycloak() {
		t.Fatal("Keycloak 服务段应启用的读者面")
	}
	if cfg.ServiceAPI.Audience != "muniu" || cfg.ServiceAPI.RequiredScope != "service:read" {
		t.Fatalf("缺省 audience/scope 未物化: %+v", cfg.ServiceAPI)
	}
}
