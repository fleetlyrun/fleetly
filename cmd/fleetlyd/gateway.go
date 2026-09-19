package main

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/gitserver"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protojson"
)

// newGatewayMux 构建 grpc-gateway mux：REST /v1/** 反向代理到本进程 gRPC
// （torchwood 同款 FromEndpoint 循环形态）。返回的 mux 直接作为 lynx HTTP
// 服务的根 handler，与 lynxhttp.Server 自挂的 /healthz/** 共存（healthz
// 不经此 mux）。
//
// 错误处理：自定义 HTTPErrorHandler（newGatewayErrorHandler，阶段 3/T0.2
// 落地）——gRPC 错误统一渲染为 fleetly.shared.v1.ErrorResponse 信封
// （snake_case 七字段），与 buf.gen.yaml disable_default_errors=true 对齐
// （默认 rpcStatus 错误体已从 OpenAPI 移除）。
//
// ── gateway 挂载清单（T2.17 纪律：gRPC-only 清单显式维护）────────────────
// 挂载（全部服务，读/写/流一致）：
//   - SystemService（Ping 豁免鉴权；Status/Nodes/Ingress 为 read）
//   - AppsService / DeploymentsService / RevisionsService / BuildsService
//   - DriftService / DomainsService / EnvService / PlacementService
//   - TokensService
//   - LogsService（Follow = chunked-JSON 流；Console SSE 直接消费）
//   - EventsService（Watch = chunked-JSON 流，seq 游标 + 过期信封帧）
//
// gRPC-only 清单：**v0.1 为空**——所有服务均挂 gateway（写操作挂 gateway
// 供 Console 使用；Follow/Watch 的 JSON 帧形态适宜 REST）。若后续出现
// 二进制/高频帧不适宜 REST 的 RPC（logs.proto 与此同步登记），在下方
// 注册清单摘除对应 HandlerFromEndpoint 并在 proto 注释同步登记。
//
// ── 原生端点例外清单（T2.19 起；torchwood 同纪律：gateway 上非 proto
// 派生的 HTTP 端点在此显式登记，禁止在别处悄悄挂载）────────────────────
//   - POST /v1/apps/{app}/webhooks/github —— GitHub push 投递（T2.19）；
//     投递体是 JSON 不是 proto，验签必须对原始字节做，无法走 gateway 反代
//     形态（gRPC 侧收到的 body 已被 protojson 重组）。签名即认证：该路径
//     前缀精确豁免 Bearer 拦截（豁免面 = webhookPathPattern，绝未放宽到
//     其他路径——非 webhook 路径无 token 仍 401，gateway_rest_test 钉死）。
//   - POST /v1/apps/{app}/webhooks/gitea  —— Gitea push 投递（T2.19），
//     同上。
//   - GET /ui/** —— Console 端静态托管（T2.21，console.static_dir 指向
//     构建产物目录时启用；缺省空 = 不挂载）。静态资源不要求 token：SPA
//     无服务端会话，鉴权语义在其数据面（REST /v1 全部走 Bearer）。豁免
//     精确到 /ui/ 前缀（实现 = console_static.go，分派面 = 豁免面），
//     非 /ui/ 路径无 token 仍 401（gateway_console_test 钉死）。
//
// 实现：newRootHandler 先按精确路径形态分派 webhook 与 /ui/ 静态 handler
// （console handler 未启用时为 nil——分派跳过），其余一律交回 grpc-gateway
// mux。
func newGatewayMux(grpcEndpoint string) (*runtime.ServeMux, error) {
	mux := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, newJSONMarshaler()),
		runtime.WithMarshalerOption("*/*", newJSONMarshaler()),
		runtime.WithMarshalerOption("application/json", newJSONMarshaler()),
		runtime.WithErrorHandler(newGatewayErrorHandler()),
	)
	opts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	for _, register := range []func(context.Context, *runtime.ServeMux, string, []grpc.DialOption) error{
		serverv1.RegisterSystemServiceHandlerFromEndpoint,
		serverv1.RegisterAppsServiceHandlerFromEndpoint,
		serverv1.RegisterDeploymentsServiceHandlerFromEndpoint,
		serverv1.RegisterRevisionsServiceHandlerFromEndpoint,
		serverv1.RegisterBuildsServiceHandlerFromEndpoint,
		serverv1.RegisterDriftServiceHandlerFromEndpoint,
		serverv1.RegisterDomainsServiceHandlerFromEndpoint,
		serverv1.RegisterEnvServiceHandlerFromEndpoint,
		serverv1.RegisterLogsServiceHandlerFromEndpoint,
		serverv1.RegisterEventsServiceHandlerFromEndpoint,
		serverv1.RegisterPlacementServiceHandlerFromEndpoint,
		serverv1.RegisterTokensServiceHandlerFromEndpoint,
	} {
		if err := register(context.Background(), mux, grpcEndpoint, opts); err != nil {
			return nil, err
		}
	}
	return mux, nil
}

// newJSONMarshaler 是 gateway 的 JSON marshaler：UseProtoNames 使字段名按
// proto 声明输出（snake_case，与 buf.gen.yaml 的 json_names_for_fields=false
// 及 swagger 声明一致）；EmitUnpopulated=false 时零值字段不输出（torchwood
// CustomMarshaler 同款语义）；DiscardUnknown 兼容客户端多发字段。
func newJSONMarshaler() *runtime.JSONPb {
	return &runtime.JSONPb{
		MarshalOptions: protojson.MarshalOptions{
			UseProtoNames:   true,
			EmitUnpopulated: false,
		},
		UnmarshalOptions: protojson.UnmarshalOptions{DiscardUnknown: true},
	}
}

