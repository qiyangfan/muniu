// 统一控制快照消费：条件请求、验签、版本单调、last-known 启动与 stale 标记。
// 应用动作交给 Applier（装配层）完成价目/拓扑/吊销/配额/策略的原子替换与持久化。
package control

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/solvela/muniu/internal/store"
)

// SignatureHeader 快照响应签名头，值为 RSA-SHA256 PKCS#1 v1.5 签名 base64。
const SignatureHeader = "X-Control-Signature"

// SnapshotStore last-known 读取接口（装配层用 store 函数实现）。
type SnapshotStore interface {
	Load(ctx context.Context, serviceID string) (*store.ControlSnapshotRecord, error)
}

// ApplyInput 一次待应用的快照及其持久化原文。
type ApplyInput struct {
	Envelope   *SnapshotEnvelope
	Raw        []byte
	Signature  string
	ETag       string
	Version    int64
	ObservedAt time.Time
}

// Applier 原子应用快照：构建全部派生数据后切换，失败保留旧目录。
type Applier interface {
	ApplySnapshot(ctx context.Context, in ApplyInput) error
}

// SnapshotStatus 快照消费状态（控制台/只读投影）。
type SnapshotStatus struct {
	Version     int64     `json:"version"`
	ETag        string    `json:"etag"`
	ObservedAt  time.Time `json:"observed_at"`
	AppliedAt   time.Time `json:"applied_at"`
	LastContact time.Time `json:"last_contact"`
	Stale       bool      `json:"stale"`
	Ready       bool      `json:"ready"`
	LastError   string    `json:"last_error,omitempty"`
}

// SnapshotConsumer 快照拉取与状态机。
type SnapshotConsumer struct {
	Client    *Client
	PublicKey *rsa.PublicKey
	Applier   Applier
	Store     SnapshotStore
	ServiceID string
	Interval  time.Duration
	Logger    *slog.Logger
	Now       func() time.Time

	mu    sync.RWMutex
	state SnapshotStatus
	etag  string
	last  *SnapshotEnvelope
}

// NewSnapshotConsumer 构造；缺省间隔 60s。
func NewSnapshotConsumer(client *Client, pub *rsa.PublicKey, applier Applier, st SnapshotStore,
	serviceID string, interval time.Duration, logger *slog.Logger) *SnapshotConsumer {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &SnapshotConsumer{
		Client: client, PublicKey: pub, Applier: applier, Store: st,
		ServiceID: serviceID, Interval: interval, Logger: logger, Now: time.Now,
	}
}

// LoadLastKnown 启动时应用本地快照；失败保持未就绪（安全类由消费方 fail-closed）。
func (c *SnapshotConsumer) LoadLastKnown(ctx context.Context) error {
	if c.Store == nil {
		return nil
	}
	rec, err := c.Store.Load(ctx, c.ServiceID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	env, err := c.verifyAndParse(rec.Payload, rec.Signature)
	if err != nil {
		c.recordError("last-known 校验失败: " + err.Error())
		return err
	}
	if env.Version < rec.Version {
		c.recordError("last-known 版本回退被拒绝")
		return fmt.Errorf("control: last-known 版本 %d 低于存储版本 %d", env.Version, rec.Version)
	}
	if err := c.Applier.ApplySnapshot(ctx, inputOf(env, rec.Payload, rec.Signature, rec.ETag)); err != nil {
		c.recordError("last-known 应用失败: " + err.Error())
		return err
	}
	c.mu.Lock()
	c.state = SnapshotStatus{
		Version: env.Version, ETag: rec.ETag, ObservedAt: env.ObservedAt,
		AppliedAt: c.now(), Ready: true, Stale: true, // 尚未联系控制面，按陈旧展示
	}
	c.etag = rec.ETag
	c.last = env
	c.mu.Unlock()
	c.Logger.Info("控制快照 last-known 已应用", "version", env.Version)
	return nil
}

// Run 启动即拉，之后按间隔条件请求；失败保持旧快照，不阻塞调用方。
func (c *SnapshotConsumer) Run(ctx context.Context) {
	c.fetchOnce(ctx)
	ticker := time.NewTicker(c.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.fetchOnce(ctx)
		}
	}
}

