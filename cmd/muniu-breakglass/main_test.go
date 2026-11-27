// break-glass CLI：列表与必填参数负向（不触存储）。
package main

import "testing"

func TestCLI_列表与必填校验(t *testing.T) {
	if code := run([]string{"-list"}); code != 0 {
		t.Fatalf("list 应成功: %d", code)
	}
	if code := run(nil); code != 2 {
		t.Fatalf("缺参数应退出 2: %d", code)
	}
	if code := run([]string{"-db", "x", "-credential", "y", "-op", "z"}); code != 2 {
		t.Fatalf("缺 reason 应退出 2: %d", code)
	}
	if code := run([]string{"-db", "x", "-credential", "y", "-op", "z", "-reason", "r",
		"-params", "{"}); code != 2 {
		t.Fatalf("非法 params 应退出 2: %d", code)
	}
}
