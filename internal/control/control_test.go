// 控制面消费侧聚焦测试：快照验签/版本单调/last-known、注册退避、命令幂等、用量 source。
package control

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/store"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "control.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newRSAKey(t *testing.T) (*rsa.PrivateKey, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, &key.PublicKey
}

func signBody(t *testing.T, key *rsa.PrivateKey, body []byte) string {
	t.Helper()
	h := sha256.Sum256(body)
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(sig)
}

func publicPEM(t *testing.T, pub *rsa.PublicKey) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	block := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return string(block)
}

func snapshotBody(t *testing.T, version int64) []byte {
	t.Helper()
	return wireSnapshotBody(t, version, true)
}

func wireSnapshotBody(t *testing.T, version int64, withRevocations bool) []byte {
	t.Helper()
	wire := WireSnapshot{
		Version: version, ObservedAt: time.Now().UTC(), ServiceID: ServiceID,
		ETag:             `"v` + strconv.FormatInt(version, 10) + `"`,
		SecuritySections: []string{"tenant_directory", "entitlements", "policy_packages", "key_revocations"},
		Sections: WireSections{
			TenantDirectory: &WireTenantDirectory{Tenants: []WireTenant{{
				ID: "t1", TenantKey: "default", Name: "default", Status: "ACTIVE",
			}}},
			Entitlements:   &WireEntitlements{Tenants: []WireEntitlement{{TenantKey: "default"}}},
			PolicyPackages: json.RawMessage(`{"packageCount":0,"packages":[]}`),
			Pricing:        &WirePricing{Pricing: []PricingEntry{}},
		},
	}
	if withRevocations {
		wire.Sections.KeyRevocations = &Revocations{Version: 1}
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestWireTenant_maxInflight(t *testing.T) {
	build := func(quota *WireQuota) *WireSnapshot {
		ent := WireEntitlement{TenantKey: "default"}
		if quota != nil {
			ent.Quotas = []WireQuota{*quota}
		}
		return &WireSnapshot{
			Version: 1, ObservedAt: time.Now().UTC(), ServiceID: ServiceID,
			SecuritySections: []string{"tenant_directory", "entitlements", "policy_packages", "key_revocations"},
			Sections: WireSections{
				TenantDirectory: &WireTenantDirectory{Tenants: []WireTenant{{
					ID: "t1", TenantKey: "default", Name: "default", Status: "ACTIVE",
				}}},
				Entitlements:   &WireEntitlements{Tenants: []WireEntitlement{ent}},
				PolicyPackages: json.RawMessage(`{"packageCount":0,"packages":[]}`),
				KeyRevocations: &Revocations{Version: 1},
			},
		}
	}
	env, err := build(&WireQuota{Dimension: "max_inflight", Unit: "count", Limit: 3}).ToEnvelope(ServiceID)
	if err != nil || len(env.Payload.Tenants) != 1 || env.Payload.Tenants[0].MaxInflight == nil ||
		*env.Payload.Tenants[0].MaxInflight != 3 {
		t.Fatalf("max_inflight 应映射为租户上限: %+v err=%v", env, err)
	}
	// 显式 0 与缺席必须区分:0=不限,缺席=本地缺省。
	env, err = build(&WireQuota{Dimension: "max_inflight", Unit: "count", Limit: 0}).ToEnvelope(ServiceID)
	if err != nil || env.Payload.Tenants[0].MaxInflight == nil || *env.Payload.Tenants[0].MaxInflight != 0 {
		t.Fatalf("显式 0 应保留: %+v err=%v", env, err)
	}
	env, err = build(nil).ToEnvelope(ServiceID)
	if err != nil || env.Payload.Tenants[0].MaxInflight != nil {
		t.Fatalf("缺席应为 nil: %+v err=%v", env, err)
	}
	// 同租户重复权益条目不得把已下发的上限覆盖成缺席后的零值。
	dup := build(nil)
	dup.Sections.Entitlements.Tenants = []WireEntitlement{
		{TenantKey: "default", Quotas: []WireQuota{{Dimension: "max_inflight", Unit: "count", Limit: 3}}},
		{TenantKey: "default"},
	}
	env, err = dup.ToEnvelope(ServiceID)
	if err != nil || env.Payload.Tenants[0].MaxInflight == nil || *env.Payload.Tenants[0].MaxInflight != 3 {
		t.Fatalf("重复权益条目不得吞掉 max_inflight: %+v err=%v", env, err)
	}
	if _, err := build(&WireQuota{Dimension: "max_inflight", Unit: "count", Limit: -1}).ToEnvelope(ServiceID); err == nil {
		t.Fatal("负数 max_inflight 必须拒绝")
	}
}

type recordingApplier struct {
	mu      sync.Mutex
	applied []int64
	fail    bool
}

func (a *recordingApplier) ApplySnapshot(_ context.Context, in ApplyInput) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.fail {
		return io.ErrUnexpectedEOF
	}
	a.applied = append(a.applied, in.Version)
	return nil
}

func (a *recordingApplier) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.applied)
}

