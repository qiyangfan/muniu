package main

import (
	"bufio"
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
		_, _ = fmt.Fprintln(stderr, "用法: muniu admins add --username <名字> | list | disable <用户名> | enable <用户名> | passwd <用户名>")
		return 2
	}
	db, err := store.Open(cfg.Storage.SQLite.Path)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() { _ = db.Close() }()
	svc, err := auth.NewAdminService(db)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	fail := func(err error) int { _, _ = fmt.Fprintln(stderr, err); return 1 }
	switch args[0] {
	case "list":
		users, err := store.ListAdminUsers(db)
		if err != nil {
			return fail(err)
		}
		for _, u := range users {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\n", u.ID, u.Username, u.Status)
		}
		return 0
	case "add":
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		fs.SetOutput(stderr)
		username := fs.String("username", "", "管理员用户名")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		if *username == "" || fs.NArg() != 0 {
			_, _ = fmt.Fprintln(stderr, "须提供 --username")
			return 2
		}
		password, err := readAdminPassword(stdin, stderr)
		if err != nil {
			return fail(err)
		}
		u, err := svc.Create(*username, password)
		if err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(stdout, "已创建管理员 %s(%s)\n", u.Username, u.ID)
		return 0
	case "disable", "enable", "passwd":
		if len(args) != 2 {
			_, _ = fmt.Fprintln(stderr, "须提供一个管理员用户名")
			return 2
		}
		u, err := store.GetAdminUser(db, args[1])
		if err != nil {
			return fail(err)
		}
		var disabled *bool
		password := ""
		if args[0] == "passwd" {
			password, err = readAdminPassword(stdin, stderr)
			if err == nil && password == "" {
				err = fmt.Errorf("密码不可为空")
			}
			if err != nil {
				return fail(err)
			}
		} else {
			v := args[0] == "disable"
			disabled = &v
		}
		if err := svc.Patch(u.ID, disabled, password); err != nil {
			return fail(err)
		}
		_, _ = fmt.Fprintf(stdout, "管理员 %s 已更新\n", u.Username)
		return 0
	default:
		_, _ = fmt.Fprintln(stderr, "未知 admins 子命令")
		return 2
	}
}

// readAdminPassword tty 禁回显且双次确认；信号取消与读入共用同步路径，确保恢复终端。
func readAdminPassword(reader io.Reader, stderr io.Writer) (string, error) {
	file, ok := reader.(*os.File)
	tty := false
	restore := func() {}
	if ok {
		state, err := unix.IoctlGetTermios(int(file.Fd()), ttyGetRequest)
		if err == nil {
			tty = true
			interrupted := make(chan os.Signal, 1)
			signal.Notify(interrupted, os.Interrupt, syscall.SIGTERM)
			defer signal.Stop(interrupted)
			reader = adminTTYReader{file: file, interrupted: interrupted}
			changed := *state
			changed.Lflag &^= unix.ECHO
			if err := unix.IoctlSetTermios(int(file.Fd()), ttySetRequest, &changed); err != nil {
				return "", err
			}
			restore = func() { _ = unix.IoctlSetTermios(int(file.Fd()), ttySetRequest, state) }
		}
	}
	defer restore()
	read := bufio.NewReaderSize(reader, 4098)
	line := func() (string, error) {
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
	readBoth := func() (string, error) {
		if tty {
			_, _ = fmt.Fprint(stderr, "口令: ")
		}
		first, err := line()
		if err != nil {
			return "", err
		}
		if tty {
			_, _ = fmt.Fprint(stderr, "\n再次输入口令: ")
			second, err := line()
			_, _ = fmt.Fprintln(stderr)
			if err != nil {
				return "", err
			}
			if first != second {
				return "", fmt.Errorf("两次口令不一致")
			}
		}
		return first, nil
	}
	return readBoth()
}

// 终端 canonical 模式仅在线就绪后读入；短轮询让取消不依赖阻塞设备的 Close 语义。
type adminTTYReader struct {
	file        *os.File
	interrupted <-chan os.Signal
}

func (r adminTTYReader) Read(p []byte) (int, error) {
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