// grpcEndpointFromAddr 从 grpc.addr 推导 gateway 转发目标（torchwood 同款）：
// 保留原主机（默认回环），仅补齐缺失的 host/端口默认值。
func grpcEndpointFromAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		if addr != "" && !strings.HasPrefix(addr, ":") {
			return addr
		}
		return defaultGRPCAddr
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// newRootHandler 组装 HTTP 面根 handler：webhook 原生端点优先精确分派，
// 其次 /ui/ 前缀的 Console 静态托管（consoleUI 为 nil 时跳过——静态托管
// 关闭形态），两者路径形态不匹配的请求原样交回 grpc-gateway mux。各豁免
// 面 = 各自分派面（线性词形判定，不存在「先豁免再分发」的放宽空间）。
// REST 面（fallback）外包 A3 匿名 401 per-IP 限速（newAuthFailureLimiter
// ——仅 gateway 面；webhook 与 /ui/ 分派不经限速层，不受影响）。
func newRootHandler(webhook http.Handler, consoleUI http.Handler, fallback http.Handler) http.Handler {
	gateway := newAuthFailureLimiter(time.Minute, 10).wrap(fallback)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gitserver.WebhookPathPattern.MatchString(r.URL.Path) {
			webhook.ServeHTTP(w, r)
			return
		}
		if consoleUI != nil && (r.URL.Path == consoleUIPathPrefix || strings.HasPrefix(r.URL.Path, consoleUIPathPrefix+"/")) {
			consoleUI.ServeHTTP(w, r)
			return
		}
		gateway.ServeHTTP(w, r)
	})
}

// ── A3（S18）：匿名 401 per-IP 限速 ─────────────────────────────────────────
//
// 问题：REST 面匿名请求（无 token / 错 token）没有 IP 维度限速——每个
// 401 都伴随一次 tokens 表哈希查询，匿名扫描可制造 DB 查询风暴。gRPC
// 直连面拿不到对端 IP，维持现状（token 熵防线：flt_ 前缀 + 192 bit 随机
// 的暴力空间）；本层仅 REST gateway 面生效（RemoteAddr host 可得）。
//
// 算法：固定窗失败桶——对最终 status 401 的响应按 RemoteAddr host 计数，
// 单 IP 单窗口（1 分钟）内第 10 次 401 后，该 IP 的后续请求直接 429
// （不再触达后端）。内存有界：单 map + mutex + 惰性过期（map 超阈值时
// 清除已出窗条目；键空间 = 触发过 401 的对端 IP 数，远小于任意请求集）。
// webhook 端点不在本层覆盖面（错签名 401 走原生分派，非鉴权失败路径，
// 按设计不受影响）；带有效 token 的请求不产生 401 计数，不受影响。
type authFailureLimiter struct {
	mu      sync.Mutex
	buckets map[string]*failBucket
	window  time.Duration
	limit   int
	now     func() time.Time
}

// failBucket 是单 IP 的固定窗计数。
type failBucket struct {
	count int
	start time.Time
}

// authFailureMaxIPs 是惰性过期阈值（触发过 401 的 distinct IP 上界）。
const authFailureMaxIPs = 10000

// newAuthFailureLimiter 构造限速器（window/limit 生产取 1min/10）。
func newAuthFailureLimiter(window time.Duration, limit int) *authFailureLimiter {
	return &authFailureLimiter{
		buckets: make(map[string]*failBucket),
		window:  window,
		limit:   limit,
		now:     time.Now,
	}
}

// wrap 把限速中间件包在 next 之外：入向先判该 IP 是否已被封禁（窗口内
// 401 计数达上限），出向用状态记录器观测最终 status——401 计数、其余
// 放行。
func (l *authFailureLimiter) wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r.RemoteAddr)
		if l.blocked(ip) {
			http.Error(w, "too many failed authentication attempts", http.StatusTooManyRequests)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		if rec.status == http.StatusUnauthorized {
			l.recordFailure(ip)
		}
	})
}

// blocked 报告该 IP 是否仍处于封禁窗内（计数达上限且窗口未过期）。
func (l *authFailureLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b, ok := l.buckets[ip]
	if !ok {
		return false
	}
	if l.now().Sub(b.start) >= l.window {
		return false // 窗口已过期：放行（下次 401 时重开窗口）
	}
	return b.count >= l.limit
}

// recordFailure 记一次 401：出窗重开计数，在窗内累加；map 超阈值时惰性
// 清除全部已出窗条目（内存有界）。
func (l *authFailureLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.buckets) >= authFailureMaxIPs {
		for k, b := range l.buckets {
			if now.Sub(b.start) >= l.window {
				delete(l.buckets, k)
			}
		}
	}
	b, ok := l.buckets[ip]
	if !ok || now.Sub(b.start) >= l.window {
		l.buckets[ip] = &failBucket{count: 1, start: now}
		return
	}
	b.count++
}

// remoteIP 从 RemoteAddr 取 host（去掉端口；IPv6 字面量形态由
// SplitHostPort 正确处理）。拿不到对端 IP 的形态返回原串（计数键退化为
// 该串，不影响有界性）。
func remoteIP(remoteAddr string) string {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return host
	}
	return remoteAddr
}

// statusRecorder 记录最终响应 status 的 ResponseWriter 包装（A3 观测面；
// Flush 透传——gateway 流式端点（Watch/Follow 的 chunked-JSON）依赖）。
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wrote {
		r.status = code
		r.wrote = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.wrote = true
	return r.ResponseWriter.Write(b)
}

// Flush 透传底层 Flusher（流式响应不被缓冲截断）。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
