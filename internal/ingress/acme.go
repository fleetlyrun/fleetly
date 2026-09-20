package ingress

// 集中 ACME 签发器（T2.16；架构 §2.6 证书集中化行）：控制面内嵌 lego，
// 域名集变化时按需签（多 SAN 单证书/app）；HTTP-01 挑战经各节点 Traefik
// 把 /.well-known/acme-challenge/* 反代回控制面应答端点（零 DNS 服务商
// 集成）；证书落证书库 + state 台账（sha256/到期时间）。
//
// 事件纪律：签发/续期不发明新事件名——审计记录承载（ingress.cert_issued/
// ingress.cert_renewed/ingress.cert_issue_failed/ingress.cert_renew_failed，
// result=error 时带错误码）；续期扫描周期任务在 Manager.Run。
//
// 挑战收敛时序：Present → 视图叠加挑战路由（revision+1）→ 等待 Traefik
// 拉走 ≥ 该 revision（awaitServed；Traefik 轮询节奏 + 预算）→ 返回让
// lego 通知 CA 校验。跳过等待（入口未接管/超时）不阻塞签发——挑战失败
// 由 lego 上报，失败形态统一为 cert 审计 error 行。

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/registration"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// acmeUser 是 lego 账号（私钥持久化文件 + 注册资源 sidecar 缓存）。
type acmeUser struct {
	EmailAddr string
	Reg       *registration.Resource
	key       crypto.PrivateKey
}

func (u *acmeUser) GetEmail() string                        { return u.EmailAddr }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.Reg }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// acmeAccountFile 是注册 sidecar（注册 URI 缓存；私钥独立文件）。
type acmeAccountFile struct {
	Email        string `json:"email"`
	Registration struct {
		URI string `json:"uri"`
	} `json:"registration"`
}

// challengeProvider 是 ACME HTTP-01 的平台实现（challenge.Provider）：
// Present 把 token 注入视图（挑战路由随配置下发到 Traefik），CleanUp 移除。
type challengeProvider struct {
	mgr *Manager
}

// Present 实现 challenge.Provider。
func (p *challengeProvider) Present(_, token, keyAuth string) error {
	rev := p.mgr.vw.addChallenge(token, keyAuth)
	// 收敛预算：Traefik 轮询 2s × 5 周期 + 余量（挑战路径必须先于 CA
	// 校验请求出现在 Traefik）。
	if !p.mgr.vw.awaitServed(rev, 12*time.Second) {
		p.mgr.log.Warn("ingress: challenge router not yet observed by traefik "+
			"(proceeding; validation may fail if ingress not converged)", "token", token[:8]+"…")
		return nil
	}
	// awaitServed 只确认「Traefik 取走载荷」（fetch），不含 Traefik 侧应用
	// 生效（apply）——CA 校验请求若先于 apply 到达，挑战路径 404 且 authz
	// 直接作废（lego 不重试单一 challenge；T2.26 旅程 dind 实测复现为
	// 403 unauthorized: Non-200 404）。此处主动探测「经 Traefik 的挑战路径」
	// 应答 = keyAuth 才放行；超时按原口径不阻塞签发（失败由 lego 上报，
	// cert 审计 error 行兜底）。
	p.mgr.awaitChallengePathLive(token, keyAuth)
	return nil
}

// CleanUp 实现 challenge.Provider。
func (p *challengeProvider) CleanUp(_, token, _ string) error {
	p.mgr.vw.removeChallenge(token)
	return nil
}

var _ challenge.Provider = (*challengeProvider)(nil)

