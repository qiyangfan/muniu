// ListKeysByTenants:绑定集收敛,空集合失败关闭,全量读取零回归。
package auth

import (
	"context"
	"testing"

	"github.com/solvela/muniu/internal/store"
)

const defaultTenantID = "01900000-0000-7000-8000-000000000001"

func TestListKeysByTenants_集合收敛与失败关闭(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	if _, _, err := CreateKey(ctx, db, "default", "默认租户key", 60, KeyOpts{}); err != nil {
		t.Fatal(err)
	}
	t2, err := store.CreateTenant(ctx, db, "tenant-b")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := CreateKey(ctx, db, "tenant-b", "b-key", 60, KeyOpts{}); err != nil {
		t.Fatal(err)
	}

	if keys, err := ListKeysByTenants(ctx, db, nil); err != nil || len(keys) != 0 {
		t.Fatalf("空集合必须失败关闭: %v %v", keys, err)
	}
	if keys, err := ListKeysByTenants(ctx, db, []string{t2}); err != nil || len(keys) != 1 || keys[0].TenantID != t2 {
		t.Fatalf("单租户过滤不符: %+v %v", keys, err)
	}
	if keys, err := ListKeysByTenants(ctx, db, []string{defaultTenantID, t2}); err != nil || len(keys) != 2 {
		t.Fatalf("多租户并集不符: %v %v", keys, err)
	}
	if keys, err := ListKeys(ctx, db); err != nil || len(keys) != 2 {
		t.Fatalf("全量列表零回归: %v %v", keys, err)
	}
}
