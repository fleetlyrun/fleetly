// Command h2cclient 是 e2e 离线自备的 HTTP 探针客户端（dind 内 curl 无
// HTTP/2 支持、宿主 curl 是 Schannel 构建、apk 又依赖出站网——出站不稳
// 环境下脚本自持）。支持任意 method + 请求体文件（webhook 段复用）。
// 用法：h2cclient [-host HOST] [-X METHOD] [-data @FILE] [-timeout 10s] URL
// 输出：STATUS/PROTO 两行头 + 响应体全文（POST 响应体落 /tmp/h2c-body 供
// 脚本续读）。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// headerFlags 收集可重复的 -H 旗标。
type headerFlags []string

func (h *headerFlags) String() string { return strings.Join(*h, ", ") }
func (h *headerFlags) Set(v string) error {
	*h = append(*h, v)
	return nil
}

func main() {
	host := flag.String("host", "", "override the Host header (routing by name without DNS)")
	method := flag.String("X", http.MethodGet, "HTTP method")
	data := flag.String("data", "", "request body: literal text or @file")
	bodyFile := flag.String("o", "", "write the response body to this file as well")
	headers := headerFlags{}
	flag.Var(&headers, "H", "request header \"Name: Value\" (repeatable)")
	timeout := flag.Duration("timeout", 10*time.Second, "request timeout")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: h2cclient [-host HOST] [-X METHOD] [-data @FILE] URL")
		os.Exit(64)
	}
	// h2c 明文先行知识：标准库 http.Protocols（Go 1.24+）声明未加密 HTTP/2
	//（x/net/http2.Transport 已弃用且 stdlib 已覆盖此面）。
	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	tr := &http.Transport{Protocols: protocols}
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var body io.Reader
	if *data != "" {
		if strings.HasPrefix(*data, "@") {
			f, err := os.Open((*data)[1:])
			if err != nil {
				fmt.Fprintln(os.Stderr, "open data file:", err)
				os.Exit(1)
			}
			defer f.Close() //nolint:errcheck // 进程退出路径
			body = f
		} else {
			body = strings.NewReader(*data)
		}
	}
	req, err := http.NewRequestWithContext(ctx, *method, flag.Arg(0), body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad url:", err)
		os.Exit(1)
	}
	if *host != "" {
		req.Host = *host
	}
	for _, h := range headers {
		name, value, found := strings.Cut(h, ":")
		if !found {
			fmt.Fprintln(os.Stderr, "bad header:", h)
			os.Exit(1)
		}
		req.Header.Set(strings.TrimSpace(name), strings.TrimSpace(value))
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "request failed:", err)
		os.Exit(1)
	}
	defer resp.Body.Close() //nolint:errcheck // 进程退出路径
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read body:", err)
		os.Exit(1)
	}
	if *bodyFile != "" {
		if err := os.WriteFile(*bodyFile, raw, 0o600); err != nil { //nolint:gosec // e2e 夹具产物
			fmt.Fprintln(os.Stderr, "write body file:", err)
			os.Exit(1)
		}
	}
	fmt.Printf("STATUS %s\nPROTO %s\n", resp.Status, resp.Proto)
	fmt.Print(string(raw))
}
