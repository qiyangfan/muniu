package store

import (
	"context"
	"database/sql"
	"errors"
)

// SaveOIDCState 落库一次登录中的 state,并顺带清理早于 cutoff 的过期行;同键覆盖。
func SaveOIDCState(ctx context.Context, db *sql.DB, stateHash, verifier, nonce, createdAt, cutoff string) error {
	if _, err := db.ExecContext(ctx, `DELETE FROM oidc_pending_states WHERE created_at < ?`, cutoff); err != nil {
		return err
	}
	_, err := db.ExecContext(ctx, `INSERT INTO oidc_pending_states(state_hash,verifier,nonce,created_at)
		VALUES(?,?,?,?) ON CONFLICT(state_hash) DO UPDATE SET
		verifier=excluded.verifier, nonce=excluded.nonce, created_at=excluded.created_at`,
		stateHash, verifier, nonce, createdAt)
	return err
}

// TakeOIDCState 单次取出并删除;早于 cutoff 的存量一并清掉,返回 ok=false。
func TakeOIDCState(ctx context.Context, db *sql.DB, stateHash, cutoff string) (
	verifier, nonce, createdAt string, ok bool, err error,
) {
	if _, err = db.ExecContext(ctx, `DELETE FROM oidc_pending_states WHERE created_at < ?`, cutoff); err != nil {
		return "", "", "", false, err
	}
	row := db.QueryRowContext(ctx, `DELETE FROM oidc_pending_states WHERE state_hash=?
		RETURNING verifier,nonce,created_at`, stateHash)
	switch err = row.Scan(&verifier, &nonce, &createdAt); {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", "", false, nil
	case err != nil:
		return "", "", "", false, err
	}
	return verifier, nonce, createdAt, true, nil
}

// CleanOIDCStates 启动期清扫;登录中 state 超过 TTL 即不可用,删除不影响正确性。
func CleanOIDCStates(ctx context.Context, db *sql.DB, cutoff string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM oidc_pending_states WHERE created_at < ?`, cutoff)
	return err
}
