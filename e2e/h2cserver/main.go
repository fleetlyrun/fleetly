// Command h2cserver 是 e2e 离线自备的 h2c 回显后端（dind 内 docker build
// 成 scratch 镜像，零外网镜像依赖）。stdlib http.Server 的 Protocols 面
// （Go 1.24+）同时收 h2c 先行知识与 HTTP/1.1——正是 messageloop 形态的
// 后端形态。
//
// 背景（N0.1 收口实证）：traefik/whoami 不说 h2c（对先行知识前奏回
// HTTP/1.1 字节），而 traefik 的 h2c:// 后端拨号不回退——h2c Route 端到端
// 断言必须用真 h2c 后端。回显体首行 "PROTO-LINE <r.Proto>" 是脚本断言锚。
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := ":8080"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	protocols := &http.Protocols{}
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 回显收到的协议版本（断言锚）。夹具以回显请求协议为职责，输出只
		// 进 e2e 探针客户端（非浏览器面，无 XSS 面）。
		_, _ = io.WriteString(w, "PROTO-LINE "+r.Proto+"\n") //nolint:gosec // G705 污点误报：回显夹具，消费方是 e2e 探针
	})
	// e2e 夹具形态（gosec G112 面向生产服务）：探针客户端短连接，仍设
	// ReadHeaderTimeout 防 dind 内挂死连接堆积。
	srv := &http.Server{
		Addr: addr, Handler: mux, Protocols: protocols,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}
