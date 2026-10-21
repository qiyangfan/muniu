package adapter_test

import (
	"sort"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	_ "github.com/solvela/muniu/internal/adapter/bridge" // 接线 anthropic 桥接工厂，与 cmd/muniu/main.go 同方式
)

// TestTypes_每个值都可被For构造 验证 Types 返回的每个类型都有对应工厂。
func TestTypes_每个值都可被For构造(t *testing.T) {
	for _, typ := range adapter.Types() {
		ad, err := adapter.For(typ, "https://example.com/v1", "sk-test")
		if err != nil {
			t.Fatalf("Types() 列出的 %q 无法被 For 构造: %v", typ, err)
		}
		if ad == nil {
			t.Fatalf("Types() 列出的 %q 构造出 nil 适配器", typ)
		}
	}
}

// TestTypes_排序且无重复 验证类型列表稳定且不含重复项。
func TestTypes_排序且无重复(t *testing.T) {
	got := adapter.Types()
	if len(got) == 0 {
		t.Fatal("值域集合不得为空")
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("Types() 必须排序返回,实际: %v", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Fatalf("Types() 含重复项 %q: %v", got[i], got)
		}
	}
}