// awaitChallengePathLive 主动探测「经 Traefik 的挑战路径」直至应答 = keyAuth
// ——把 awaitServed 的「Traefik 已取走」收敛确认推进到「Traefik 已应用并
// 反代回控制面」的端到端确认。预算 6s（200ms × 30，超时按 awaitServed 同款
// 「不阻塞签发」口径放行，失败形态由 lego 上报 + cert 审计 error 行兜底）。
// 探测出口 = swarm advertise（与 Traefik provider endpoint 同源地址），入口 =
// 平台 HTTP 端口（挑战路由所在 entrypoint）。
func (m *Manager) awaitChallengePathLive(token, keyAuth string) {
	u, err := neturl.Parse(m.responderURLValue())
	if err != nil || u.Hostname() == "" {
		return // 出口未知（单测/未收敛形态）：保持「不阻塞」语义
	}
	target := fmt.Sprintf("http://%s%s%s",
		net.JoinHostPort(u.Hostname(), fmt.Sprint(m.cfg.HTTPPort)), acmeChallengePathPrefix, token)
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := m.nowFunc().Add(6 * time.Second)
	for m.nowFunc().Before(deadline) {
		resp, getErr := client.Get(target)
		if getErr == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK && string(body) == keyAuth {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	m.log.Warn("ingress: challenge path did not serve keyauth within budget "+
		"(proceeding; validation may fail)", "token", token[:8]+"…")
}

// ensureAccount 加载（或创建）ACME 账号：私钥文件 + 注册 URI sidecar。
// E1（S19）：m.user 缓存的读写统一走 m.mu——原「无锁读 + invalidateAccount
// 有锁写」在 -race 下竞态；构建段（文件 IO/注册）持锁外执行，发布时
// double-check（并发先到者胜出，后到者复用）。
func (m *Manager) ensureAccount(ctx context.Context) (*acmeUser, error) {
	m.mu.Lock()
	cached := m.user
	m.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	keyPath := m.cfg.ACME.AccountKeyFile
	if keyPath == "" {
		keyPath = accountKeyPath(m.cfg.CertDir)
	}
	key, err := loadOrCreateAccountKey(keyPath)
	if err != nil {
		return nil, err
	}
	user := &acmeUser{EmailAddr: m.cfg.ACME.Email, key: key}

	sidecar := keyPath + ".json"
	var reg registration.Resource
	if sb, rerr := os.ReadFile(sidecar); rerr == nil { //nolint:gosec // 路径来自操作员配置（ingress.acme.*），非不可信输入
		_ = json.Unmarshal(sb, &reg) // 损坏 sidecar = 重新注册（幂等安全）
	}
	user.Reg = &reg

	if reg.URI == "" {
		// 客户端只在需要注册时构造（sidecar 命中已注册账号则不发起 CA
		// 目录请求——E1 测试的离线前提；生产 Obtain 路径自带客户端）。
		client, err := m.newClient(ctx, user)
		if err != nil {
			return nil, err
		}
		registered, rerr := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if rerr != nil {
			return nil, fmt.Errorf("ingress: acme register: %w", rerr)
		}
		user.Reg = registered
		af := acmeAccountFile{Email: user.EmailAddr}
		if user.Reg != nil {
			af.Registration.URI = user.Reg.URI
		}
		if raw, jerr := json.Marshal(af); jerr == nil {
			_ = os.WriteFile(sidecar, raw, 0o600)
		}
	}
	m.mu.Lock()
	if m.user != nil {
		// 并发先到者已发布账号：复用（同私钥注册幂等，不重复建号）。
		winner := m.user
		m.mu.Unlock()
		return winner, nil
	}
	m.user = user
	m.mu.Unlock()
	return user, nil
}

// loadOrCreateAccountKey 读取（或生成）账号私钥（PEM PKCS8，0600）。
func loadOrCreateAccountKey(keyPath string) (crypto.PrivateKey, error) {
	raw, err := os.ReadFile(keyPath) //nolint:gosec // 路径来自操作员配置（ingress.acme.account_key_file），非不可信输入
	if errors.Is(err, os.ErrNotExist) {
		generated, gerr := GenerateAccountKey()
		if gerr != nil {
			return nil, gerr
		}
		// 目录先行（cert_dir 可能尚未创建——首签发早于首次证书落盘）。
		if merr := os.MkdirAll(filepath.Dir(keyPath), 0o750); merr != nil {
			return nil, fmt.Errorf("ingress: create account key dir: %w", merr)
		}
		if werr := os.WriteFile(keyPath, generated, 0o600); werr != nil {
			return nil, fmt.Errorf("ingress: write acme account key: %w", werr)
		}
		raw = generated
	} else if err != nil {
		return nil, fmt.Errorf("ingress: read acme account key: %w", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("ingress: acme account key %s is not PEM", keyPath)
	}
	parsed, kerr := x509.ParsePKCS8PrivateKey(block.Bytes)
	if kerr != nil {
		return nil, fmt.Errorf("ingress: parse acme account key: %w", kerr)
	}
	return parsed, nil
}

// accountKeyPath 是账号私钥缺省路径（<CertDir>/acme-account.key）。
func accountKeyPath(certDir string) string {
	return filepath.Join(certDir, "acme-account.key")
}

// newClient 构造 lego 客户端（CA 目录 URL + 可选 CA 池；KeyType EC256）。
func (m *Manager) newClient(_ context.Context, user registration.User) (*lego.Client, error) {
	cfg := lego.NewConfig(user)
	cfg.CADirURL = m.cfg.ACME.CADirURL
	cfg.Certificate.KeyType = certcrypto.EC256
	if m.cfg.ACME.CAPoolFile != "" {
		pool := x509.NewCertPool()
		raw, err := os.ReadFile(m.cfg.ACME.CAPoolFile)
		if err != nil {
			return nil, fmt.Errorf("ingress: read acme ca pool: %w", err)
		}
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("ingress: acme ca pool %s has no usable PEM certificate", m.cfg.ACME.CAPoolFile)
		}
		cfg.HTTPClient = &http.Client{
			Timeout:   30 * time.Second,
			Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
		}
	}
	client, err := lego.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("ingress: construct acme client: %w", err)
	}
	return client, nil
}

// ensureCertificate 保证 app 域名集的证书就绪：无/域名集变化/进入续期窗口
// 才签发；已就绪直接返回既有证书引用。审计动作区分 issued/renewed（失败
// 走 *_failed error 行——错误码恒 E_ROUTE_PUBLISH_FAILED 域，签名材料
// 失败不路由降级外的额外信封）。
//
// 账号失效自愈：CA 侧账号丢失（pebble 重启/账号库重置）时 Obtain 报
// accountDoesNotExist——清除注册 sidecar 重新注册并重试一次。
func (m *Manager) ensureCertificate(ctx context.Context, appID, app string, domains []string, renewing bool) (*CertificateRef, error) {
	if !m.cfg.ACME.ACMEEnabled() {
		return nil, nil
	}
	// E1（S19）：签发全程串行（issueMu，v0.1 单签发容量——注释见 Manager
	// 字段）：并发调用方在此排队，后到者重读证书库命中刚落盘的证书。
	m.issueMu.Lock()
	defer m.issueMu.Unlock()
	existing, err := m.certs.Load(app)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		existing = nil
	default:
		return nil, fmt.Errorf("ingress: load cert of %s: %w", app, err)
	}
	if !needsRenewal(existing, domains, m.nowFunc(), m.cfg.RenewBefore) {
		return &CertificateRef{App: app, SHA256: existing.SHA256, NotAfter: existing.NotAfter.UnixNano()}, nil
	}
	ref, err := m.obtainAndRegister(ctx, appID, app, domains, renewing, existing)
	if err == nil {
		return ref, nil
	}
	m.writeCertAudit(ctx, renewing, app, existing, err)
	return nil, err
}

