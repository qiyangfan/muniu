// 宿主导入模块 muniu.log 与 muniu.get_config。
// 每插件一个 wazero Runtime，宿主导入以该插件的配置和日志器闭包绑定，
// 插件只见自己的配置，天然隔离，无跨插件可见性。
package plugin

import (
	"context"
	"log/slog"

	wazero "github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// sharedCompilationCache 进程级编译缓存，多插件多版本只编译一次，不进请求热路径。
var sharedCompilationCache = wazero.NewCompilationCache()

// buildHostModule 在插件专属 runtime 内实例化 "muniu" 宿主导入模块。
func buildHostModule(
	ctx context.Context, rt wazero.Runtime, name string, cfg map[string]string, logger *slog.Logger,
) (api.Module, error) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	// 日志与配置都只经闭包访问宿主侧传入值，不向插件暴露任何文件或网络能力。
	logFn := newHostLogFn(ctx, name, logger)
	getConfigFn := newHostGetConfigFn(cfg)

	host, err := rt.NewHostModuleBuilder(HostModuleName).
		NewFunctionBuilder().WithFunc(logFn).Export("log").
		NewFunctionBuilder().WithFunc(getConfigFn).Export("get_config").
		Instantiate(ctx)
	if err != nil {
		return nil, err
	}
	return host, nil
}

// newHostLogFn muniu.log：将插件线性内存中的字节按级别透传，带插件名标签；
// 不落请求载荷，防敏感数据进日志。
func newHostLogFn(
	ctx context.Context, name string, logger *slog.Logger,
) func(context.Context, api.Module, uint32, uint32, uint32) {
	return func(_ context.Context, m api.Module, level, off, ln uint32) {
		msg, ok := m.Memory().Read(off, ln)
		if !ok {
			return
		}
		logger.Log(context.WithoutCancel(ctx), hostLogLevel(level), "插件日志",
			"plugin", name, "plugin_log", string(msg))
	}
}

// sizeToI32 把宿主侧长度按 wasm i32 位型回传；超 int32 正值域（约 2GiB）返回负值，
// 由调用方按「不存在」处置，避免负数长度被插件误判为缓冲不足请求。
func sizeToI32(n int) int32 {
	const maxI32 = 1<<31 - 1
	if n > maxI32 {
		return -1
	}
	return int32(n) //nolint:gosec // 上方已显式排除超出 int32 正值域的长度。
}

// hostLogLevel 未知 level 码降级为 debug，与 ABI 常量 LogDebug 对齐。
func hostLogLevel(level uint32) slog.Level {
	switch level {
	case LogInfo:
		return slog.LevelInfo
	case LogWarn:
		return slog.LevelWarn
	case LogError:
		return slog.LevelError
	}
	return slog.LevelDebug
}

// newHostGetConfigFn muniu.get_config：返回写入字节数，0 表示 key 不存在，
// -N 表示缓冲不足需 N 字节；非法 key 指针与写失败同样返回 0。
func newHostGetConfigFn(
	cfg map[string]string,
) func(context.Context, api.Module, uint32, uint32, uint32, uint32) int32 {
	return func(_ context.Context, m api.Module, keyOff, keyLen, valOff, valLen uint32) int32 {
		key, ok := m.Memory().Read(keyOff, keyLen)
		if !ok {
			return 0
		}
		val, exists := cfg[string(key)]
		if !exists {
			return 0
		}
		// 长度以 i32 位型回传插件；配置值受管理面长度校验约束，不会触及 int32 正值域之外。
		n := sizeToI32(len(val))
		if n < 0 {
			return 0
		}
		if len(val) > int(valLen) {
			return -n
		}
		if !m.Memory().Write(valOff, []byte(val)) {
			return 0
		}
		return n
	}
}

// newRuntime 构造插件专属 runtime：开启 context 中断支撑超时 epoch 打断；
// 实例内存硬顶以 runtime 为粒度施加，恰好与每插件隔离边界重合。
func newRuntime(memoryLimitPages uint32) wazero.Runtime {
	cfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(memoryLimitPages)
	if sharedCompilationCache != nil {
		cfg = cfg.WithCompilationCache(sharedCompilationCache)
	}
	return wazero.NewRuntimeWithConfig(context.Background(), cfg)
}

// wasiInstantiate 挂载 wasi_snapshot_preview1，零文件系统挂载、零环境注入，
// 插件不持凭证不触网；Rust wasm32-wasip1 目标默认链接 WASI 导入，因此必须挂载。
func wasiInstantiate(ctx context.Context, rt wazero.Runtime) error {
	_, err := wasi_snapshot_preview1.Instantiate(ctx, rt)
	return err
}
