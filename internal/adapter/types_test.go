package adapter_test

import (
	"sort"
	"testing"

	"github.com/solvela/muniu/internal/adapter"
	_ "github.com/solvela/muniu/internal/adapter/bridge" // 接线 anthropic 桥接工厂(与 cmd/muniu/main.go 同方式)
)

// TestTypes_每个值都可被For构造 值域单源的防漂移钉子:
// 列进 Types() 的类型必须真能构造出来,否则「清单有、实现没有」当场变红。
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

// TestTypes_排序且无重复 报错文案由该集合渲染:顺序抖动会让提示不稳定,重复项会印出重复取值。
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
