// 后台刷新器:定时扫描账号,过期前窗口内换新凭据;刷新被拒进 auth_failed 终态。
// 判定契约(oauth-account-lifecycle spec):401/403 必终态;400+invalid_grant 按 RFC 6749
// 语义同判终态(refresh token 已撤销,重试无意义);其余(网络/5xx)留待下一 tick 重探,
// 账号保持可调度直至 token 实际失效。每轮结束回调 sync 钩子(装配层注入池凭据同步)。
package oauth

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Refresher 定扫参数:tick 间隔与提前刷新窗;单 goroutine 顺序执行,单飞语义天然成立
// (无并发刷新)。凭据为小时级生命周期,顺序扫描的吞吐绰绰有余。
type Refresher struct {
	cfg      Config
	store    AccountStore
	flow     *Flow
	logger   *slog.Logger
	sync     func() // 每轮结束的池同步回调(装配注入:拓扑内账号成员凭据/可用性对齐)
	interval time.Duration
	window   time.Duration
	now      func() time.Time
}

// RefreshTuning 刷新节奏(tick 间隔 / 提前刷新窗),测试可注入压缩值。
type RefreshTuning struct {
	Interval time.Duration
	Window   time.Duration
}

// 缺省节奏:30s 扫描(删除账号的池收缩延迟上界)、过期前 30 分钟刷新窗。
const (
	defaultInterval = 30 * time.Second
	defaultWindow   = 30 * time.Minute
)

// NewRefresher 构造;logger/sync 可为 nil(nil sync = 每轮空操作)。
func NewRefresher(cfg Config, store AccountStore, flow *Flow, logger *slog.Logger, tuning RefreshTuning, sync func()) *Refresher {
	if tuning.Interval <= 0 {
		tuning.Interval = defaultInterval
	}
	if tuning.Window <= 0 {
		tuning.Window = defaultWindow
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Refresher{
		cfg: cfg, store: store, flow: flow, logger: logger, sync: sync,
		interval: tuning.Interval, window: tuning.Window, now: time.Now,
	}
}

// Run 阻塞循环;ctx 取消即退出。首 tick 前先同步一轮(启动补齐池与存储的偏差)。
func (r *Refresher) Run(ctx context.Context) {
	if r.sync != nil {
		r.sync()
	}
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

// tick 单轮扫描:刷新判定表逐账号执行,终态迁移落 store,每轮末回调 sync。
func (r *Refresher) tick(ctx context.Context) {
	accs, err := r.store.List()
	if err != nil {
		r.logger.Warn("oauth 账号扫描失败,本轮跳过", "error_detail", err.Error())
		return
	}
	now := r.now()
	for _, acc := range accs {
		if ctx.Err() != nil {
			return
		}
		// 终态账号不刷新:重新授权(管理面回写凭据)后状态回 active 才恢复
		if acc.Status != StatusActive {
			continue
		}
		if acc.ExpiresAt.Sub(now) > r.window {
			continue
		}
		pair, err := r.flow.Refresh(ctx, acc.RefreshToken)
		switch {
		case err == nil:
			if pair.ExpiresIn <= 0 {
				// 缺 expires_in = 响应形态非法(与缺 access_token 同判):按 0 回写会令
				// 过期时刻=now,每 tick 重入刷新窗形成刷新风暴(评审域二 P3)。
				r.logger.Warn("oauth 刷新响应缺 expires_in,按暂失败重探", "account", acc.ID)
				break
			}
			// RFC 6749 §6:响应未携带 refresh_token 时旧 RT 仍然有效,必须保留
			// (覆写为空 → 下一轮空凭据重探或误判终态,评审域二 P1)。
			newRT := pair.RefreshToken
			if newRT == "" {
				newRT = acc.RefreshToken
			}
			exp := now.Add(pair.ExpiresIn)
			if uerr := r.store.UpdateTokens(acc.ID, pair.AccessToken, newRT, exp); uerr != nil {
				r.logger.Warn("oauth 刷新凭据回写失败", "account", acc.ID, "error_detail", uerr.Error())
				continue
			}
			r.logger.Info("oauth 凭据已刷新", "account", acc.ID)
		case isAuthReject(err):
			reason := err.Error()
			if serr := r.store.SetStatus(acc.ID, StatusAuthFailed, reason); serr != nil && !errors.Is(serr, ErrAccountNotFound) {
				r.logger.Warn("oauth 终态迁移失败", "account", acc.ID, "error_detail", serr.Error())
				continue
			}
			r.logger.Warn("oauth 刷新被拒,账号进入终态(需重新授权)", "account", acc.ID, "reason", reason)
		default:
			// 网络/5xx/其余 4xx:下一 tick 重探(节奏即退避),账号保持可调度
			r.logger.Warn("oauth 刷新暂失败,下轮重探", "account", acc.ID, "error_detail", err.Error())
		}
	}
	if r.sync != nil {
		r.sync()
	}
}

// isAuthReject 终态判定:401/403(spec MUST)+ 400 invalid_grant(RFC 6749:凭据已撤销,
// 重试无意义)。
func isAuthReject(err error) bool {
	te, ok := err.(*TokenError)
	if !ok {
		return false
	}
	if te.Status == 401 || te.Status == 403 {
		return true
	}
	return te.Status == 400 && te.GrantType == "invalid_grant"
}
