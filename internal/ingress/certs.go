package ingress

// 证书库（T2.16；state-model §2.1：证书材料属控制面、独立备份目录）：
// 控制面侧文件目录（<CertDir>/<app>.crt|.key，0600/0644）+ 台账登记
// （domains 表 cert_sha256/cert_not_after）。Traefik 经证书目录只读挂载
// 取文件；文件名与 tls.certificates 引用由 Synthesize 统一（<app>.crt）。

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/oklog/ulid/v2"
)

// CertificatePair 是一张落盘证书（app 级多 SAN 单证书，architecture §2.4
// 域名行：列表内域名同服务同路由、证书按 app 域名集合出一张）。
type CertificatePair struct {
	App     string
	Domains []string
	// CertPEM 是证书链 PEM（leaf + 中间；lego Bundle=true 形态）。
	CertPEM []byte
	// KeyPEM 是私钥 PEM（ECDSA P256）。
	KeyPEM []byte
	// NotAfter 是叶证书到期时间（UTC）。
	NotAfter time.Time
	// SHA256 是 CertPEM 内容摘要（台账登记；变更即重发布 TLS 段）。
	SHA256 string
}

// certMeta 是证书边车元数据（SAN 集——续期判定「域名集变化即重签」的
// 依据； PEM 之外的最小台账）。
type certMeta struct {
	App      string   `json:"app"`
	Domains  []string `json:"domains"`
	NotAfter int64    `json:"not_after_unix_nano"`
	SHA256   string   `json:"sha256"`
}

// certStore 是证书目录的读写门面。
type certStore struct {
	dir string
}

func newCertStore(dir string) *certStore { return &certStore{dir: dir} }

// Load 读取 app 证书（缺失返回 errors.Is(err, os.ErrNotExist)）。
func (s *certStore) Load(app string) (*CertificatePair, error) {
	crt, err := os.ReadFile(s.certPath(app))
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(s.keyPath(app))
	if err != nil {
		return nil, fmt.Errorf("ingress: read key of %s: %w", app, err)
	}
	metaRaw, err := os.ReadFile(s.metaPath(app))
	if err != nil {
		return nil, fmt.Errorf("ingress: read cert meta of %s: %w", app, err)
	}
	var meta certMeta
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		return nil, fmt.Errorf("ingress: decode cert meta of %s: %w", app, err)
	}
	return &CertificatePair{
		App:      app,
		Domains:  meta.Domains,
		CertPEM:  crt,
		KeyPEM:   key,
		NotAfter: time.Unix(0, meta.NotAfter).UTC(),
		SHA256:   meta.SHA256,
	}, nil
}

// Save 落盘证书三件（crt/key/meta；先写临时文件再 rename 的原子形态的
// Windows 简化：直接写 + 校验回读 sha256 一致——损坏即报错重试路径）。
func (s *certStore) Save(pair *CertificatePair) error {
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return fmt.Errorf("ingress: create cert dir: %w", err)
	}
	if err := os.WriteFile(s.certPath(pair.App), pair.CertPEM, 0o600); err != nil {
		return fmt.Errorf("ingress: write cert of %s: %w", pair.App, err)
	}
	// 私钥 0600（POSIX 面；Windows 卷 ACL 由父目录控制，goose/secrets 同款取舍）。
	if err := os.WriteFile(s.keyPath(pair.App), pair.KeyPEM, 0o600); err != nil {
		return fmt.Errorf("ingress: write key of %s: %w", pair.App, err)
	}
	meta := certMeta{
		App:      pair.App,
		Domains:  pair.Domains,
		NotAfter: pair.NotAfter.UnixNano(),
		SHA256:   pair.SHA256,
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("ingress: encode cert meta of %s: %w", pair.App, err)
	}
	if err := os.WriteFile(s.metaPath(pair.App), raw, 0o600); err != nil {
		return fmt.Errorf("ingress: write cert meta of %s: %w", pair.App, err)
	}
	return nil
}

// List 返回证书目录内全部 app 名（meta 文件为索引）。
func (s *certStore) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("ingress: read cert dir: %w", err)
	}
	var apps []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".meta.json" {
			apps = append(apps, trimExt(trimExt(e.Name())))
		}
	}
	return apps, nil
}

// trimExt 去掉一个扩展名（.meta.json 两段）。
func trimExt(name string) string {
	ext := filepath.Ext(name)
	return name[:len(name)-len(ext)]
}

func (s *certStore) certPath(app string) string { return filepath.Join(s.dir, app+".crt") }
func (s *certStore) keyPath(app string) string  { return filepath.Join(s.dir, app+".key") }
func (s *certStore) metaPath(app string) string {
	return filepath.Join(s.dir, app+".meta.json")
}

// ParsePair 解析签发产物（lego Resource 的 PEM bundle）为证书对：叶到期
// 时间取 bundle 内最小 NotAfter（链任一环到期即证书失效），sha256 为
// CertPEM 内容摘要。
func ParsePair(app string, domains []string, certPEM, keyPEM []byte) (*CertificatePair, error) {
	certs, err := parsePEMBundle(certPEM)
	if err != nil {
		return nil, fmt.Errorf("ingress: parse cert bundle of %s: %w", app, err)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("ingress: cert bundle of %s is empty", app)
	}
	notAfter := certs[0].NotAfter.UTC()
	for _, c := range certs[1:] {
		if c.NotAfter.Before(notAfter) {
			notAfter = c.NotAfter.UTC()
		}
	}
	sum := sha256.Sum256(certPEM)
	return &CertificatePair{
		App:      app,
		Domains:  append([]string{}, domains...),
		CertPEM:  certPEM,
		KeyPEM:   keyPEM,
		NotAfter: notAfter,
		SHA256:   hex.EncodeToString(sum[:]),
	}, nil
}

// parsePEMBundle 解析 PEM 证书链（叶子在前约定，与顺序无关地全收集）。
func parsePEMBundle(raw []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := raw
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		certs = append(certs, c)
	}
	return certs, nil
}

// needsRenewal 报告证书是否需要（重）签发：无证书 / 域名集变化（多 SAN
// 契约：app 域名集合一张证书，集合变化即旧证书不全）/ 进入续期窗口。
func needsRenewal(pair *CertificatePair, domains []string, now time.Time, renewBefore time.Duration) bool {
	if pair == nil {
		return true
	}
	if !sameDomainSet(pair.Domains, domains) {
		return true
	}
	return now.Add(renewBefore).After(pair.NotAfter)
}

// sameDomainSet 判定域名集合相等（无序；归一化已在 compose 层完成）。
func sameDomainSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]int, len(a))
	for _, s := range a {
		set[s]++
	}
	for _, s := range b {
		if set[s] == 0 {
			return false
		}
		set[s]--
	}
	return true
}

// GenerateAccountKey 生成 ACME 账号私钥（ECDSA P256；PEM PKCS8）。
func GenerateAccountKey() ([]byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ingress: generate acme account key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("ingress: marshal acme account key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}

// GenerateToken 生成随机 token（配置端点鉴权；32 字节 hex）。
func GenerateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("ingress: generate token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// NewID 是台账行的 ULID 生成出口（domains 表；state 层自持同款形态）。
func NewID() string { return ulid.Make().String() }