// obtainAndRegister 是一次「账号 → Obtain（含账号失效自愈）→ 落盘/
// 台账」的完整签发（账号自愈段与平台证书共用 obtainPEMWithHeal）。
func (m *Manager) obtainAndRegister(ctx context.Context, appID, app string, domains []string, renewing bool, existing *CertificatePair) (*CertificateRef, error) {
	certPEM, keyPEM, err := m.obtainPEMWithHeal(ctx, app, domains)
	if err != nil {
		return nil, err
	}
	pair, err := ParsePair(app, domains, certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	if err := m.certs.Save(pair); err != nil {
		return nil, err
	}
	// 证书分发经动态配置内联下发（E1-2，D-MN-4）：落盘即真源就绪，TLS 段
	// 随调用方的 publishWithCerts 收敛进视图（不再经证书卷/seed 容器同步
	// ——该路径已退役）。
	// 台账登记（逐域名；行已随发布同步存在——签发晚于域名撤销的竞态
	// 由 ErrDomainNotFound 显式跳过，不静默丢事实：日志留痕）。
	for _, d := range domains {
		if err := m.store.SetDomainCert(ctx, appID, d, pair.SHA256, pair.NotAfter); err != nil {
			if errors.Is(err, state.ErrDomainNotFound) {
				m.log.Warn("ingress: cert issued for revoked domain row (skip registration)", "app", app, "domain", d)
				continue
			}
			return nil, err
		}
	}
	m.writeCertAudit(ctx, renewing, app, pair, nil)
	return &CertificateRef{App: app, SHA256: pair.SHA256, NotAfter: pair.NotAfter.UnixNano()}, nil
}

// legoObtain 是 obtainFn 的生产实现（E1 注入缝默认值）：lego 客户端 +
// HTTP-01 挑战 provider + Obtain，返回证书链/私钥 PEM。
func (m *Manager) legoObtain(ctx context.Context, app string, user registration.User, domains []string) ([]byte, []byte, error) {
	client, err := m.newClient(ctx, user)
	if err != nil {
		return nil, nil, err
	}
	if err := client.Challenge.SetHTTP01Provider(&challengeProvider{mgr: m}); err != nil {
		return nil, nil, fmt.Errorf("ingress: set http-01 provider: %w", err)
	}
	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: append([]string{}, domains...),
		Bundle:  true,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("ingress: acme obtain for %s: %w", app, err)
	}
	return res.Certificate, res.PrivateKey, nil
}

