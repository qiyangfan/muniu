// OAuth 账号访问器契约:建读往返、刷新回写恢复态、状态迁移与删除的行数语义。
package store

import (
	"errors"
	"testing"
	"time"
)

func TestOAuthAccount_建读往返(t *testing.T) {
	db := openTestStore(t)
	now := time.Now()
	exp := now.Add(time.Hour).Truncate(time.Second)

	if err := CreateOAuthAccount(db, OAuthAccountRow{
		ID: "acc-1", ProviderType: "claude", Status: OAuthStatusActive,
		AccessToken: "at-1", RefreshToken: "rt-1", ExpiresAt: exp,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("建账号: %v", err)
	}
	// id 冲突显式报错,不静默覆盖;且必须落 ErrOAuthAccountExists 哨兵
	// (管理面据此区分 409 与 500,其余 DB 故障不得伪装成「已存在」)
	if err := CreateOAuthAccount(db, OAuthAccountRow{
		ID: "acc-1", ProviderType: "claude", Status: OAuthStatusActive,
		AccessToken: "x", RefreshToken: "x", ExpiresAt: exp, CreatedAt: now, UpdatedAt: now,
	}); !errors.Is(err, ErrOAuthAccountExists) {
		t.Fatalf("重复 id 应 ErrOAuthAccountExists, got %v", err)
	}
	got, err := GetOAuthAccount(db, "acc-1")
	if err != nil {
		t.Fatalf("读账号: %v", err)
	}
	if got.AccessToken != "at-1" || got.RefreshToken != "rt-1" ||
		!got.ExpiresAt.Equal(exp) || got.Status != OAuthStatusActive ||
		got.ProviderType != "claude" {
		t.Fatalf("往返失真: %+v", got)
	}
	if _, err := GetOAuthAccount(db, "nope"); !errors.Is(err, ErrOAuthAccountNotFound) {
		t.Fatalf("不存在应 ErrOAuthAccountNotFound, got %v", err)
	}

	// 列表稳定序
	if err := CreateOAuthAccount(db, OAuthAccountRow{
		ID: "acc-0", ProviderType: "claude", Status: OAuthStatusActive,
		AccessToken: "a", RefreshToken: "r", ExpiresAt: exp, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("建第二账号: %v", err)
	}
	list, err := ListOAuthAccounts(db)
	if err != nil || len(list) != 2 {
		t.Fatalf("列表: %v len=%d", err, len(list))
	}
	if list[0].ID != "acc-0" || list[1].ID != "acc-1" {
		t.Fatalf("列表序失真: %s, %s", list[0].ID, list[1].ID)
	}
}

func TestOAuthAccount_刷新回写与终态(t *testing.T) {
	db := openTestStore(t)
	now := time.Now()
	exp := now.Add(time.Hour).Truncate(time.Second)
	if err := CreateOAuthAccount(db, OAuthAccountRow{
		ID: "acc-1", ProviderType: "claude", Status: OAuthStatusAuthFailed, StatusReason: "stale",
		AccessToken: "old", RefreshToken: "rt-old", ExpiresAt: exp, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("建账号: %v", err)
	}

	// 刷新回写:新凭据 + 回 active 清原因(重新授权恢复同一入口)
	newExp := now.Add(2 * time.Hour).Truncate(time.Second)
	if err := UpdateOAuthAccountTokens(db, "acc-1", "at-2", "rt-2", newExp, now); err != nil {
		t.Fatalf("回写: %v", err)
	}
	got, _ := GetOAuthAccount(db, "acc-1")
	if got.AccessToken != "at-2" || got.RefreshToken != "rt-2" || !got.ExpiresAt.Equal(newExp) ||
		got.Status != OAuthStatusActive || got.StatusReason != "" {
		t.Fatalf("回写后失真: %+v", got)
	}

	// 终态迁移
	if err := SetOAuthAccountStatus(db, "acc-1", OAuthStatusAuthFailed, "refresh 401", now); err != nil {
		t.Fatalf("终态: %v", err)
	}
	got, _ = GetOAuthAccount(db, "acc-1")
	if got.Status != OAuthStatusAuthFailed || got.StatusReason != "refresh 401" {
		t.Fatalf("终态失真: %+v", got)
	}
	if err := SetOAuthAccountStatus(db, "nope", OAuthStatusActive, "", now); !errors.Is(err, ErrOAuthAccountNotFound) {
		t.Fatalf("未知账号迁移应 NotFound, got %v", err)
	}
	if err := UpdateOAuthAccountTokens(db, "nope", "a", "r", newExp, now); !errors.Is(err, ErrOAuthAccountNotFound) {
		t.Fatalf("未知账号回写应 NotFound, got %v", err)
	}
}

func TestOAuthAccount_删除(t *testing.T) {
	db := openTestStore(t)
	now := time.Now()
	if err := CreateOAuthAccount(db, OAuthAccountRow{
		ID: "acc-1", ProviderType: "claude", Status: OAuthStatusActive,
		AccessToken: "a", RefreshToken: "r", ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("建账号: %v", err)
	}
	if err := DeleteOAuthAccount(db, "acc-1"); err != nil {
		t.Fatalf("删除: %v", err)
	}
	if _, err := GetOAuthAccount(db, "acc-1"); !errors.Is(err, ErrOAuthAccountNotFound) {
		t.Fatalf("删除后应 NotFound, got %v", err)
	}
	if err := DeleteOAuthAccount(db, "acc-1"); !errors.Is(err, ErrOAuthAccountNotFound) {
		t.Fatalf("重复删除应 NotFound, got %v", err)
	}
}
