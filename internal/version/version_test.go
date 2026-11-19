package version

import "testing"

// 未注入 -ldflags 的构建必须保留可读的 dev 标识,空值会让 /version 与日志失去区分度。
func TestVersionDefault(t *testing.T) {
	t.Parallel()
	if Version == "" {
		t.Fatal("Version 默认值被清空,应保持 dev")
	}
}
