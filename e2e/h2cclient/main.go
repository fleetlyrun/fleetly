// Command h2cclient 是 e2e 离线自备的 h2c 探针客户端（dind 内 curl 无
// HTTP/2 支持、宿主 curl 是 Schannel 构建——出站不稳环境下脚本自持）。
// 用法：h2cclient [-host HOST] URL
// 输出：STATUS/PROTO 两行头 + 响应体全文（whoami 的 Proto 行是断言锚）。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	host := flag.String("host", "", "override the Host header (routing by name without DNS)")
	timeout := flag.Duration("timeout", 10*time.Second, "request timeout")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: h2cclient [-host HOST] URL")
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, flag.Arg(0), nil)
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
