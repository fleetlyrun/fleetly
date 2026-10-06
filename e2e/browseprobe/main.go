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
// -readonly-query 开启）。步 3 对冷启动重试（镜像 index 解析 + 载体起服
// + 路由发布的窗口）。
package main

import (
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
	wait := flag.Duration("wait", 3*time.Minute, "cold-start wait budget for the tool page (step 3)")
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
	root := base.Scheme + "://" + base.Host
	// 不跟随重定向（步 1 要看 302 本体）。
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}, Timeout: 15 * time.Second}
	follow := &http.Client{Timeout: 15 * time.Second}

	// 步 1：entry 烧票 → 302 + cookie（重试窗：traefik 配置轮询
	// 5s + 发布节拍——受理后即刻探测会撞上轮询间隙）。
	var cookie *http.Cookie
	var resp *http.Response
	entryDeadline := time.Now().Add(*wait)
	for {
		var err error
		resp, err = noRedirect.Get(*entry)
		if err != nil {
			fmt.Fprintf(os.Stderr, "browseprobe: entry request failed: %v\n", err)
			os.Exit(1)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusFound {
			for _, c := range resp.Cookies() {
				if c.Name == "flt_browse" {
					cookie = c
				}
			}
			break
		}
		if time.Now().After(entryDeadline) {
			fmt.Fprintf(os.Stderr, "browseprobe: entry status = %d, want 302 (proxy poll lag exceeded budget)\n", resp.StatusCode)
			os.Exit(1)
		}
		time.Sleep(2 * time.Second)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "flt_browse" {
			cookie = c
		}
	}
	if cookie == nil || !strings.Contains(cookie.Value, ".") {
		fmt.Fprintln(os.Stderr, "browseprobe: entry 302 without a flt_browse=<sid>.<grant> cookie")
		osExitHint()
	}
	fmt.Println("ENTRY-OK 302 + flt_browse cookie minted")

	// 步 2：同票二次 → 401。
	resp, err = noRedirect.Get(*entry)
	if err != nil {
		fmt.Fprintf(os.Stderr, "browseprobe: ticket-reuse request failed: %v\n", err)
		os.Exit(1)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		fmt.Fprintf(os.Stderr, "browseprobe: ticket reuse status = %d, want 401\n", resp.StatusCode)
		os.Exit(1)
	}
	fmt.Println("REUSE-REJECTED 401 (single-use ticket)")

	// 步 3：cookie 过门禁取工具页（冷启动重试）。
	deadline := time.Now().Add(*wait)
	var body string
	for {
		req, _ := http.NewRequest(http.MethodGet, root+"/", nil)
		req.AddCookie(cookie)
		resp, err = follow.Do(req)
		if err == nil {
			bodyBytes, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			body = string(bodyBytes)
			if resp.StatusCode == http.StatusOK && strings.Contains(strings.ToLower(body), strings.ToLower(*marker)) {
				break
			}
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "browseprobe: tool page did not answer 200 with marker %q (last status=%d body head=%.200s)\n",
				*marker, respStatus(resp), body)
			os.Exit(1)
		}
		time.Sleep(2 * time.Second)
	}
	fmt.Println("TOOL-OK 200 behind the ForwardAuth gate")

	// 步 4：无 cookie → 401。
	req, _ := http.NewRequest(http.MethodGet, root+"/", nil)
	resp, err = follow.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "browseprobe: cookieless request failed: %v\n", err)
		os.Exit(1)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		fmt.Fprintf(os.Stderr, "browseprobe: cookieless status = %d, want 401\n", resp.StatusCode)
		os.Exit(1)
	}
	fmt.Println("GATE-REJECTS-NOCOOKIE 401")

	// 步 5：pgweb 服务端只读执法。
	if *readonlyQuery {
		form := url.Values{"query": {"show default_transaction_read_only"}}
		req, _ = http.NewRequest(http.MethodPost, root+"/api/query", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		resp, err = follow.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "browseprobe: read-only query failed: %v\n", err)
			os.Exit(1)
		}
		bodyBytes, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !strings.Contains(string(bodyBytes), "\"on\"") {
			fmt.Fprintf(os.Stderr, "browseprobe: server-side read-only missing (body=%.200s)\n", string(bodyBytes))
			os.Exit(1)
		}
		fmt.Println("READONLY-ON server-enforced session read-only")
	}
}

func respStatus(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

// osExitHint 提示 cookie 面诊断（编译期防误用：函数体在 main 同文件）。
func osExitHint() {
	fmt.Fprintln(os.Stderr, "  hint: wget -S swallows Set-Cookie; use this probe for cookie-chain assertions")
	os.Exit(1)
}
