package ingress

// 控制面配置端点（HTTP provider 面与 ACME 挑战应答面，T2.15/T2.16）：
//
//	GET /configs                          → 全量动态配置 JSON（Traefik 轮询；
//	                                        鉴权 token 强制，错误恒 401）
//	GET /healthz                          → 探活（无鉴权、无信息泄露）
//	GET /.well-known/acme-challenge/<tok> → 挑战应答（公开路径，ACME 契约；
//	                                        Traefik 挑战路由反代到这里）
//
// 形态取舍：独立内部端口（默认 0.0.0.0:8422），不挂现有 gateway mux——
// gateway 面是 gRPC/REST 契约面（127.0.0.1 回环 + 平台鉴权体系），而本
// 端点的调用方是 Traefik 任务（容器 netns，须宿主 IP 可达）与公网 ACME
// 校验流量（经 Traefik 反代），鉴权模型（静态 bearer token / 公开挑战
// 路径）与契约面完全不同；混挂会把两类暴露面耦合进同一个 mux。默认绑定
// 非回环是 Traefik 任务可达性的前提，暴露面收敛依赖：①强制 token（常量
// 时间比较），②/healthz 无信息，③挑战路径只应答在途 token。

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// tokenLoadOrGenerate 读取（或首启生成并持久化）配置端点 token。
// 文件权限 0600（POSIX 面；Windows 由卷 ACL 兜底，secrets 同款取舍）。
func tokenLoadOrGenerate(path string) (string, bool, error) {
	if raw, err := os.ReadFile(path); err == nil { //nolint:gosec // 路径来自操作员配置（ingress.token_file），非不可信输入
		token := strings.TrimSpace(string(raw))
		if token != "" {
			return token, false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	token, err := GenerateToken()
	if err != nil {
		return "", false, err
	}
	// 目录先行（token_file 可能指向尚未创建的目录）。
	if derr := os.MkdirAll(filepath.Dir(path), 0o750); derr != nil {
		return "", false, derr
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", false, err
	}
	return token, true, nil
}

// newProviderHandler 构造端点 handler（token 鉴权 + 视图应答）。
func newProviderHandler(v *view, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/configs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !authorize(r, token) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		cfg := v.snapshot()
		raw, err := MarshalJSONBytes(cfg)
		if err != nil {
			http.Error(w, "marshal config", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		v.markServed()
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc(acmeChallengePathPrefix, func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.URL.Path, acmeChallengePathPrefix)
		if token == "" || strings.Contains(token, "/") {
			http.NotFound(w, r)
			return
		}
		keyAuth, ok := v.challengeKeyAuth(token)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(keyAuth))
	})
	return mux
}

// authorize 校验 bearer token（常量时间比较——时序侧写防御）。
func authorize(r *http.Request, token string) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return false
	}
	got := h[len(prefix):]
	return subtle.ConstantTimeCompare([]byte(got), []byte(token)) == 1
}
