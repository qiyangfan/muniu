// 宿主导入模块(ABI v0 §3):muniu.log 与 muniu.get_config。
// 每插件一个 wazero Runtime,宿主导入以该插件的配置/日志器闭包绑定——
// 插件只见自己的配置,天然隔离,无跨插件可见性。
package plugin

import (
	"context"
	"log/slog"

	wazero "github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// sharedCompilationCache 进程级编译缓存:多插件/多版本共享编译产物,注册期编译一次,
// 不进请求热路径(design Risks:AOT 冷启动常驻化)。
var sharedCompilationCache = wazero.NewCompilationCache()

// buildHostModule 在插件专属 runtime 内实例化 "muniu" 宿主导入模块。
func buildHostModule(ctx context.Context, rt wazero.Runtime, name string, cfg map[string]string, logger *slog.Logger) (api.Module, error) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	// log:插件线性内存中的字节按级别透传(带插件名标签;不落请求载荷,防敏感数据进日志)
	logFn := func(_ context.Context, m api.Module, level, off, ln uint32) {
		msg, ok := m.Memory().Read(off, ln)
		if !ok {
			return
		}
		lvl := slog.LevelDebug
		switch level {
		case LogInfo:
			lvl = slog.LevelInfo
		case LogWarn:
			lvl = slog.LevelWarn
		case LogError:
			lvl = slog.LevelError
		}
		logger.Log(context.WithoutCancel(ctx), lvl, "插件日志",
			"plugin", name, "plugin_log", string(msg))
	}

	// get_config:返回写入字节数;0=key 不存在;-N=缓冲不足需 N 字节(plugin-abi.md §3)
	getConfigFn := func(_ context.Context, m api.Module, keyOff, keyLen, valOff, valLen uint32) int32 {
		key, ok := m.Memory().Read(keyOff, keyLen)
		if !ok {
			return 0
		}
		val, exists := cfg[string(key)]
		if !exists {
			return 0
		}
		if len(val) > int(valLen) {
			return -int32(len(val))
		}
		if !m.Memory().Write(valOff, []byte(val)) {
			return 0
		}
		return int32(len(val))
	}

	host, err := rt.NewHostModuleBuilder(HostModuleName).
		NewFunctionBuilder().WithFunc(logFn).Export("log").
		NewFunctionBuilder().WithFunc(getConfigFn).Export("get_config").
		Instantiate(ctx)
	if err != nil {
		return nil, err
	}
	return host, nil
}

// newRuntime 构造插件专属 runtime:开启 context 中断(超时 epoch 中断的基座)、
// 实例内存硬顶(wazero 以 runtime 为粒度施加,恰好与每插件隔离边界重合)、
// 共享进程级编译缓存(多插件多版本只编一次)。
func newRuntime(memoryLimitPages uint32) wazero.Runtime {
	cfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(memoryLimitPages)
	if sharedCompilationCache != nil {
		cfg = cfg.WithCompilationCache(sharedCompilationCache)
	}
	return wazero.NewRuntimeWithConfig(context.Background(), cfg)
}

// wasiInstantiate 挂载 wasi_snapshot_preview1(零文件系统挂载、零环境注入——
// 插件不持凭证不触网是 spec 硬性要求;Rust wasm32-wasip1 目标默认链接 WASI 导入)。
func wasiInstantiate(ctx context.Context, rt wazero.Runtime) error {
	_, err := wasi_snapshot_preview1.Instantiate(ctx, rt)
	return err
}
