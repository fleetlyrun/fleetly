package fleetly

// Dial 地址 scheme 行为测试（ADR-0079）：grpcs:// 必须协商 TLS（自签
// 证书 → x509 校验失败即传输层确在验证书）、grpc:// 剥前缀后明文往返、
// 裸 host:port 现状不变。

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
)

// selfSignedListener 起一个持自签证书的 TLS 监听（不注册任何 gRPC
// 服务——本测试只需要 TLS 握手发生）。
func selfSignedListener(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fleetly-sdk-test"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:     []string{"localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	tlsLn := tls.NewListener(ln, &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2"},
	})
	// 循环 accept 并显式驱动握手：tls.listener.Accept 只包连接不握手
	//（惰性到首次读写），不驱动则客户端 ClientHello 永远无应答；grpc
	// 拨号失败带退避重试，每次重试都要握手（客户端侧验证书失败）。
	go func() {
		for {
			conn, err := tlsLn.Accept()
			if err != nil {
				return // 监听已关
			}
			go func() {
				defer conn.Close() //nolint:errcheck // 测试握手驱动路径
				_ = conn.(*tls.Conn).Handshake()
			}()
		}
	}()
	t.Cleanup(func() { _ = tlsLn.Close() })
	return tlsLn.Addr().String()
}

// bareGRPCServer 起一个无服务的明文 gRPC 服务（调用任何 method 回
// Unimplemented——证明剥前缀后请求/响应完整往返）。
func bareGRPCServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := grpc.NewServer()
	go func() { _ = srv.Serve(ln) }() //nolint:errcheck // 测试进程退出路径
	t.Cleanup(srv.Stop)
	return ln.Addr().String()
}

// grpcs:// 必须以 TLS 传输拨号：对自签证书服务器的握手失败必须是证书
// 校验错（x509）——明文传输对 TLS 服务器只会得到握手层乱码错误，不可能
// 出现 x509。
func TestDialGRPCSSchemeNegotiatesTLS(t *testing.T) {
	addr := selfSignedListener(t)
	c, err := Dial("grpcs://" + addr)
	require.NoError(t, err)
	defer c.Close() //nolint:errcheck // 测试清理路径

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = c.Users.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "x509", "TLS 传输必在验证书：%v", err)
}

// grpc:// 剥前缀后明文往返：无服务面回 Unimplemented（请求到达服务器并
// 带回响应——非连接层失败）。
func TestDialGRPCSchemeStripsPrefix(t *testing.T) {
	addr := bareGRPCServer(t)
	c, err := Dial("grpc://" + addr)
	require.NoError(t, err)
	defer c.Close() //nolint:errcheck // 测试清理路径

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = c.Users.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unimplemented, status.Code(err), "明文完整往返：method 未注册而非连接失败：%v", err)
}

// 裸 host:port（无 scheme）现状不变：明文直连。
func TestDialBareAddrPlaintextUnchanged(t *testing.T) {
	addr := bareGRPCServer(t)
	c, err := Dial(addr)
	require.NoError(t, err)
	defer c.Close() //nolint:errcheck // 测试清理路径

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = c.Users.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
	require.Error(t, err)
	assert.Equal(t, codes.Unimplemented, status.Code(err), "现状缺省不得受 scheme 支持扰动：%v", err)
}
