// OAuth 订阅账号装配接线(oauth-subscription-lane):store 访问器适配 oauth.AccountStore
// (oauth 包不依赖 store,依赖方向与 CooldownSink 先例同构)、池凭据同步闭包、刷新器启动。
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/solvela/muniu/internal/adapter"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/oauth"
	"github.com/solvela/muniu/internal/proxy"
	"github.com/solvela/muniu/internal/store"
)

// oauthStoreAdapter 把 store 账号访问器适配为 oauth.AccountStore;行→投影在此单点转换。
type oauthStoreAdapter struct{ db *sql.DB }

func (a oauthStoreAdapter) toAccount(r store.OAuthAccountRow) oauth.Account {
	return oauth.Account{
		ID: r.ID, Status: r.Status,
		AccessToken: r.AccessToken, RefreshToken: r.RefreshToken, ExpiresAt: r.ExpiresAt,
	}
}

func (a oauthStoreAdapter) List() ([]oauth.Account, error) {
	rows, err := store.ListOAuthAccounts(a.db)
	if err != nil {
		return nil, err
	}
	out := make([]oauth.Account, 0, len(rows))
	for _, r := range rows {
		out = append(out, a.toAccount(r))
	}
	return out, nil
}

func (a oauthStoreAdapter) UpdateTokens(id, accessToken, refreshToken string, expiresAt time.Time) error {
	err := store.UpdateOAuthAccountTokens(a.db, id, accessToken, refreshToken, expiresAt, time.Now())
	if errors.Is(err, store.ErrOAuthAccountNotFound) {
		return oauth.ErrAccountNotFound
	}
	return err
}

func (a oauthStoreAdapter) SetStatus(id, status, reason string) error {
	err := store.SetOAuthAccountStatus(a.db, id, status, reason, time.Now())
	if errors.Is(err, store.ErrOAuthAccountNotFound) {
		return oauth.ErrAccountNotFound
	}
	return err
}

// syncAccountPools 池凭据同步闭包:遍历当前拓扑的 oauth 渠道,把存储侧账号状态对齐进池
// (token 原位替换 / 删除或终态成员收缩)。刷新器每轮末调用;拓扑热替换后下一轮即对齐。
func syncAccountPools(db *sql.DB, px *proxy.Proxy, logger *slog.Logger) func() {
	return func() {
		provs, pools := px.Topology()
		for _, prov := range provs {
			if prov.Auth != adapter.AuthOAuth {
				continue
			}
			pool := pools[prov.Name]
			if pool == nil {
				continue
			}
			for _, kr := range prov.Keys {
				if kr.Account == "" {
					continue // config 校验保证 oauth 渠道全账号引用,防御兜底
				}
				row, err := store.GetOAuthAccount(db, kr.Account)
				if err == nil {
					pool.SyncAccount(kr.Account, row.AccessToken, row.Status == store.OAuthStatusActive)
				} else { // 含 not found:运行中删除 → 成员收缩(禁用),留痕
					pool.SyncAccount(kr.Account, "", false)
					logger.Warn("oauth 账号引用不可用,池成员收缩",
						"provider", prov.Name, "account", kr.Account, "error_detail", err.Error())
				}
			}
		}
	}
}

// startOAuthRefresher 装配并启动刷新器,返回授权流(交管理面)与停止函数(oauth 段未
// 完整声明时两者皆 nil 且零 goroutine——声明 oauth 渠道但段缺失在 config 校验已拒绝,
// 此处是纵深防御)。
func startOAuthRefresher(ctx context.Context, cfg *config.Config, db *sql.DB, px *proxy.Proxy, logger *slog.Logger) (*oauth.Flow, func()) {
	if !cfg.OAuth.Complete() {
		return nil, nil
	}
	oauthCfg := oauth.Config{
		AuthorizeURL: cfg.OAuth.AuthorizeURL,
		TokenURL:     cfg.OAuth.TokenURL,
		ClientID:     cfg.OAuth.ClientID,
		CallbackURL:  cfg.OAuth.CallbackURL,
	}
	flow := oauth.NewFlow(oauthCfg, nil, nil)
	ref := oauth.NewRefresher(oauthCfg, oauthStoreAdapter{db: db}, flow, logger,
		oauth.RefreshTuning{}, syncAccountPools(db, px, logger))
	runCtx, cancel := context.WithCancel(ctx)
	go ref.Run(runCtx)
	logger.Info("oauth 刷新器已启动", "token_url", cfg.OAuth.TokenURL)
	return flow, cancel
}
