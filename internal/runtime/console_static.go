package runtime

// Console 静态托管（T2.21）：gateway 在 /ui/ 前缀托管 Console SPA 构建产物
// （console.static_dir 指向 console/dist 时启用）。鉴权豁免**精确到 /ui/
// 前缀**（静态资源不要求 token；数据面仍全部走 /v1 鉴权）——本文件是该
// 豁免的唯一实现位，登记见 gateway.go 的原生端点例外清单。

import (
	"compress/gzip"
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

// consoleCSP 是 /ui/ 静态面的 Content-Security-Policy（D4-④）。指令集按
// Console 构建产物的实际加载形态定稿（2026-09-19 对 dist/ 排查）：Vite
// 产物为外部 module script + 外部样式表（Tailwind v4 无内联样式注入），
// 数据面 fetch/流式全走同源 /v1，图标为同源 svg——无 'unsafe-inline' 面。
//   - connect-src 'self'：/v1 REST + NDJSON 流（VITE_API_BASE 指向跨源
//     控制面时需放宽本指令）；
//   - img-src 'self' data:：同源 favicon/图标，data: 为零散内联图标预留；
//   - style-src 'self'：仅外部样式表（React 运行时改 style 走 CSSOM，不受限）。
const consoleCSP = "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'"

// consoleCompressibleExt 是按扩展名的可压缩静态资产（2026-09-25 加载优化
// ——staging 实测 daemon 静态面无压缩，浏览器实传 1MB 未压缩 JS；gzip 后
// 传输约 1/4）。woff2/jpg/png 等本身已压缩的格式不在此列（再压缩无收益）。
var consoleCompressibleExt = map[string]bool{
	".js":   true,
	".css":  true,
	".html": true,
	".svg":  true,
	".json": true,
	".map":  true,
	".txt":  true,
}

// newConsoleUIHandler 构造 Console SPA 静态托管 handler（/ui/ 前缀的分派
// 目标）：
//   - 命中目录内真实文件 → 按扩展名 Content-Type 原样托管（fs.ValidPath
//     拒绝 ..、绝对路径等形态——目录外不可达，无穿越面）；
//   - 未命中（SPA 深链 /ui/apps/xyz 等）→ 回退 index.html（200，前端
//     router 接管）；
//   - 目录缺 index.html 在装配期 fail-fast（Console 未构建即配置启用属
//     配置错误，拒绝启动而非运行期 500）；
//   - 可压缩资产按 Accept-Encoding 协商 gzip（透明包装 ResponseWriter——
//     Content-Length 与压缩流长度必然不一致，压缩时删除；范围面只此静态
//     前缀，流式 API 不经此路径）。
func newConsoleUIHandler(dir string) (http.Handler, error) {
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return nil, fmt.Errorf("console.static_dir %q has no index.html (run `pnpm build` in console/ first): %w", dir, err)
	}
	fsys := os.DirFS(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CSP 对真实文件与 SPA 回退（index.html）一律生效（见 consoleCSP 注释）。
		w.Header().Set("Content-Security-Policy", consoleCSP)
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
		serveName := name
		if st, err := fs.Stat(fsys, name); err != nil || st.IsDir() {
			serveName = "index.html"
		}
		w, done := wrapConsoleGzip(w, r, serveName)
		defer done()
		//nolint:gosec // G703：serveName 已过 fs.ValidPath（拒绝 ..、绝对路径等形态）——fsys 根定，目录外不可达
		http.ServeFileFS(w, r, fsys, serveName)
	}), nil
}

// wrapConsoleGzip 对可压缩资产 + 客户端声明 gzip 的请求包装压缩写面；返回
// 的 done 在 handler 返回时冲刷 gzip 尾部（不压缩路径为 no-op）。
func wrapConsoleGzip(w http.ResponseWriter, r *http.Request, name string) (http.ResponseWriter, func()) {
	if !consoleCompressibleExt[strings.ToLower(filepath.Ext(name))] {
		return w, func() {}
	}
	if !strings.Contains(strings.ToLower(r.Header.Get("Accept-Encoding")), "gzip") {
		return w, func() {}
	}
	gzw := &consoleGzipWriter{ResponseWriter: w, gw: gzip.NewWriter(nil)}
	return gzw, func() {
		if gzw.compress {
			//nolint:errcheck // 静态资产收尾冲刷——失败只能意味着连接已断
			gzw.gw.Close()
		}
	}
}

// consoleGzipWriter 是协商后的压缩写面：WriteHeader 时定案（2xx 才压缩——
// 304 无 body、4xx 短响应无收益），压缩则删 Content-Length、声明
// Content-Encoding 并登记 Vary（缓存层按协商头分流）。
type consoleGzipWriter struct {
	http.ResponseWriter
	gw          *gzip.Writer
	compress    bool
	wroteHeader bool
}

func (c *consoleGzipWriter) WriteHeader(code int) {
	if c.wroteHeader {
		return
	}
	c.wroteHeader = true
	if code >= 200 && code < 300 {
		h := c.Header()
		h.Del("Content-Length")
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
		c.compress = true
		c.gw.Reset(c.ResponseWriter)
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *consoleGzipWriter) Write(p []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK)
	}
	if c.compress {
		return c.gw.Write(p)
	}
	return c.ResponseWriter.Write(p)
}
