// managed 失败语义：无 last-known 时安全类 fail-closed（503）；控制面缺席时用 last-known 起服。
package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/control"
	"github.com/solvela/muniu/internal/testkit"
)

func closedControlURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

func seedManagedKey(t *testing.T, cfg *config.Config) string {
	t.Helper()
	db, err := config0Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	plaintext, _, err := auth.CreateKey(context.Background(), db, "default", "fail-closed", 60, auth.KeyOpts{})
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func writePEMFile(t *testing.T, path string, pub *rsa.PublicKey) {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotApplier_拒绝吊销版本回退(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	verifier := auth.NewVerifier(context.Background(), nil, auth.Options{}, logger)
	t.Cleanup(verifier.Close)
	if !verifier.ApplyRevocations(5, []string{"k-1"}) {
		t.Fatal("初始吊销快照应被应用")
	}
	applier := &snapshotApplier{wiring: controlWiring{verifier: verifier}}
	env := &control.SnapshotEnvelope{
		Version: 9, ObservedAt: time.Now().UTC(),
		Payload: control.SnapshotPayload{
			Revocations: &control.Revocations{Version: 4},
			Tenants:     []control.Tenant{{ID: "t1", Name: "default", Status: "active"}},
		},
	}
	err := applier.ApplySnapshot(context.Background(), control.ApplyInput{Envelope: env, Version: 9})
	if err == nil || !strings.Contains(err.Error(), "吊销版本回退") {
		t.Fatalf("回退吊销版本必须整包拒绝: %v", err)
	}
	if verifier.RevocationsVersion() != 5 || !verifier.RevocationsReady() {
		t.Fatal("拒绝后已应用的吊销名单不得变化")
	}
}

func TestManaged_无快照failClosed(t *testing.T) {
	t.Setenv("E2E_FAKE_KEY", "sk-e2e")
	t.Setenv("MUNIU_ADMIN_TOKEN", "admin-token")
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	dir := t.TempDir()
	pubPath := filepath.Join(dir, "pub.pem")
	signer, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	writePEMFile(t, pubPath, &signer.PublicKey)
	cfgPath := filepath.Join(dir, "muniu.yaml")
	cfgYAML := managedConfigYAML(t, filepath.Join(dir, "muniu.db"), up.URL(), closedControlURL(t), pubPath)
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	apiKey := seedManagedKey(t, cfg)
	handler, _, cleanup, err := assemble(context.Background(), cfg, cfgPath,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("控制面缺席不得阻塞装配: %v", err)
	}
	t.Cleanup(cleanup)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	if code := postChat(t, ts.URL, apiKey); code != http.StatusServiceUnavailable {
		t.Fatalf("无租户配额快照必须 fail-closed(503): %d", code)
	}
	if code, body := adminCall(t, ts.URL, "POST", "/admin/v1/keys", "admin-token",
		`{"tenant":"default","name":"k","rpm_limit":1}`); code != http.StatusServiceUnavailable {
		t.Fatalf("managed 目录未就绪不得签发 key: %d %s", code, body)
	}
}

func TestManaged_缺席时用lastKnown起服(t *testing.T) {
	t.Setenv("E2E_FAKE_KEY", "sk-e2e")
	t.Setenv("MUNIU_ADMIN_TOKEN", "admin-token")
	up := testkit.StartJSONUpstream(t, 200, testkit.JSONOKBody)
	cp := newControlPlaneServer(t, managedSnapshot(t, up.URL()))
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "muniu.db")
	pubPath := filepath.Join(dir, "pub.pem")
	writePEMFile(t, pubPath, &cp.key.PublicKey)

	// 第一轮：控制面在线，落 last-known
	onlinePath := filepath.Join(dir, "online.yaml")
	if err := os.WriteFile(onlinePath, []byte(
		managedConfigYAML(t, dbPath, up.URL(), cp.URL, pubPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(onlinePath)
	if err != nil {
		t.Fatal(err)
	}
	apiKey := seedManagedKey(t, cfg)
	handler, _, cleanup, err := assemble(context.Background(), cfg, onlinePath,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(handler)
	waitJSON(t, ts.URL+"/admin/v1/runtime/status", "admin-token", func(m map[string]any) bool {
		s, _ := m["snapshot"].(map[string]any)
		return s != nil && s["ready"] == true
	})
	cleanup()
	ts.Close()
	cp.Close()

	// 第二轮：控制面缺席，同一 DB 用 last-known 起服
	offlinePath := filepath.Join(dir, "offline.yaml")
	if err := os.WriteFile(offlinePath, []byte(
		managedConfigYAML(t, dbPath, up.URL(), closedControlURL(t), pubPath)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg2, err := config.Load(offlinePath)
	if err != nil {
		t.Fatal(err)
	}
	handler2, _, cleanup2, err := assemble(context.Background(), cfg2, offlinePath,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("控制面缺席必须可用 last-known 起服: %v", err)
	}
	t.Cleanup(cleanup2)
	ts2 := httptest.NewServer(handler2)
	t.Cleanup(ts2.Close)
	status := waitJSON(t, ts2.URL+"/admin/v1/runtime/status", "admin-token", func(m map[string]any) bool {
		s, _ := m["snapshot"].(map[string]any)
		return s != nil && s["ready"] == true
	})
	s, _ := status["snapshot"].(map[string]any)
	if s["stale"] != true {
		t.Fatalf("控制面缺席的 last-known 必须标 stale: %v", s)
	}
	if code := postChat(t, ts2.URL, apiKey); code != 200 {
		t.Fatalf("last-known 起服后数据面应正常: %d", code)
	}
}
