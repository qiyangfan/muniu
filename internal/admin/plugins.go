// 插件管理端点(ADR-0011,任务 5.1):列表 / 注册与版本替换 / 启停。
// 注册与替换以本地文件为载体(PUT 传 path,服务端读文件),内容 SHA-256 固定版本;
// 注册即校验(wasm magic → 编译 → ABI 版本 → plugin_start),失败 400 且既有插件不受影响。
// 替换热生效:新请求由新版本处理,在途请求在旧版本上完成;替换不改变启停状态。
// 运行态注册不持久化——重启后回落 YAML plugins 静态声明(ADR-0004 拓扑分工)。
package admin

import (
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/solvela/muniu/internal/logging"
	"github.com/solvela/muniu/internal/plugin"
	"github.com/solvela/muniu/internal/protocol"
)

// pluginPutRequest PUT /admin/v1/plugins/{name} 请求体。
type pluginPutRequest struct {
	Path           string            `json:"path"`             // wasm 二进制本地路径(必填)
	Enabled        *bool             `json:"enabled"`          // 新注册缺省 true;替换时沿用原状态(忽略本字段)
	FailOpen       bool              `json:"fail_open"`        // true = 失效放行(安全类插件务必 false)
	TimeoutMs      int64             `json:"timeout_ms"`       // 0 = 缺省 50
	MemoryLimitMiB int               `json:"memory_limit_mib"` // 0 = 缺省 64
	PoolSize       int               `json:"pool_size"`        // 0 = 缺省 8
	Config         map[string]string `json:"config"`           // get_config 键值
}

// 插件路由经 adminRouteTable 挂载(pluginsList/pluginPut/pluginPatch),不再单独注册。

func pluginsList(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Plugins == nil {
			writeJSON(w, 200, map[string]any{"plugins": []plugin.Info{}})
			return
		}
		writeJSON(w, 200, map[string]any{"plugins": d.Plugins.List()})
	}
}

func pluginPut(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Plugins == nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "plugins_disabled",
				"插件运行时未启用(装配未创建注册表)")
			return
		}
		name := strings.TrimSpace(r.PathValue("name"))
		var req pluginPutRequest
		if err := decode(r, &req); err != nil || name == "" {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "name 与 path 必填,请求体须为 JSON")
			return
		}
		if req.Path == "" {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "path 必填")
			return
		}
		// 资源硬顶与 config 层同源(单一真理源用 plugin 包常量):超限直达 wazero 会
		// panic(admin 链无 recover),负值经 uint32 换算会溢出成巨大上限——入口即拒。
		if req.TimeoutMs < 0 || req.TimeoutMs > plugin.MaxTimeoutMs {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request",
				"timeout_ms 须在 0.."+strconv.Itoa(plugin.MaxTimeoutMs))
			return
		}
		if req.MemoryLimitMiB < 0 || req.MemoryLimitMiB > plugin.MaxMemoryLimitPages/16 {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request",
				"memory_limit_mib 须在 0.."+strconv.Itoa(plugin.MaxMemoryLimitPages/16)+"(0=缺省 64)")
			return
		}
		if req.PoolSize < 0 || req.PoolSize > plugin.MaxPoolSize {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request",
				"pool_size 须在 0.."+strconv.Itoa(plugin.MaxPoolSize))
			return
		}
		bin, err := os.ReadFile(req.Path)
		if err != nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "plugin_file_unreadable", "读取插件文件失败: "+err.Error())
			return
		}
		oldSHA, _ := d.Plugins.SHA256(name)
		info, replaced, err := d.Plugins.Register(plugin.Options{
			Name:             name,
			Binary:           bin,
			Config:           req.Config,
			Enabled:          req.Enabled == nil || *req.Enabled,
			FailOpen:         req.FailOpen,
			TimeoutMs:        req.TimeoutMs,
			MemoryLimitPages: uint32(mibOrDefault(req.MemoryLimitMiB)) * 16, // 64KiB/页
			PoolSize:         req.PoolSize,
			Logger:           d.Logger,
		})
		if err != nil {
			// 拒载属业务可读错误(非 wasm/ABI 不匹配/start 失败),原样返回不外泄内部细节
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "plugin_rejected", err.Error())
			return
		}
		if replaced {
			d.Logger.Info("插件版本替换", "plugin", name,
				"old_sha256", oldSHA, "new_sha256", info.SHA256,
				"request_id", logging.RequestIDFrom(r.Context()))
		} else {
			d.Logger.Info("插件注册", "plugin", name, "sha256", info.SHA256,
				"request_id", logging.RequestIDFrom(r.Context()))
		}
		writeJSON(w, 200, map[string]any{
			"name": info.Name, "sha256": info.SHA256, "enabled": info.Enabled,
			"fail_open": info.FailOpen, "binary_bytes": info.ByteLen, "replaced": replaced,
		})
	}
}

// pluginPatchRequest PATCH /admin/v1/plugins/{name} 请求体(仅启停)。
type pluginPatchRequest struct {
	Enabled *bool `json:"enabled"`
}

func pluginPatch(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSpace(r.PathValue("name"))
		var req pluginPatchRequest
		if err := decode(r, &req); err != nil || req.Enabled == nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "bad_request", "enabled 必填(bool)")
			return
		}
		if d.Plugins == nil {
			writeAdminError(w, r, 400, protocol.TypeBadRequest, "plugins_disabled",
				"插件运行时未启用(装配未创建注册表)")
			return
		}
		if !d.Plugins.Enable(name, *req.Enabled) {
			writeAdminError(w, r, 404, protocol.TypeBadRequest, "plugin_not_found", "插件不存在: "+name)
			return
		}
		d.Logger.Info("插件启停", "plugin", name, "enabled", *req.Enabled,
			"request_id", logging.RequestIDFrom(r.Context()))
		writeJSON(w, 200, map[string]any{"name": name, "enabled": *req.Enabled})
	}
}

func mibOrDefault(mib int) int {
	if mib == 0 {
		return 64
	}
	return mib
}