type memStore struct{ rec *store.ControlSnapshotRecord }

func (m *memStore) Load(context.Context, string) (*store.ControlSnapshotRecord, error) {
	if m.rec == nil {
		return nil, store.ErrNotFound
	}
	return m.rec, nil
}

func TestSnapshot_验签应用版本单调与lastKnown(t *testing.T) {
	key, pub := newRSAKey(t)
	var version atomic.Int64
	version.Store(1)
	var body atomic.Value
	body.Store(snapshotBody(t, 1))
	var authHeader atomic.Value
	var sawCond atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader.Store(r.Header.Get("Authorization"))
		raw := body.Load().([]byte)
		sum := sha256.Sum256(raw)
		etag := `"` + hex.EncodeToString(sum[:8]) + `"`
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			sawCond.Store(true)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set(SignatureHeader, signBody(t, key, raw))
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)

	client := NewClient(srv.URL, StaticToken("token-0123456789abcdef"), time.Second)
	applier := &recordingApplier{}
	c := NewSnapshotConsumer(client, pub, applier, &memStore{}, ServiceID, time.Minute, testLogger())

	c.fetchOnce(context.Background())
	if applier.count() != 1 || c.Status().Version != 1 || !c.Status().Ready {
		t.Fatalf("首轮应应用 v1: applied=%d status=%+v", applier.count(), c.Status())
	}
	if authHeader.Load() != "Bearer token-0123456789abcdef" {
		t.Fatalf("必须携带服务身份 Bearer: %v", authHeader.Load())
	}

	// 304：不重复应用且视为可达（非 stale）
	c.fetchOnce(context.Background())
	if !sawCond.Load() {
		t.Fatal("第二轮必须带 If-None-Match 条件请求")
	}
	if applier.count() != 1 {
		t.Fatal("304 不得重复应用")
	}

	// v2 应用
	version.Store(2)
	v2 := snapshotBody(t, 2)
	body.Store(v2)
	c.fetchOnce(context.Background())
	if applier.count() != 2 || c.Status().Version != 2 {
		t.Fatalf("v2 应被应用: applied=%d status=%+v", applier.count(), c.Status())
	}

	// 版本回退拒绝
	body.Store(snapshotBody(t, 1))
	c.fetchOnce(context.Background())
	if applier.count() != 2 || c.Status().Version != 2 {
		t.Fatal("旧版本不得覆盖新版本")
	}
	if c.Status().LastError == "" {
		t.Fatal("版本回退应留下错误状态")
	}

	// 缺吊销名单（安全类）拒绝
	body.Store(wireSnapshotBody(t, 3, false))
	c.fetchOnce(context.Background())
	if applier.count() != 2 || c.Status().Version != 2 {
		t.Fatal("缺吊销名单的安全快照必须拒绝")
	}

	// 控制面恢复 304：last_error 必须清除，否则诊断页长期显示已恢复的错误
	body.Store(v2)
	c.fetchOnce(context.Background())
	if got := c.Status().LastError; got != "" {
		t.Fatalf("304 后应清除 last_error: %q", got)
	}

	// last-known 应用与篡改拒绝
	raw2 := snapshotBody(t, 2)
	rec := &store.ControlSnapshotRecord{
		ServiceID: ServiceID, Version: 2, ETag: `"v2"`, ObservedAt: time.Now().UTC().Format(time.RFC3339),
		Signature: signBody(t, key, raw2), Payload: raw2,
	}
	applied := &recordingApplier{}
	c2 := NewSnapshotConsumer(client, pub, applied, &memStore{rec: rec}, ServiceID, time.Minute, testLogger())
	if err := c2.LoadLastKnown(context.Background()); err != nil {
		t.Fatalf("last-known 应可应用: %v", err)
	}
	if applied.count() != 1 || !appliedSnapshot(applied, 2) {
		t.Fatal("last-known 应应用 v2")
	}
	if !c2.Status().Stale {
		t.Fatal("未联系控制面的 last-known 应标记 stale")
	}
	rec.Payload = append([]byte{}, rec.Payload...)
	rec.Payload[len(rec.Payload)-2] ^= 0xff
	applied2 := &recordingApplier{}
	c3 := NewSnapshotConsumer(client, pub, applied2, &memStore{rec: rec}, ServiceID, time.Minute, testLogger())
	if err := c3.LoadLastKnown(context.Background()); err == nil || applied2.count() != 0 {
		t.Fatal("篡改的 last-known 必须拒绝且不应用")
	}
}

