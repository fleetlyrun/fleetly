// Package console 承载 Console 静态产物面（F2.6/ADR-0044）：vite 构建
// 产物（dist/，生成纪律同 genproto——committed + CI 漂移门禁）go:embed
// 进 fleetlyd，由 REST gateway 同端口静态服务。零私有服务端面：本包只
// 做静态文件与 SPA fallback，无任何 Console 专属端点（架构钉形——
// Console 只消费公共 REST API）。
package console

import (
	"embed"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var distFS embed.FS

// assetMaxAge 是 hashed 产物的不可变缓存窗（文件名带内容哈希，内容变更
// 即换名——index.html 以 no-cache 对冲发版）。
const assetMaxAge = 365 * 24 * time.Hour

// Mount 把 Console 静态面挂在非 /v1 路径上：/v1/* 原样交给 next（REST
// API 面零变化，ADR-0044 决策 3），其余路径服务产物文件、未命中回
// index.html（SPA fallback——hash 路由在浏览器解析，服务端无路由面）。
func Mount(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1" || strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		serveStatic(w, r)
	})
}

// Handler 是纯静态形态（无 API 旁路；测试与工具面入口）。
func Handler() http.Handler {
	return http.HandlerFunc(serveStatic)
}

// serveStatic 服务一个静态请求：GET/HEAD 之外的动词 405；命中产物文件
// 即服务（assets 不可变缓存）；目录、index.html 与未命中路径一律回
// index.html（no-cache）。路径经上层 ServeMux 清洗 + embed FS 自身拒绝
// ..，无穿越面。
func serveStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "the console serves static content only", http.StatusMethodNotAllowed)
		return
	}
	if upath := strings.TrimPrefix(r.URL.Path, "/"); upath != "" {
		name := path.Join("dist", upath)
		if file, err := distFS.Open(name); err == nil {
			defer file.Close() //nolint:errcheck // 只读句柄，进程内 embed
			if stat, err := file.Stat(); err == nil && !stat.IsDir() && upath != "index.html" {
				seeker, ok := file.(io.ReadSeeker)
				if !ok {
					http.Error(w, "console asset is not seekable", http.StatusInternalServerError)
					return
				}
				w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, immutable", int64(assetMaxAge/time.Second)))
				http.ServeContent(w, r, upath, time.Time{}, seeker)
				return
			}
		}
	}
	serveIndex(w, r)
}

// serveIndex 以 no-cache 服务 SPA 入口（发版即取新）。
func serveIndex(w http.ResponseWriter, r *http.Request) {
	file, err := distFS.Open("dist/index.html")
	if err != nil {
		http.Error(w, "console bundle is missing", http.StatusInternalServerError)
		return
	}
	defer file.Close() //nolint:errcheck // 只读句柄，进程内 embed
	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		http.Error(w, "console index is not seekable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, seeker)
}
