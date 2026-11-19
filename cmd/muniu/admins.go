package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/solvela/muniu/internal/auth"
	"github.com/solvela/muniu/internal/config"
	"github.com/solvela/muniu/internal/store"
	"golang.org/x/sys/unix"
)

func adminsSubArgs(args []string) ([]string, bool) {
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "-c":
			i++
		case args[i] == "admins":
			return args[i+1:], true
		case strings.HasPrefix(args[i], "-"):
		default:
			return nil, false
		}
	}
	return nil, false
}

func runAdmins(path string, args []string) int {
	cfg, err := config.Load(path)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return adminsMain(cfg, args, os.Stdin, os.Stdout, os.Stderr)
}

func adminsMain(cfg *config.Config, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprintln(stderr,
			"用法: muniu admins add --username <名字> | list | disable <用户名> | enable <用户名> | passwd <用户名>")
		return 2
	}
	db, err := store.Open(context.Background(), cfg.Storage.SQLite.Path)
	if err != nil {
		return reportErr(stderr, err)
	}
	defer func() { _ = db.Close() }()
	svc, err := auth.NewAdminService(context.Background(), db)
	if err != nil {
		return reportErr(stderr, err)
	}
	switch args[0] {
	case "list":
		return adminsList(db, stdout, stderr)
	case "add":
		return adminsAdd(svc, args[1:], stdin, stdout, stderr)
	case "disable", "enable", "passwd":
		return adminsMutate(svc, db, args, stdin, stdout, stderr)
	default:
		_, _ = fmt.Fprintln(stderr, "未知 admins 子命令")
		return 2
	}
}

// reportErr 打印错误并以退出码 1 返回。
func reportErr(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintln(stderr, err)
	return 1
}

// adminsList 列出管理员。
func adminsList(db *sql.DB, stdout, stderr io.Writer) int {
	users, err := store.ListAdminUsers(context.Background(), db)
	if err != nil {
		return reportErr(stderr, err)
	}
	for _, u := range users {
		_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\n", u.ID, u.Username, u.Status)
	}
	return 0
}

// adminsAdd 创建管理员（交互读口令）。
func adminsAdd(svc *auth.AdminService, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	username := fs.String("username", "", "管理员用户名")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *username == "" || fs.NArg() != 0 {
		_, _ = fmt.Fprintln(stderr, "须提供 --username")
		return 2
	}
	password, err := readAdminPassword(stdin, stderr)
	if err != nil {
		return reportErr(stderr, err)
	}
	u, err := svc.Create(context.Background(), *username, password)
	if err != nil {
		return reportErr(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "已创建管理员 %s(%s)\n", u.Username, u.ID)
	return 0
}

// adminsMutate 启停/改密。
func adminsMutate(svc *auth.AdminService, db *sql.DB, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		_, _ = fmt.Fprintln(stderr, "须提供一个管理员用户名")
		return 2
	}
	u, err := store.GetAdminUser(context.Background(), db, args[1])
	if err != nil {
		return reportErr(stderr, err)
	}
	disabled, password, err := adminMutation(args[0], stdin, stderr)
	if err != nil {
		return reportErr(stderr, err)
	}
	if err := svc.Patch(context.Background(), u.ID, disabled, password); err != nil {
		return reportErr(stderr, err)
	}
	_, _ = fmt.Fprintf(stdout, "管理员 %s 已更新\n", u.Username)
	return 0
}

// adminMutation 解析改动载荷：passwd 读口令，disable/enable 置状态。
func adminMutation(cmd string, stdin io.Reader, stderr io.Writer) (*bool, string, error) {
	if cmd != "passwd" {
		v := cmd == "disable"
		return &v, "", nil
	}
	password, err := readAdminPassword(stdin, stderr)
	if err != nil {
		return nil, "", err
	}
	if password == "" {
		return nil, "", fmt.Errorf("密码不可为空")
	}
	return nil, password, nil
}

// readAdminPassword tty 禁回显且双次确认；信号取消与读入共用同步路径，确保恢复终端。
func readAdminPassword(reader io.Reader, stderr io.Writer) (string, error) {
	reader, restore, tty, err := adminPasswordReader(reader)
	if err != nil {
		return "", err
	}
	defer restore()
	return readPasswordTwice(bufio.NewReaderSize(reader, 4098), tty, stderr)
}

// adminPasswordReader 若输入是终端则关闭回显并返回可取消的读取器；
// 非终端或 ioctl 不可用时原样返回。
func adminPasswordReader(reader io.Reader) (io.Reader, func(), bool, error) {
	file, ok := reader.(*os.File)
	if !ok {
		return reader, func() {}, false, nil
	}
	// ioctl 失败（非终端或平台不支持）即视为无回显可关，按原样返回。
	state, _ := unix.IoctlGetTermios(int(file.Fd()), ttyGetRequest)
	if state == nil {
		return reader, func() {}, false, nil
	}
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
	changed := *state
	changed.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(int(file.Fd()), ttySetRequest, &changed); err != nil {
		signal.Stop(interrupted)
		return nil, nil, false, err
	}
	restore := func() {
		signal.Stop(interrupted)
		_ = unix.IoctlSetTermios(int(file.Fd()), ttySetRequest, state)
	}
	return adminTTYReader{file: file, interrupted: interrupted}, restore, true, nil
}

// readPasswordTwice 终端下双次输入并比对；管道输入只读一次。
func readPasswordTwice(read *bufio.Reader, tty bool, stderr io.Writer) (string, error) {
	if tty {
		_, _ = fmt.Fprint(stderr, "口令: ")
	}
	first, err := readPasswordLine(read)
	if err != nil {
		return "", err
	}
	if !tty {
		return first, nil
	}
	_, _ = fmt.Fprint(stderr, "\n再次输入口令: ")
	second, err := readPasswordLine(read)
	_, _ = fmt.Fprintln(stderr)
	if err != nil {
		return "", err
	}
	if first != second {
		return "", fmt.Errorf("两次口令不一致")
	}
	return first, nil
}

// readPasswordLine 读取一行口令并做长度上限与换行裁剪。
func readPasswordLine(read *bufio.Reader) (string, error) {
	bytes, err := read.ReadSlice('\n')
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("口令过长或输入不可读: %w", err)
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(bytes), "\n"), "\r")
	if len(value) > 4096 {
		return "", fmt.Errorf("口令不可超过 4096 字节")
	}
	return value, nil
}

// 终端 canonical 模式仅在线就绪后读入；短轮询让取消不依赖阻塞设备的 Close 语义。
type adminTTYReader struct {
	file        *os.File
	interrupted <-chan os.Signal
}

func (r adminTTYReader) Read(p []byte) (int, error) {
	//nolint:gosec // fd 由内核分配,值域远小于 int32 上限
	fds := []unix.PollFd{{Fd: int32(r.file.Fd()), Events: unix.POLLIN}}
	for {
		select {
		case <-r.interrupted:
			return 0, fmt.Errorf("口令输入已取消")
		default:
		}
		n, err := unix.Poll(fds, 100)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n > 0 {
			return r.file.Read(p)
		}
	}
}