func rsaJWK(pub *rsa.PublicKey, kid string) map[string]any {
	return map[string]any{
		"kty": "RSA",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}
}

func TestSnapshot_JWKS按keyid验签(t *testing.T) {
	key, pub := newRSAKey(t)
	raw := snapshotBody(t, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/jwks.json" {
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{rsaJWK(pub, "kid-1")}})
			return
		}
		w.Header().Set(KeyIDHeader, "kid-1")
		w.Header().Set(SignatureHeader, signBody(t, key, raw))
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)

	client := NewClient(srv.URL, StaticToken("token-0123456789abcdef"), time.Second)
	applier := &recordingApplier{}
	c := NewSnapshotConsumer(client, nil, applier, &memStore{}, ServiceID, time.Minute, testLogger())
	c.Keys = NewJWKSKeySource(srv.URL + "/.well-known/jwks.json")
	c.fetchOnce(context.Background())
	if applier.count() != 1 || c.Status().Version != 1 {
		t.Fatalf("JWKS 模式应按 kid 验签并应用: applied=%d status=%+v", applier.count(), c.Status())
	}
}

func TestJWKSKeySource_未知kid触发刷新(t *testing.T) {
	_, pub1 := newRSAKey(t)
	_, pub2 := newRSAKey(t)
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		keys := []any{rsaJWK(pub1, "kid-1")}
		if calls.Add(1) > 1 {
			keys = append(keys, rsaJWK(pub2, "kid-2"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	}))
	t.Cleanup(srv.Close)
	src := NewJWKSKeySource(srv.URL)
	src.mu.Lock()
	src.forcedAt = time.Now().Add(-time.Minute)
	src.mu.Unlock()

	key, err := src.Key(context.Background(), "kid-1")
	if err != nil || key.N.Cmp(pub1.N) != 0 {
		t.Fatalf("kid-1 应解析到首把公钥: %v", err)
	}
	if _, err := src.Key(context.Background(), "kid-2"); err != nil {
		t.Fatalf("未知 kid 应触发刷新: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("未知 kid 只应触发一次刷新: %d", calls.Load())
	}
}

func appliedSnapshot(a *recordingApplier, version int64) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, v := range a.applied {
		if v == version {
			return true
		}
	}
	return false
}

