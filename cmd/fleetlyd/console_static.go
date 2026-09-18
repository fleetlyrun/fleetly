package main

// Console 静态托管（T2.21）：gateway 在 /ui/ 前缀托管 Console SPA 构建产物
// （console.static_dir 指向 console/dist 时启用）。鉴权豁免**精确到 /ui/
// 前缀**（静态资源不要求 token；数据面仍全部走 /v1 鉴权）——本文件是该
// 豁免的唯一实现位，登记见 gateway.go 的原生端点例外清单。

import (
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// consoleUIPathPrefix 是 Console 静态托管的唯一 URL 前缀。分派面 = 豁免面：
// newRootHandler 只把该前缀的请求交给静态 handler，其余路径原样进 gateway
// mux（无 token 仍 401）。
const consoleUIPathPrefix = "/ui"

// newConsoleUIHandler 构造 Console SPA 静态托管 handler（/ui/ 前缀的分派
// 目标）：
//   - 命中目录内真实文件 → 按扩展名 Content-Type 原样托管（fs.ValidPath
//     拒绝 ..、绝对路径等形态——目录外不可达，无穿越面）；
//   - 未命中（SPA 深链 /ui/apps/xyz 等）→ 回退 index.html（200，前端
//     router 接管）；
//   - 目录缺 index.html 在装配期 fail-fast（Console 未构建即配置启用属
//     配置错误，拒绝启动而非运行期 500）。
func newConsoleUIHandler(dir string) (http.Handler, error) {
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return nil, fmt.Errorf("console.static_dir %q has no index.html (run `pnpm build` in console/ first): %w", dir, err)
	}
	fsys := os.DirFS(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, consoleUIPathPrefix)
		name = strings.TrimPrefix(name, "/")
		if name == "" {
			name = "index.html"
		}
		if !fs.ValidPath(name) {
			http.NotFound(w, r)
			return
		}
		// 目录不托管（无列表面）：目录形态与未命中同样走 SPA 回退。
		if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
			//nolint:gosec // G703：name 已过 fs.ValidPath（拒绝 ..、绝对路径等形态）——fsys 根定，目录外不可达
			http.ServeFileFS(w, r, fsys, name)
			return
		}
		http.ServeFileFS(w, r, fsys, "index.html")
	}), nil
}
