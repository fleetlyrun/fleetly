// Command browseprobe 是 F3.6 e2e 的浏览器会话全链探针（dind 内自备：
// busybox wget 吞 Set-Cookie 头且自动跟重定向——cookie 链判定不可靠，
// 判定单源住本探针）。五步断言（ADR-0051 验收锚的 e2e 面）：
//
//  1. GET entry（不跟随重定向）→ 302 + Set-Cookie flt_browse=<sid>.<grant>
//  2. 同票二次 → 401（Launcher Ticket 单用途）
//  3. GET /（携带 cookie）→ 200 且响应体含工具标记（ForwardAuth 放行）
//  4. GET /（无 cookie）→ 401（门禁反锚）
//  5. POST /api/query（pgweb 方言）show default_transaction_read_only → on
//     （服务端只读执法——postgres 会话级，非工具摆设）
//
// 用法：browseprobe -entry URL -marker pgweb（step 5 是 pgweb 专属，
// -readonly-query 开启）。步 1/3 各带重试窗（traefik 配置轮询 5s + 镜像
// index 解析 + 载体起服 + 路由发布的冷启动窗口）。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	entry := flag.String("entry", "", "browse entry URL (with session and ticket query)")
	marker := flag.String("marker", "pgweb", "response body marker of the browser tool page")
	readonlyQuery := flag.Bool("readonly-query", false, "run the pgweb server-side read-only assertion (step 5)")
	wait := flag.Duration("wait", 3*time.Minute, "cold-start wait budget (steps 1 and 3)")
	flag.Parse()
	if *entry == "" {
		fmt.Fprintln(os.Stderr, "usage: browseprobe -entry URL [-marker pgweb] [-readonly-query]")
		os.Exit(64)
	}
	base, err := url.Parse(*entry)
	if err != nil || base.Host == "" {
		fmt.Fprintf(os.Stderr, "browseprobe: entry URL malformed: %v\n", err)
		os.Exit(64)
	}
	ctx := context.Background()
	root := base.Scheme + "://" + base.Host
	// 不跟随重定向（步 1 要看 302 本体）。
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}, Timeout: 15 * time.Second}
	follow := &http.Client{Timeout: 15 * time.Second}

	// doReq 是统一请求面（noctx 面单源：一切请求带 ctx）。
	doReq := func(c *http.Client, method, target string, cookie *http.Cookie, contentType string, body io.Reader) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, method, target, body)
		if err != nil {
			return nil, err
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		return c.Do(req)
	}
	drain := func(resp *http.Response) string {
		if resp == nil {
			return ""
		}
		b, _ := io.ReadAll(resp.Body) //nolint:errcheck // 诊断输出
		_ = resp.Body.Close()
		return string(b)
	}

	// 步 1：entry 烧票 → 302 + cookie（重试窗：traefik 配置轮询 5s +
	// 发布节拍——受理后即刻探测会撞上轮询间隙）。
	var cookie *http.Cookie
	deadline := time.Now().Add(*wait)
	var resp *http.Response
	for {
		resp, err = doReq(noRedirect, http.MethodGet, *entry, nil, "", nil)
		if err != nil {
			fmt.Fprintf(os.Stderr, "browseprobe: entry request failed: %v\n", err)
			os.Exit(1)
		}
		_ = drain(resp)
		if resp.StatusCode == http.StatusFound {
			for _, c := range resp.Cookies() {
				if c.Name == "flt_browse" {
					cookie = c
				}
			}
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "browseprobe: entry status = %d, want 302 (proxy poll lag exceeded budget)\n", resp.StatusCode)
			os.Exit(1)
		}
		time.Sleep(2 * time.Second)
	}
	if cookie == nil || !strings.Contains(cookie.Value, ".") {
		fmt.Fprintln(os.Stderr, "browseprobe: entry 302 without a flt_browse=<sid>.<grant> cookie (wget -S swallows Set-Cookie; this probe is the assertion surface)")
		os.Exit(1)
	}
	fmt.Println("ENTRY-OK 302 + flt_browse cookie minted")

	// 步 2：同票二次 → 401。
	resp, err = doReq(noRedirect, http.MethodGet, *entry, nil, "", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "browseprobe: ticket-reuse request failed: %v\n", err)
		os.Exit(1)
	}
	_ = drain(resp)
	if resp.StatusCode != http.StatusUnauthorized {
		fmt.Fprintf(os.Stderr, "browseprobe: ticket reuse status = %d, want 401\n", resp.StatusCode)
		os.Exit(1)
	}
	fmt.Println("REUSE-REJECTED 401 (single-use ticket)")

	// 步 3：cookie 过门禁取工具页（冷启动重试）。
	deadline = time.Now().Add(*wait)
	body := ""
	for {
		resp, err = doReq(follow, http.MethodGet, root+"/", cookie, "", nil)
		if err == nil {
			body = drain(resp)
			if resp.StatusCode == http.StatusOK && strings.Contains(strings.ToLower(body), strings.ToLower(*marker)) {
				break
			}
		}
		if time.Now().After(deadline) {
			status := 0
			if resp != nil {
				status = resp.StatusCode
			}
			fmt.Fprintf(os.Stderr, "browseprobe: tool page did not answer 200 with marker %q (last status=%d body head=%.200s)\n",
				*marker, status, body)
			os.Exit(1)
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Println("TOOL-OK 200 behind the ForwardAuth gate")

	// 步 4：无 cookie → 401。
	resp, err = doReq(follow, http.MethodGet, root+"/", nil, "", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "browseprobe: cookieless request failed: %v\n", err)
		os.Exit(1)
	}
	_ = drain(resp)
	if resp.StatusCode != http.StatusUnauthorized {
		fmt.Fprintf(os.Stderr, "browseprobe: cookieless status = %d, want 401\n", resp.StatusCode)
		os.Exit(1)
	}
	fmt.Println("GATE-REJECTS-NOCOOKIE 401")

	// 步 5：pgweb 服务端只读执法。
	if *readonlyQuery {
		form := url.Values{"query": {"show default_transaction_read_only"}}
		resp, err = doReq(follow, http.MethodPost, root+"/api/query", cookie, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "browseprobe: read-only query failed: %v\n", err)
			os.Exit(1)
		}
		body = drain(resp)
		if !strings.Contains(body, "\"on\"") {
			fmt.Fprintf(os.Stderr, "browseprobe: server-side read-only missing (body=%.200s)\n", body)
			os.Exit(1)
		}
		fmt.Println("READONLY-ON server-enforced session read-only")
	}
}
