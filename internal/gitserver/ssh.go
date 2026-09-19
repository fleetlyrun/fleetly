package gitserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

// SSH git 面（T2.19）：golang.org/x/crypto/ssh 承载（原间接依赖提升为
// 直接依赖——非新引入第三方框架）。
//
// 认证 = 平台管理的 git 公钥：PublicKeyCallback 按 SHA256 指纹查 git_keys
// 表（指纹 = gossh.FingerprintSHA256，ssh-keygen -lf 同格式）；查无此键
// 一律拒绝。
//
// 协议 = exec 白名单：session 的 exec 请求限定
//
//	git-receive-pack '<app>.git'   （push；bare 仓库懒创建 + 钩子生成）
//	git-upload-pack '<app>.git'    （clone/fetch）
//
// app 名经 ValidAppName 严格校验（路径注入防线）；git 直接 exec（无
// shell），命令形态解析拒绝任何其他词形。

// ListenAndServe 监听 SSH git 面（阻塞至 ctx 取消或监听错误）；启用态由
// lynx.Service 壳（cmd/fleetlyd）驱动。
func (s *GitTriggers) ListenAndServe(ctx context.Context, addr string) error {
	signer, err := s.ensureHostKey()
	if err != nil {
		return err
	}
	serverConfig := &gossh.ServerConfig{}
	// 安全评审注记：PublicKeyCallback 返回错误即拒绝该公钥；拒绝文案不
	// 泄漏注册表信息（unknown key 统一措辞）。SystemLog 对失败尝试输出
	// 对端地址——保留默认即可。
	serverConfig.PublicKeyCallback = func(meta gossh.ConnMetadata, key gossh.PublicKey) (*gossh.Permissions, error) {
		fingerprint := gossh.FingerprintSHA256(key)
		if _, err := s.st.GetGitKeyByFingerprint(ctx, fingerprint); err != nil {
			s.log.Warn("gitserver: ssh auth rejected", "remote", meta.RemoteAddr().String(),
				"fingerprint", fingerprint)
			return nil, fmt.Errorf("unknown public key")
		}
		return &gossh.Permissions{Extensions: map[string]string{"fingerprint": fingerprint}}, nil
	}
	serverConfig.AddHostKey(signer)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("gitserver: listen %s: %w", addr, err)
	}
	s.log.Info("gitserver: ssh git endpoint listening", "addr", ln.Addr().String())
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("gitserver: accept: %w", err)
		}
		go s.handleConn(ctx, conn, serverConfig)
	}
}

// handleConn 完成一次 SSH 握手并服务其会话（每连接独立 goroutine）。
func (s *GitTriggers) handleConn(ctx context.Context, conn net.Conn, config *gossh.ServerConfig) {
	defer func() { _ = conn.Close() }()
	sconn, chans, reqs, err := gossh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer func() { _ = sconn.Close() }()
	go func() {
		for req := range reqs {
			// 全局请求（无 v0.1 语义）一律拒绝。
			_ = req.Reply(false, nil)
		}
	}()
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(gossh.UnknownChannelType, "unsupported channel type")
			continue
		}
		ch, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ctx, sconn, ch, requests)
	}
}

