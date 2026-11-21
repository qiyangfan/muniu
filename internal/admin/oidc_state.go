package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/solvela/muniu/internal/oidc"
	"github.com/solvela/muniu/internal/store"
)

// sqlStateStore 把登录中 state 落进本地库:state 只存哈希,verifier/nonce 随取出即删。
type sqlStateStore struct{ db *sql.DB }

func (s sqlStateStore) Save(
	ctx context.Context, state, verifier, nonce string, createdAt time.Time, ttl time.Duration,
) error {
	cutoff := createdAt.Add(-ttl).UTC().Format(time.RFC3339Nano)
	return store.SaveOIDCState(ctx, s.db, stateHash(state), verifier, nonce,
		createdAt.UTC().Format(time.RFC3339Nano), cutoff)
}

func (s sqlStateStore) Take(ctx context.Context, state string, now time.Time, ttl time.Duration) (
	string, string, bool, error,
) {
	cutoff := now.Add(-ttl).UTC().Format(time.RFC3339Nano)
	verifier, nonce, createdAt, ok, err := store.TakeOIDCState(ctx, s.db, stateHash(state), cutoff)
	if err != nil || !ok {
		return "", "", false, err
	}
	t, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil || now.Sub(t) > ttl {
		return "", "", false, err
	}
	return verifier, nonce, true, nil
}

func stateHash(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

var _ oidc.StateStore = sqlStateStore{}

// NewOIDCStateStore 返回落库的 state 存储;多副本/重启共享同一 SQLite 时登录不中断。
func NewOIDCStateStore(db *sql.DB) oidc.StateStore { return sqlStateStore{db: db} }
