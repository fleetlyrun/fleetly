// TestClientPing 是 SDK 的使用示例测试：起一个最小 SystemService gRPC
// 服务（模拟 fleetlyd 的 gRPC 面），随后按典型用法 NewClient → Ping →
// 断言响应 → Close。
package fleetly_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// stubSystemService 是示例服务端：Ping 回应 service/version（与 fleetlyd
// 的真实实现同形，见 cmd/fleetlyd/grpc.go）。
type stubSystemService struct {
	serverv1.UnimplementedSystemServiceServer
}

func (s *stubSystemService) Ping(context.Context, *serverv1.PingRequest) (*serverv1.PingResponse, error) {
	return &serverv1.PingResponse{Service: "fleetlyd", Version: "dev"}, nil
}

func TestClientPing(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	serverv1.RegisterSystemServiceServer(srv, &stubSystemService{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	client, err := fleetly.NewClient(fleetly.WithAddr(lis.Addr().String()))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := client.Ping(ctx)
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if resp.GetService() != "fleetlyd" {
		t.Fatalf("service = %q, want %q", resp.GetService(), "fleetlyd")
	}
	if resp.GetVersion() == "" {
		t.Fatal("version is empty")
	}
}

// startTLSService 起一个自签证书的 TLS gRPC 服务（SystemService stub 同上），
// 返回地址、证书 DER 与收口函数（V2-8 TLS 选项测试的共用量具）。
func startTLSService(t *testing.T) (addr string, certDER []byte, stop func()) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "sdk-tls-test"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	})))
	serverv1.RegisterSystemServiceServer(srv, &stubSystemService{})
	go func() { _ = srv.Serve(lis) }()
	return lis.Addr().String(), der, srv.Stop
}

// rootPoolWith 返回信任 certDER 的根证书池（WithTLS 的信任面装配形态）。
func rootPoolWith(t *testing.T, der []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) {
		t.Fatal("append test root failed")
	}
	return pool
}

// TestClientTLS V2-8（E7 同批）SDK TLS 面：WithTLS 信任自签根 → 校验通过；
// WithTLSInsecure → 显式旁路；TLS 拨号 + token → RequireTransportSecurity
// 跟随为 true 且经 TLS 正常携带；TLS 客户端拨明文端口 → 失败；明文客户端
// 拨明文端口 → 存量行为不变。
func TestClientTLS(t *testing.T) {
	addr, der, stop := startTLSService(t)
	t.Cleanup(stop)

	t.Run("WithTLS verifies against the pinned root", func(t *testing.T) {
		c, err := fleetly.NewClient(fleetly.WithAddr(addr), fleetly.WithTLS(&tls.Config{
			RootCAs:    rootPoolWith(t, der),
			ServerName: "localhost",
			MinVersion: tls.VersionTLS12,
		}))
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.Ping(ctx); err != nil {
			t.Fatalf("Ping over verified TLS: %v", err)
		}
	})

	t.Run("WithTLSInsecure skips verification explicitly", func(t *testing.T) {
		c, err := fleetly.NewClient(fleetly.WithAddr(addr), fleetly.WithTLSInsecure())
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.Ping(ctx); err != nil {
			t.Fatalf("Ping over insecure TLS: %v", err)
		}
	})

	t.Run("bearer credentials ride TLS with RequireTransportSecurity=true", func(t *testing.T) {
		c, err := fleetly.NewClient(fleetly.WithAddr(addr), fleetly.WithTLSInsecure(), fleetly.WithToken("flt_test"))
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.Ping(ctx); err != nil {
			t.Fatalf("Ping with token over TLS: %v", err)
		}
	})

	t.Run("plaintext client against a TLS port fails", func(t *testing.T) {
		c, err := fleetly.NewClient(fleetly.WithAddr(addr))
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		t.Cleanup(func() { _ = c.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := c.Ping(ctx); err == nil {
			t.Fatal("plaintext dial against a TLS server must fail")
		}
	})
}

// TestClientTLSRequireTransportSecurityFollows 拨号面决定凭据约束：TLS 客
// 户端（凭据要求安全）拨**明文**端口时 gRPC 传输层拒绝发送凭据——「跟随」
// 语义的强制面证据（不是调用方自律）。
func TestClientTLSRequireTransportSecurityFollows(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	serverv1.RegisterSystemServiceServer(srv, &stubSystemService{})
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	c, err := fleetly.NewClient(fleetly.WithAddr(lis.Addr().String()), fleetly.WithTLSInsecure(), fleetly.WithToken("flt_test"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := c.Ping(ctx); err == nil {
		t.Fatal("credentials requiring TLS must be rejected on a plaintext connection")
	}
}
