// Command h2cclient 是 e2e 离线自备的 h2c 探针客户端（dind 内 curl 无
// HTTP/2 支持、宿主 curl 是 Schannel 构建——出站不稳环境下脚本自持）。
// 用法：h2cclient [-host HOST] URL
// 输出：STATUS/PROTO 两行头 + 响应体全文（whoami 的 Proto 行是断言锚）。
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"golang.org/x/net/http2"
)

func main() {
	host := flag.String("host", "", "override the Host header (routing by name without DNS)")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: h2cclient [-host HOST] URL")
		os.Exit(64)
	}
	// h2c 明文先行知识：AllowHTTP + DialTLS 落普通 TCP（无 TLS 握手）。
	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequest(http.MethodGet, flag.Arg(0), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad url:", err)
		os.Exit(1)
	}
	if *host != "" {
		req.Host = *host
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "request failed:", err)
		os.Exit(1)
	}
	defer resp.Body.Close() //nolint:errcheck // 进程退出路径
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read body:", err)
		os.Exit(1)
	}
	fmt.Printf("STATUS %s\nPROTO %s\n", resp.Status, resp.Proto)
	fmt.Print(string(body))
}