// invalidateAccount 丢弃缓存的 ACME 账号与注册 sidecar（账号失效自愈的
// 清理步；下次 ensureAccount 重新生成/注册）。
func (m *Manager) invalidateAccount() {
	keyPath := m.cfg.ACME.AccountKeyFile
	if keyPath == "" {
		keyPath = accountKeyPath(m.cfg.CertDir)
	}
	_ = os.Remove(keyPath + ".json") // 注册 URI 缓存失效（私钥保留复用）
	m.mu.Lock()
	m.user = nil
	m.mu.Unlock()
}

// writeCertAudit 写签发/续期审计行（单一写事务；失败只降级日志——证书
// 事实已落盘，审计失败不回滚事实；与 build.finish 审计纪律同构）。
func (m *Manager) writeCertAudit(ctx context.Context, renewing bool, app string, pair *CertificatePair, issueErr error) {
	action := "ingress.cert_issued"
	if renewing {
		action = "ingress.cert_renewed"
	}
	result := "ok"
	errCode := ""
	domainsJSON := `[]`
	if pair != nil {
		raw, err := json.Marshal(pair.Domains)
		if err == nil {
			domainsJSON = string(raw)
		}
	}
	diff := fmt.Sprintf(`{"app":%s,"domains":%s,"sha256":%q,"not_after":%q}`,
		jsonQuote(app), domainsJSON, certSHA(pair), certNotAfter(pair))
	if issueErr != nil {
		if renewing {
			action = "ingress.cert_renew_failed"
		} else {
			action = "ingress.cert_issue_failed"
		}
		result = "error"
		errCode = "E_ROUTE_PUBLISH_FAILED"
		diff = fmt.Sprintf(`{"app":%s,"error":%s}`, jsonQuote(app), jsonQuote(issueErr.Error()))
	}
	auditErr := m.store.InTx(ctx, func(tx *state.Tx) error {
		return tx.WriteAudit(ctx, state.AuditEntry{
			Actor:       "system",
			Action:      action,
			Target:      "app:" + app,
			Result:      result,
			ErrorCode:   errCode,
			DiffSummary: diff,
		})
	})
	if auditErr != nil {
		m.log.Warn("ingress: write cert audit", "app", app, "error", auditErr)
	}
}

// certSHA / certNotAfter 是审计 diff 字段的 nil 安全取值。
func certSHA(pair *CertificatePair) string {
	if pair == nil {
		return ""
	}
	return pair.SHA256
}

func certNotAfter(pair *CertificatePair) string {
	if pair == nil || pair.NotAfter.IsZero() {
		return ""
	}
	return pair.NotAfter.Format(time.RFC3339)
}

// jsonQuote 是审计 diff 的最小 JSON 字符串转义（encoding/json 语义）。
func jsonQuote(s string) string {
	raw, err := json.Marshal(s)
	if err != nil {
		return `"?"`
	}
	return string(raw)
}
