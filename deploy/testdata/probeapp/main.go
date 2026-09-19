// probeapp 是 dind 升级验收用的最小探针应用（T2.23）：
//
//	probeapp serve            —— HTTP 服务：恒 200 "ok"（被部署的应用本体）
//	probeapp hc               —— healthcheck 动作：exit 0（Swarm 健康门）
//	probeapp watch -url ... -host ... -out ... —— 入口探针：周期 GET，记录
//	                             total/fail 计数与最近错误（场景 1 的
//	                             「升级全程 probe 零失败」断言数据源）
//
// 设计约束：零第三方依赖、静态编译、scratch 镜像可跑；计数文件原子重写
// （tmp + rename），升级编排随时可读而不撕裂。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type counter struct {
	Total    int64  `json:"total"`
	Fail     int64  `json:"fail"`
	LastOK   string `json:"last_ok,omitempty"`
	LastErr  string `json:"last_err,omitempty"`
	Started  string `json:"started"`
	Stopping bool   `json:"stopping,omitempty"`
}

func writeCounter(path string, c counter) error {
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil { //nolint:gosec // 探针受控输出路径
		return err
	}
	return os.Rename(tmp, path)
}

func readCounter(path string) counter {
	raw, err := os.ReadFile(path) //nolint:gosec // 探针受控路径
	if err != nil {
		return counter{}
	}
	var c counter
	_ = json.Unmarshal(raw, &c)
	return c
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: probeapp serve|hc|watch -url URL -host HOST -out FILE [-every 1s]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		srv := &http.Server{ //nolint:gosec // 验收探针：无超时收紧必要
			Addr: ":8080",
			Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte("ok"))
			}),
		}
		if err := srv.ListenAndServe(); err != nil {
			fmt.Fprintln(os.Stderr, "serve:", err)
			os.Exit(1)
		}
	case "hc":
		// 进程活着即可服务——healthcheck 动作恒成功。
		return
	case "watch":
		fs := flag.NewFlagSet("watch", flag.ExitOnError)
		url := fs.String("url", "http://127.0.0.1/", "target URL")
		host := fs.String("host", "", "override Host header (route matching)")
		out := fs.String("out", "/tmp/probe.json", "counter output file")
		every := fs.Duration("every", time.Second, "sample interval")
		_ = fs.Parse(os.Args[2:])
		c := counter{Started: time.Now().UTC().Format(time.RFC3339)}
		_ = writeCounter(*out, c)
		client := &http.Client{Timeout: 5 * time.Second}
		ticker := time.NewTicker(*every)
		defer ticker.Stop()
		for range ticker.C {
			c.Total++
			req, err := http.NewRequest(http.MethodGet, *url, nil)
			if err == nil {
				if *host != "" {
					req.Host = *host
				}
				resp, err := client.Do(req)
				if err == nil {
					_ = resp.Body.Close()
					if resp.StatusCode == http.StatusOK {
						c.LastOK = time.Now().UTC().Format(time.RFC3339)
					} else {
						c.Fail++
						c.LastErr = fmt.Sprintf("status %d at %s", resp.StatusCode, time.Now().UTC().Format(time.RFC3339))
					}
				} else {
					c.Fail++
					c.LastErr = err.Error() + " at " + time.Now().UTC().Format(time.RFC3339)
				}
			} else {
				c.Fail++
				c.LastErr = err.Error()
			}
			if err := writeCounter(*out, c); err != nil {
				fmt.Fprintln(os.Stderr, "write counter:", err)
			}
			_ = filepath.Clean(*out)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown mode:", os.Args[1])
		os.Exit(2)
	}
}
