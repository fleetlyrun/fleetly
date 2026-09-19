package gitserver

// E6 验收测试（S19）：SSH git 子进程治理——连接级 ctx 的取消源与回收链。
//   - WaitDelay 在位 + 连接级 ctx 取消 → 子进程被杀、Wait 限时回收；
//   - 真实 SSH 握手（net.Pipe）后客户端单方面断开 → 连接级 ctx 取消
//     （exec 子进程的取消源）。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	gossh "golang.org/x/crypto/ssh"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestMain 拦截测试二进制再执行形态（E6 假 git 进程）：newGitCommand 的
// 注入缝把 gitBin 指向测试二进制自身，以「挂起直到被父进程 ctx 取消
// 杀掉」的子进程驱动取消回收断言——无需真实挂起的 git 传输。
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && (os.Args[1] == "git-receive-pack" || os.Args[1] == "git-upload-pack") {
		for {
			time.Sleep(time.Hour) // 挂起等待被杀（E6 取消路径的观测对象）
		}
	}
	os.Exit(m.Run())
}

// TestGitCommandCancelledByConnContext E6：WaitDelay 设置存在 + 连接级
// ctx 取消 → 子进程被杀、Wait 限时返回（不依赖客户端体面退出）。
func TestGitCommandCancelledByConnContext(t *testing.T) {
	oldBin := gitBin
	gitBin = os.Args[0] // E6 注入缝：测试二进制充当挂起的 git
	t.Cleanup(func() { gitBin = oldBin })

	ctx, cancel := context.WithCancel(context.Background())
	cmd := newGitCommand(ctx, "git-upload-pack", "/unused/repo")
	if cmd.WaitDelay != gitWaitDelay {
		t.Fatalf("WaitDelay = %v, want %v (E6: I/O 挂死后 Wait 的兜底上限)", cmd.WaitDelay, gitWaitDelay)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake git: %v", err)
	}
	cancel() // 连接关闭形态：ctx 取消（connContext 的取消源）
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("killed process must surface a non-nil Wait error")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("git subprocess not reaped after connection ctx cancel (E6 regression)")
	}
}

// TestConnContextCancelsOnClientDisconnect E6：真实 SSH 握手（net.Pipe，
// 平台公钥认证链同构——按指纹查 git_keys）后客户端单方面断开 →
// sconn.Wait 返回 → 连接级 ctx 取消。这是「客户端中途断开 → git 子进程
// 限时回收」的取消源段（进程回收段由上一用例覆盖）。
func TestConnContextCancelsOnClientDisconnect(t *testing.T) {
	src, st, _, _ := newTestSource(t, 0)
	ctx := context.Background()

	hostSigner, err := src.ensureHostKey()
	if err != nil {
		t.Fatalf("ensure host key: %v", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	clientSigner, err := gossh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("client signer: %v", err)
	}
	fp := gossh.FingerprintSHA256(clientSigner.PublicKey())
	if _, err := st.CreateGitKey(ctx, state.GitKeyWrite{
		Fingerprint: fp,
		PublicKey:   string(gossh.MarshalAuthorizedKey(clientSigner.PublicKey())),
		KeyType:     "ssh-ed25519",
	}); err != nil {
		t.Fatalf("register git key: %v", err)
	}

	serverCfg := &gossh.ServerConfig{}
	serverCfg.PublicKeyCallback = func(_ gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
		if _, err := st.GetGitKeyByFingerprint(ctx, gossh.FingerprintSHA256(key)); err != nil {
			return nil, fmt.Errorf("unknown public key")
		}
		return &gossh.Permissions{}, nil
	}
	serverCfg.AddHostKey(hostSigner)

	// 回环 TCP 而非 net.Pipe：SSH 版本交换两侧「先写后读」，net.Pipe 的
	// 同步无缓冲语义会让双方互堵在写侧（kernel 缓冲的 TCP 上无此问题）。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	type serverResult struct {
		conn *gossh.ServerConn
		err  error
	}
	srvCh := make(chan serverResult, 1)
	go func() {
		c1, err := ln.Accept()
		if err != nil {
			srvCh <- serverResult{err: err}
			return
		}
		sconn, chans, reqs, err := gossh.NewServerConn(c1, serverCfg)
		srvCh <- serverResult{conn: sconn, err: err}
		// 排空握手后的全局请求/通道打开面（本用例不 exec——只验证取消源）。
		go func() {
			for req := range reqs {
				_ = req.Reply(false, nil)
			}
		}()
		go func() {
			for nc := range chans {
				_ = nc.Reject(gossh.UnknownChannelType, "test")
			}
		}()
	}()
	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = c2.Close() }()
	clientConn, _, _, err := gossh.NewClientConn(c2, ln.Addr().String(), &gossh.ClientConfig{
		User:            "git",
		Auth:            []gossh.AuthMethod{gossh.PublicKeys(clientSigner)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(), //nolint:gosec // 测试内回环握手，无中间人面
		Timeout:         10 * time.Second,
	})
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	srv := <-srvCh
	if srv.err != nil {
		t.Fatalf("server handshake: %v", srv.err)
	}

	connCtx, cancel := connContext(ctx, srv.conn)
	defer cancel()

	// 客户端单方面断开（中途断开形态）。
	if err := clientConn.Close(); err != nil {
		t.Fatalf("client close: %v", err)
	}
	select {
	case <-connCtx.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("connection ctx must cancel on client disconnect (E6 regression)")
	}
	_ = srv.conn.Close()
}