// handleSession 服务一个 session 通道：只认 exec 请求（env/pty 等一律
// 拒绝——请求面即攻击面）。
func (s *GitTriggers) handleSession(ctx context.Context, sconn *gossh.ServerConn, ch gossh.Channel, requests <-chan *gossh.Request) {
	defer func() { _ = ch.Close() }()
	for req := range requests {
		switch req.Type {
		case "exec":
			var payload struct{ Command string }
			if err := gossh.Unmarshal(req.Payload, &payload); err != nil {
				_ = req.Reply(false, nil)
				return
			}
			_ = req.Reply(true, nil)
			s.execGitCommand(ctx, sconn, ch, payload.Command)
			return
		case "env":
			_ = req.Reply(false, nil)
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}
}

// execGitCommand 解析白名单命令并 exec 系统 git（stdio 直连通道）。exit
// status 随通道回执；未知命令回 127。
func (s *GitTriggers) execGitCommand(ctx context.Context, sconn *gossh.ServerConn, ch gossh.Channel, command string) {
	sub, app, err := parseGitCommand(command)
	if err != nil {
		s.log.Warn("gitserver: rejected git command", "remote", sconn.RemoteAddr().String(),
			"command", truncate(command, 120), "error", err.Error())
		_, _ = fmt.Fprintf(ch.Stderr(), "fleetly: %v\r\n", err)
		sendExitStatus(ch, 127)
		return
	}
	repoPath := s.repoPath(app)
	if sub == "receive-pack" {
		// push 路径：bare 仓库懒创建 + post-receive 钩子在位。
		if _, _, err := s.EnsureBareRepo(ctx, app); err != nil {
			s.log.Warn("gitserver: ensure bare repo failed", "app", app, "error", err.Error())
			_, _ = fmt.Fprintf(ch.Stderr(), "fleetly: repository init failed: %v\r\n", err)
			sendExitStatus(ch, 1)
			return
		}
	}
	gitArgs := []string{sub, repoPath}
	cmd := exec.CommandContext(ctx, "git", gitArgs...) //nolint:gosec // G204：子命令为白名单词形、路径经 app 名严格校验
	cmd.Stdin = ch
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	if err := cmd.Start(); err != nil {
		_, _ = fmt.Fprintf(ch.Stderr(), "fleetly: exec git: %v\r\n", err)
		sendExitStatus(ch, 127)
		return
	}
	waitErr := cmd.Wait()
	code := 0
	if waitErr != nil {
		code = 1
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			code = exitErr.ExitCode()
			if code < 0 {
				code = 1
			}
		}
	}
	s.log.Info("gitserver: git command finished", "app", app, "command", sub, "exit", code)
	sendExitStatus(ch, code)
}

// parseGitCommand 解析 SSH exec 命令形态：
//
//	git-receive-pack '<app>.git' | git-upload-pack '<app>.git'
//
// 引号可选（引号必须成对闭合）；路径允许一个前导斜杠（ssh:// URL 形态
// git 原样发送 /<app>.git——归一后校验），其余含路径分隔符、..、空白或
// 词形非法的形态一律拒绝。app 名去 .git 后缀后经 ValidAppName 严格校验
// ——白名单即防线（最终仓库路径由服务端以 <root>/<app>.git 构造，客户端
// 传值只决定 app 名）。
func parseGitCommand(command string) (sub, app string, err error) {
	fields := strings.TrimSpace(command)
	sub, rest, found := strings.Cut(fields, " ")
	if !found || rest == "" {
		return "", "", fmt.Errorf("malformed command")
	}
	if sub != "git-receive-pack" && sub != "git-upload-pack" {
		return "", "", fmt.Errorf("command not allowed")
	}
	repo := strings.TrimSpace(rest)
	if len(repo) >= 2 && (repo[0] == '\'' || repo[0] == '"') {
		q := repo[0]
		if repo[len(repo)-1] != q {
			return "", "", fmt.Errorf("unbalanced quotes")
		}
		repo = repo[1 : len(repo)-1]
	}
	repo = strings.TrimPrefix(repo, "/")
	if repo == "" || strings.ContainsAny(repo, "/\\ \t") || strings.Contains(repo, "..") {
		return "", "", fmt.Errorf("malformed repository path")
	}
	app = strings.TrimSuffix(repo, ".git")
	if !ValidAppName(app) {
		return "", "", fmt.Errorf("invalid repository name %q", app)
	}
	return strings.TrimPrefix(sub, "git-"), app, nil
}

// sendExitStatus 回执 exit-status 并关闭写侧（SSH 协议约定）。
func sendExitStatus(ch gossh.Channel, code int) {
	payload := gossh.Marshal(struct{ Status uint32 }{Status: uint32(code)}) //nolint:gosec // G115：exit code 经上文归一到 0..255
	if _, err := ch.SendRequest("exit-status", false, payload); err != nil {
		return
	}
	_ = ch.CloseWrite()
}

// truncate 是日志字段截断（防超长命令刷屏）。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
