// 测试辅助：临时库缓存实例与指针构造。
package semantic

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/store"
)

func newCacheForTest(t *testing.T, opts Options, embedder Embedder) (*Cache, error) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "sc.db"))
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = db.Close() })
	// 语义缓存行有 tenants 外键，先种子测试租户 t1/t2。
	now := time.Now().UTC().Format(time.RFC3339)
	for _, tn := range []string{"t1", "t2"} {
		if _, err := db.ExecContext(context.Background(), `INSERT INTO tenants (id, name, created_at) VALUES (?,?,?)`, tn, tn, now); err != nil {
			return nil, err
		}
	}
	return NewCache(context.Background(), db, opts, embedder)
}

func ptrFloat(f float64) *float64 { return &f }