func TestLoadPublicKey_PEM往返(t *testing.T) {
	_, pub := newRSAKey(t)
	path := filepath.Join(t.TempDir(), "pub.pem")
	if err := os.WriteFile(path, []byte(publicPEM(t, pub)), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPublicKey(path)
	if err != nil || loaded.N.Cmp(pub.N) != 0 {
		t.Fatalf("公钥加载失败: %v", err)
	}
}

func TestVerifySignature_有效与篡改(t *testing.T) {
	key, pub := newRSAKey(t)
	body := []byte(`{"hello":"world"}`)
	sig := signBody(t, key, body)
	if err := VerifySignature(pub, body, sig); err != nil {
		t.Fatalf("有效签名应通过: %v", err)
	}
	if err := VerifySignature(pub, append(body, 'x'), sig); err == nil {
		t.Fatal("篡改体必须拒绝")
	}
	if err := VerifySignature(pub, body, ""); err == nil {
		t.Fatal("缺签名必须拒绝")
	}
}

func TestRegistrar_退避与恢复补报(t *testing.T) {
	var registers, heartbeats atomic.Int64
	var firstFail atomic.Bool
	firstFail.Store(true)
	var payloads sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-0123456789abcdef" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		switch r.URL.Path {
		case "/control/v1/registry/heartbeat":
			heartbeats.Add(1)
			payloads.Store("heartbeat", body)
			w.WriteHeader(http.StatusNoContent)
		case "/ingest/health":
			payloads.Store("health", body)
			w.WriteHeader(http.StatusNoContent)
		case "/control/v1/registry/register":
			registers.Add(1)
			if firstFail.Swap(false) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			payloads.Store("register", body)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client := NewClient(srv.URL, StaticToken("token-0123456789abcdef"), time.Second)
	reg := NewRegistrar(client, Identity{ServiceID: ServiceID, InstanceID: "inst-1",
		Version: "test", StartedAt: time.Now()}, 20*time.Millisecond, func(context.Context) Health {
		return Health{Status: "ok", Metrics: map[string]any{"db_ok": true}}
	}, testLogger())
	reg.Backoff = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { reg.Run(ctx); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for heartbeats.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if registers.Load() < 2 {
		t.Fatalf("首次失败后必须重新注册: registers=%d", registers.Load())
	}
	if heartbeats.Load() == 0 {
		t.Fatal("恢复后必须补报心跳")
	}
	regBody, _ := payloads.Load("register")
	m := regBody.(map[string]any)
	if m["serviceId"] != ServiceID || m["instanceId"] != "inst-1" || m["version"] != "test" ||
		m["environment"] != "local" {
		t.Fatalf("注册 payload 不符: %v", m)
	}
	for _, forbidden := range []string{"endpoint", "token", "credential", "service_config", "base_url"} {
		if _, ok := m[forbidden]; ok {
			t.Fatalf("注册 payload 不得包含 %s: %v", forbidden, m)
		}
	}
	hb, _ := payloads.Load("heartbeat")
	hbm := hb.(map[string]any)
	if hbm["serviceId"] != ServiceID || hbm["instanceId"] != "inst-1" {
		t.Fatalf("心跳 payload 不符: %v", hbm)
	}
	healthBody, _ := payloads.Load("health")
	hm := healthBody.(map[string]any)
	if hm["serviceId"] != ServiceID || hm["status"] != "UP" {
		t.Fatalf("健康事件须独立上报为 UP: %v", hm)
	}
}

// probeTargets 在执行瞬间回读台账，证明 pending 先于执行落库。
type probeTargets struct {
	db    *sql.DB
	seen  atomic.Value
	execs atomic.Int64
}

func (p *probeTargets) PurgeCache(ctx context.Context, _ string, _ bool) (int, error) {
	p.execs.Add(1)
	var status string
	if err := p.db.QueryRowContext(ctx, `SELECT status FROM control_commands WHERE id='c-pending'`).
		Scan(&status); err != nil {
		return 0, err
	}
	p.seen.Store(status)
	return 2, nil
}

func (p *probeTargets) ReloadTopology(context.Context) error { return nil }

func TestCommandExecutor_执行前先落pending(t *testing.T) {
	db := testDB(t)
	var claims atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/control/v1/commands/claim":
			if claims.Add(1) > 1 {
				_ = json.NewEncoder(w).Encode([]any{})
				return
			}
			_ = json.NewEncoder(w).Encode([]any{
				map[string]any{"id": "c-pending", "action": "cache_purge", "actionSchemaVersion": 1,
					"params": map[string]any{"all": true}},
			})
		case strings.HasSuffix(r.URL.Path, "/ack"):
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	targets := &probeTargets{db: db}
	client := NewClient(srv.URL, StaticToken("token-0123456789abcdef"), time.Second)
	exec := NewCommandExecutor(db, client, "inst-1", targets, nil, time.Minute, testLogger())
	if err := exec.ClaimOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if targets.execs.Load() != 1 {
		t.Fatalf("合法命令应执行一次: %d", targets.execs.Load())
	}
	if got, _ := targets.seen.Load().(string); got != "pending" {
		t.Fatalf("执行时台账必须为 pending,实际 %q", got)
	}
	var status string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status FROM control_commands WHERE id='c-pending'`).Scan(&status); err != nil || status != "acked" {
		t.Fatalf("执行后台账应更新为 acked: status=%q err=%v", status, err)
	}
}

// 心跳持续失败时退避不得被“注册成功”反复复位，否则形成 1s 级紧循环并刷审计。
func TestRegistrar_心跳失败保持指数退避(t *testing.T) {
	var registers, heartbeats atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/control/v1/registry/register":
			registers.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case "/control/v1/registry/heartbeat":
			heartbeats.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case "/ingest/health":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	client := NewClient(srv.URL, StaticToken("token-0123456789abcdef"), time.Second)
	reg := NewRegistrar(client, Identity{ServiceID: ServiceID, InstanceID: "inst-hb",
		Version: "test", StartedAt: time.Now()}, time.Hour, nil, testLogger())
	reg.Backoff = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { reg.Run(ctx); close(done) }()
	time.Sleep(280 * time.Millisecond)
	cancel()
	<-done
	// 20→40→80→160ms 退避下约 4 轮；旧实现会以 20ms 间隔跑十余轮。
	if got := registers.Load(); got < 2 || got > 7 {
		t.Fatalf("心跳持续失败时注册次数应随退避收敛,实际 %d(心跳 %d)", got, heartbeats.Load())
	}
}

func TestClientCredentials_缓存与刷新(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "client_credentials" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok-1", "expires_in": 3600})
	}))
	t.Cleanup(srv.Close)
	now := time.Now()
	cc := &ClientCredentials{TokenURL: srv.URL, ClientID: "muniu-control", ClientSecret: "secret",
		now: func() time.Time { return now }}
	tok, err := cc.Token(context.Background())
	if err != nil || tok != "tok-1" {
		t.Fatalf("取令牌失败: %v %q", err, tok)
	}
	if _, err := cc.Token(context.Background()); err != nil || calls.Load() != 1 {
		t.Fatalf("缓存期内不得重复取令牌: calls=%d", calls.Load())
	}
	now = now.Add(2 * time.Hour)
	if _, err := cc.Token(context.Background()); err != nil || calls.Load() != 2 {
		t.Fatalf("过期后应刷新: calls=%d", calls.Load())
	}
}

type countTargets struct {
	mu     sync.Mutex
	purges int
	reload int
}

func (c *countTargets) PurgeCache(context.Context, string, bool) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purges++
	return 3, nil
}

func (c *countTargets) ReloadTopology(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reload++
	return nil
}

func TestCommandExecutor_幂等未知拒绝与本地命令(t *testing.T) {
	db := testDB(t)
	var claimCalls atomic.Int64
	var acks sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token-0123456789abcdef" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/control/v1/commands/claim":
			if claimCalls.Add(1) > 1 {
				_ = json.NewEncoder(w).Encode([]any{})
				return
			}
			_ = json.NewEncoder(w).Encode([]any{
				map[string]any{"id": "c1", "action": "cache_purge", "actionSchemaVersion": 1,
					"params": map[string]any{"all": true}, "actor": "alice", "reason": "cleanup"},
				map[string]any{"id": "c2", "action": "config_write", "actionSchemaVersion": 1, "params": map[string]any{}},
				map[string]any{"id": "c3", "action": "cache_purge", "actionSchemaVersion": 9, "params": map[string]any{"all": true}},
				map[string]any{"id": "c4", "action": "cache_purge", "actionSchemaVersion": 1, "params": map[string]any{"tenant": ""}},
			})
		case strings.HasSuffix(r.URL.Path, "/ack"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			acks.Store(strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/control/v1/commands/"), "/ack"), body["status"])
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	targets := &countTargets{}
	client := NewClient(srv.URL, StaticToken("token-0123456789abcdef"), time.Second)
	exec := NewCommandExecutor(db, client, "inst-1", targets, nil, time.Minute, testLogger())
	if err := exec.ClaimOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if targets.purges != 1 {
		t.Fatalf("只有合法 cache_purge 执行一次: %d", targets.purges)
	}
	for id, want := range map[string]string{"c1": "SUCCEEDED", "c2": "FAILED", "c3": "FAILED", "c4": "FAILED"} {
		got, ok := acks.Load(id)
		if !ok || got != want {
			t.Fatalf("%s ack 应为 %s,实际 %v", id, want, got)
		}
	}
	// 重复 claim 同一命令：终态幂等，不再执行
	claimCalls.Store(0)
	if err := exec.ClaimOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if targets.purges != 1 {
		t.Fatalf("重复命令不得重复执行: %d", targets.purges)
	}

	// 本地命令直接执行
	view, err := exec.ExecuteLocal(context.Background(), "admin", "manual", "cache_purge",
		[]byte(`{"tenant":"t1"}`))
	if err != nil || view.Status != "acked" || view.Source != "local" {
		t.Fatalf("本地命令应成功: %+v err=%v", view, err)
	}
	if targets.purges != 2 {
		t.Fatalf("本地命令应执行一次: %d", targets.purges)
	}
	if _, err := exec.ExecuteLocal(context.Background(), "admin", "manual", "config_write",
		[]byte(`{}`)); err == nil {
		t.Fatal("本地未知 action 必须失败")
	}
	cmds, err := exec.Commands(context.Background(), 20)
	if err != nil || len(cmds) < 5 {
		t.Fatalf("台账应保留命令: %d err=%v", len(cmds), err)
	}
	for _, c := range cmds {
		if c.ID == "c2" && !strings.Contains(c.Result, "未注册的 action") {
			t.Fatalf("失败行须落错误原因供控制台展示: %+v", c)
		}
	}
}

func TestUsageSink_本地落库与source(t *testing.T) {
	db := testDB(t)
	var got atomic.Value
	fail := atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.Store(string(raw))
		if fail.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	client := NewClient(srv.URL, StaticToken("token-0123456789abcdef"), time.Second)
	sink := NewUsageSink(context.Background(), db, client)
	row := store.UsageRow{
		ID: "u-1", RequestID: "r-1", TenantID: "t1", APIKeyID: "k1", Provider: "p",
		ModelAlias: "m", UpstreamModel: "u", Status: "ok", CostCredits: 5,
		Currency: "CNY", LatencyTotalMs: 1, CreatedAt: time.Now().UTC(),
	}
	if err := sink.Write([]store.UsageRow{row}); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM usage_logs`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("本地计量必须保留: n=%d err=%v", n, err)
	}
	var env struct {
		Source string           `json:"source"`
		Rows   []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal([]byte(got.Load().(string)), &env); err != nil {
		t.Fatal(err)
	}
	if env.Source != "muniu" || len(env.Rows) != 1 || env.Rows[0]["ID"] != "u-1" {
		t.Fatalf("用量信封不符: %s", got.Load())
	}
	fail.Store(true)
	row2 := row
	row2.ID = "u-2"
	if err := sink.Write([]store.UsageRow{row2}); err == nil {
		t.Fatal("控制面失败必须返回错误交给缓冲重试")
	}
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM usage_logs`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("远端失败也必须本地落库: n=%d err=%v", n, err)
	}
}