// fetchOnce 单轮拉取。
func (c *SnapshotConsumer) fetchOnce(ctx context.Context) {
	c.mu.RLock()
	etag := c.etag
	c.mu.RUnlock()
	headers := map[string]string{}
	if etag != "" {
		headers["If-None-Match"] = etag
	}
	status, hdr, raw, err := c.Client.Get(ctx, c.Client.Paths.Snapshot, headers)
	if err != nil {
		c.recordError(err.Error())
		c.Logger.Warn("控制快照拉取失败,保持 last-known", "error_detail", err.Error())
		return
	}
	if status == http.StatusNotModified {
		c.touch()
		return
	}
	env, err := c.verifyAndParse(raw, hdr.Get(SignatureHeader))
	if err != nil {
		c.recordError(err.Error())
		c.Logger.Warn("控制快照验签/解析失败,保持 last-known", "error_detail", err.Error())
		return
	}
	if err := c.checkVersion(env.Version); err != nil {
		c.recordError(err.Error())
		c.Logger.Warn("控制快照版本回退被拒绝", "incoming", env.Version)
		return
	}
	newETag := hdr.Get("ETag")
	if err := c.Applier.ApplySnapshot(ctx, inputOf(env, raw, hdr.Get(SignatureHeader), newETag)); err != nil {
		c.recordError("应用失败: " + err.Error())
		c.Logger.Warn("控制快照应用失败,保持旧目录", "error_detail", err.Error())
		return
	}
	c.commit(env, newETag)
	c.Logger.Info("控制快照已应用", "version", env.Version,
		"tenants", len(env.Payload.Tenants), "pricing", len(env.Payload.Pricing))
}

// checkVersion 版本单调：旧版本不得覆盖新版本。
func (c *SnapshotConsumer) checkVersion(v int64) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.state.Ready && v < c.state.Version {
		return fmt.Errorf("control: 快照版本回退 %d < %d", v, c.state.Version)
	}
	return nil
}

// commit 应用成功后更新状态与 ETag。
func (c *SnapshotConsumer) commit(env *SnapshotEnvelope, etag string) {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state = SnapshotStatus{
		Version: env.Version, ETag: etag, ObservedAt: env.ObservedAt,
		AppliedAt: now, LastContact: now, Ready: true,
	}
	c.etag = etag
	c.last = env
}

// touch 304：控制面可达，刷新接触时间、清错误并消 stale。
func (c *SnapshotConsumer) touch() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.LastContact = c.now()
	c.state.LastError = ""
	if c.state.Ready {
		c.state.Stale = false
	}
}

// recordError 记录失败事实；last-known 可用时继续服务。
func (c *SnapshotConsumer) recordError(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.LastError = msg
	c.state.Stale = true
}

// Status 返回快照消费状态；stale 由接触时间与 2×间隔（下限 2min）推导。
func (c *SnapshotConsumer) Status() SnapshotStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	st := c.state
	if st.Ready && !st.LastContact.IsZero() {
		window := 2 * c.Interval
		if window < 2*time.Minute {
			window = 2 * time.Minute
		}
		st.Stale = c.now().Sub(st.LastContact) > window
	}
	return st
}

// LastSnapshot 最近应用成功的快照（只读，不修改）。
func (c *SnapshotConsumer) LastSnapshot() *SnapshotEnvelope {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.last
}

// verifyAndParse 验签后解析信封并校验安全类字段。
func (c *SnapshotConsumer) verifyAndParse(body []byte, sig string) (*SnapshotEnvelope, error) {
	if c.PublicKey == nil {
		return nil, errors.New("control: 快照验签公钥未配置")
	}
	if err := VerifySignature(c.PublicKey, body, sig); err != nil {
		return nil, err
	}
	var env SnapshotEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("control: 快照 JSON 非法: %w", err)
	}
	if env.Version <= 0 {
		return nil, errors.New("control: 快照版本必须为正")
	}
	if env.ObservedAt.IsZero() {
		return nil, errors.New("control: 快照缺少 observed_at")
	}
	if err := ValidatePayload(env.Payload); err != nil {
		return nil, err
	}
	return &env, nil
}

// VerifySignature 校验 RSA-SHA256 PKCS#1 v1.5 裸签名（base64 标准或 URL 安全）。
func VerifySignature(pub *rsa.PublicKey, body []byte, sigB64 string) error {
	if strings.TrimSpace(sigB64) == "" {
		return errors.New("control: 快照缺少签名")
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil {
		sig, err = base64.RawURLEncoding.DecodeString(sigB64)
	}
	if err != nil {
		return errors.New("control: 快照签名非 base64")
	}
	hash := sha256.Sum256(body)
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hash[:], sig); err != nil {
		return errors.New("control: 快照签名无效")
	}
	return nil
}

// inputOf 组装 Applier 输入。
func inputOf(env *SnapshotEnvelope, raw []byte, sig, etag string) ApplyInput {
	return ApplyInput{
		Envelope: env, Raw: raw, Signature: sig, ETag: etag,
		Version: env.Version, ObservedAt: env.ObservedAt,
	}
}

// now 时钟源。
func (c *SnapshotConsumer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
