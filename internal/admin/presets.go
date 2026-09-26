// 官方端点预设目录端点:只读参考数据,无 Deps 依赖、无运行时行为(目录语义见 internal/presets)。
package admin

import (
	"net/http"

	"github.com/solvela/muniu/internal/presets"
)

func providerPresets(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"presets": presets.Catalog()})
	}
}
