// Command webhookrecv 是 e2e 离线自备的告警通道假接收器（ADR-0041 验收
// 锚 2/3 的真投递面）：与 fleetlyd 同在 dind 内跑——loopback 可达，通道
// URL 即 http://127.0.0.1:9099/hook。h2cserver 只回显不落载荷，投递断言
// 需要取回面，故仿 h2cclient/h2cserver 形态独立成件（脚本内 go build →
// docker cp 进 dind → setsid 后台跑）。
//
// 三个面：POST /hook 记录载荷（内存；engine 派发 10s 上界——先读后记，
// 200 即回）；GET /dump 按接收序返回全部已收载荷（分隔行带序号与接收
// 时刻，脚本按整行载荷 grep 断言字段真实值）；GET /reset 清空（分阶段
// 断言隔离——test 载荷与 alert 载荷互不污染）。
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

// maxPayloadBytes 是单条载荷上界（防御面；告警载荷是几百字节级）。
const maxPayloadBytes = 1 << 20

// receivedPayload 是一条已收载荷（原文 + 接收时刻）。
type receivedPayload struct {
	at   time.Time
	body string
}

func main() {
	addr := ":9099"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}

	var mu sync.Mutex
	payloads := []receivedPayload{}

	mux := http.NewServeMux()
	mux.HandleFunc("/hook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, maxPayloadBytes))
		if err != nil {
			http.Error(w, "read body failed", http.StatusBadRequest)
			return
		}
		mu.Lock()
		payloads = append(payloads, receivedPayload{at: time.Now().UTC(), body: string(body)})
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/dump", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		for i, p := range payloads {
			_, _ = fmt.Fprintf(w, "=== received %d at %s ===\n%s\n", i+1, p.at.Format(time.RFC3339Nano), p.body)
		}
	})
	mux.HandleFunc("/reset", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		payloads = nil
		mu.Unlock()
		_, _ = io.WriteString(w, "reset")
	})

	// e2e 夹具形态（gosec G112 面向生产服务）：短连接探针/派发客户端，
	// 仍设 ReadHeaderTimeout 防 dind 内挂死连接堆积（h2cserver 同款）。
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}
