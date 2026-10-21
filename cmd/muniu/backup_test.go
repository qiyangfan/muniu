package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/solvela/muniu/internal/config"
)

// TestDefaultBackupDirShape 缺省目录形态 <data>/backups/muniu-backup-<UTC>。
func TestDefaultBackupDirShape(t *testing.T) {
	got := defaultBackupDir("/var/lib/muniu/data/muniu.db",
		time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	want := "/var/lib/muniu/data/backups/muniu-backup-20260930T120000Z"
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}

// TestBackupSubArgs 子命令判定，容忍 -c 前置；首个位置参数非 backup/restore 即否。
func TestBackupSubArgs(t *testing.T) {
	cases := []struct {
		args []string
		want bool
		rest string
	}{
		{[]string{"muniu"}, false, ""},
		{[]string{"muniu", "backup"}, true, "backup"},
		{[]string{"muniu", "-c", "x.yaml", "restore", "dir"}, true, "restore"},
		{[]string{"muniu", "keys", "list"}, false, ""},
		{[]string{"muniu", "-c", "x.yaml", "backup", "--output", "d"}, true, "backup"},
	}
	for _, c := range cases {
		rest, ok := backupSubArgs(c.args)
		if ok != c.want || (ok && rest[0] != c.rest) {
			t.Fatalf("backupSubArgs(%v) = (%v,%v), want (%v,%v)", c.args, rest, ok, c.rest, c.want)
		}
	}
}

// TestBackupMainUsage 无参与未知子命令均返回退出码 2，表示用法错误。
func TestBackupMainUsage(t *testing.T) {
	var out, errOut strings.Builder
	if code := backupMain(nil, nil, &out, &errOut); code != 2 {
		t.Fatalf("无参 code = %d, want 2", code)
	}
	if code := backupMain(nil, []string{"frobnicate"}, &out, &errOut); code != 2 {
		t.Fatalf("未知子命令 code = %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "用法") {
		t.Fatalf("应输出用法: %q", errOut.String())
	}
}

// TestRestoreCmdMixedOrder 验证位置参数与选项可以任意混排。
func TestRestoreCmdMixedOrder(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "nonexistent-backup-dir")
	cfg := &config.Config{}
	// 混排加坏目录：参数解析须通过，失败只可能来自 restore 本体的目录不存在。
	var out, errOut strings.Builder
	code := runRestoreCmd(cfg, []string{bad, "--target", filepath.Join(dir, "x.db"), "--force"},
		&out, &errOut)
	if code != 1 {
		t.Fatalf("code = %d, want 1(restore 本体失败)", code)
	}
	if !strings.Contains(errOut.String(), "manifest") {
		t.Fatalf("应到达 restore 本体(缺 manifest),实际: %q", errOut.String())
	}
	// 两个位置参数仍须拒。
	code = runRestoreCmd(cfg, []string{bad, "extra"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("双位置参数 code = %d, want 2", code)
	}
}
