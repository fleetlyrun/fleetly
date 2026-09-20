package runtime

// 根路径引导页（landing page）：GET / 的原生静态端点——裸访问控制面端口
// 时不再落 grpc-gateway 的 404 NotFound JSON，而是给出入口指引（Console /
// REST / healthz）。例外清单登记见 gateway.go「原生端点例外清单」；豁免面
// = 分派面 = 精确 GET/HEAD /（其余方法与其余路径一律不进本 handler）。

import (
	"io"
	"net/http"
)

// landingCSP 是引导页的 Content-Security-Policy：纯静态内容，无脚本/图片/
// 连接面——default-src 'none' 封死一切，仅放行内联 <style>（页面样式即
// 内容，非注入面）。
const landingCSP = "default-src 'none'; style-src 'unsafe-inline'"

// landingHTML 是引导页唯一内容。纯静态、无脚本、无外部资源；文案 = 操作者
// 上手三件事——打开 Console、持 token 调 REST、看健康端点。token 创建命令
// 与 Console 登录页口径一致（fleetly tokens create --scopes admin）。
const landingHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>fleetly</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; margin: 0; }
  body {
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 1.5rem;
    font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
    background: #f7f7f8;
    color: #18181b;
  }
  .card {
    width: 100%;
    max-width: 34rem;
    background: #ffffff;
    border: 1px solid #e4e4e7;
    border-radius: 12px;
    padding: 1.75rem 2rem;
  }
  .brand { display: flex; align-items: center; gap: .65rem; margin-bottom: 1rem; }
  .mark {
    width: 2rem; height: 2rem; flex: none;
    display: flex; align-items: center; justify-content: center;
    background: #18181b; color: #fafafa;
    border-radius: 6px; font-weight: 700; font-size: 1rem;
  }
  h1 { font-size: 1.05rem; font-weight: 600; letter-spacing: .01em; }
  .sub { font-size: .72rem; text-transform: uppercase; letter-spacing: .12em; color: #71717a; }
  p.desc { font-size: .875rem; color: #52525b; margin: .4rem 0 1.25rem; }
  ul { list-style: none; padding: 0; }
  li { display: flex; align-items: baseline; justify-content: space-between; gap: 1rem;
       padding: .7rem 0; border-top: 1px solid #f0f0f2; font-size: .875rem; }
  .label { color: #52525b; flex: none; }
  .value { text-align: right; }
  a { color: #18181b; font-weight: 600; text-decoration: none;
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: .8rem; }
  a:hover { text-decoration: underline; }
  code {
    font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    font-size: .75rem; background: #f4f4f5; border-radius: 4px; padding: .1rem .35rem;
  }
  .hint { margin-top: 1.25rem; font-size: .75rem; line-height: 1.6; color: #71717a; }
  @media (prefers-color-scheme: dark) {
    body { background: #0a0a0b; color: #fafafa; }
    .card { background: #131316; border-color: #2b2b30; }
    .sub, .hint { color: #8f8f98; }
    p.desc, .label { color: #b6b6bf; }
    li { border-top-color: #232327; }
    a { color: #fafafa; }
    code { background: #26262b; }
    .mark { background: #fafafa; color: #18181b; }
  }
</style>
</head>
<body>
<main class="card">
  <div class="brand">
    <span class="mark">f</span>
    <div>
      <h1>fleetly</h1>
      <div class="sub">control plane</div>
    </div>
  </div>
  <p class="desc">Single-node container platform. Start here:</p>
  <ul>
    <li><span class="label">Console (web UI)</span>
        <span class="value"><a href="/ui/">/ui/</a></span></li>
    <li><span class="label">REST API (Bearer token)</span>
        <span class="value"><a href="/v1/apps">/v1</a></span></li>
    <li><span class="label">Liveness</span>
        <span class="value"><a href="/healthz/liveness">/healthz/liveness</a></span></li>
    <li><span class="label">Readiness</span>
        <span class="value"><a href="/healthz/readiness">/healthz/readiness</a></span></li>
  </ul>
  <p class="hint">
    Create a token with <code>fleetly tokens create --scopes admin</code>, then
    pass it as <code>Authorization: Bearer &lt;token&gt;</code>. Unknown API
    paths answer with a JSON error envelope.
  </p>
</main>
</body>
</html>
`

// newLandingHandler 构造引导页 handler（无任何依赖，静态内容）。
func newLandingHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", landingCSP)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		//nolint:gosec // G704：landingHTML 是编译期固定字符串，无任何用户可控内容
		_, _ = io.WriteString(w, landingHTML)
	})
}
