package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// domains 表读写（T2.15/T2.16 入口与域名票）：路由域名台账 = 入口路由
// 发布的事实源（app/service/domain + 证书材料登记）。写入方唯一 =
// internal/ingress（发布时按归一化 compose 对账同步）；读方 = ingress
// 配置合成与 fleetly domains CLI。域名唯一性由表约束（domain UNIQUE）
// 与 compose 层冲突校验（E_DOMAIN_CONFLICT）共同承载。

// Domain 是一条域名台账行（T 线 IMPL-T1-1 起为域名资源真值：per-domain
// {domain, service, port, protocol, cert_mode}，写入方 = API CRUD 与发布
// 种子；发布渲染按行消费）。
type Domain struct {
	ID      string
	AppID   string
	Service string
	Domain  string
	// Port 是路由目标端口（旧行为 = compose expose 首端口；API 资源面为
	// 显式声明；'' = 未同步）。
	Port string
	// Protocol 是后端协议（http | h2c；h2c = Traefik 后端 scheme=h2c）。
	Protocol string
	// CertMode 是证书模式（http01 | wildcard；本票只落存储与校验）。
	CertMode string
	// CertSHA256 是证书 PEM（链 + 叶）内容 sha256 hex；'' = 尚无证书。
	CertSHA256 string
	// CertNotAfter 是叶证书 NotAfter；零值 = 尚无证书。
	CertNotAfter time.Time
	// CertUpdatedAt 是证书登记最近一次写入；零值 = 从未。
	CertUpdatedAt time.Time
	// CreatedAt 是域名首 declarations 时间。
	CreatedAt time.Time
}

// domainScan 列清单（port/cert 列来自迁移 00006、protocol/cert_mode 来自
// 迁移 00023，旧库打开即迁移后形态）。
const domainColumns = `id, app_id, service, domain, port, protocol, cert_mode, cert_sha256, cert_not_after, cert_updated_at, created_at`

// scanDomain 从单行构造 Domain（row 接口同时覆盖 *sql.Row 与 *sql.Rows）。
func scanDomain(row interface{ Scan(dest ...any) error }) (Domain, error) {
	var d Domain
	var notAfter, updated sql.NullInt64
	var created int64
	if err := row.Scan(&d.ID, &d.AppID, &d.Service, &d.Domain, &d.Port, &d.Protocol, &d.CertMode, &d.CertSHA256, &notAfter, &updated, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Domain{}, ErrDomainNotFound
		}
		return Domain{}, fmt.Errorf("state: scan domain: %w", err)
	}
	if notAfter.Valid {
		d.CertNotAfter = time.Unix(0, notAfter.Int64).UTC()
	}
	if updated.Valid {
		d.CertUpdatedAt = time.Unix(0, updated.Int64).UTC()
	}
	d.CreatedAt = time.Unix(0, created).UTC()
	return d, nil
}

// ErrDomainNotFound 表示域名台账行不存在。
var ErrDomainNotFound = errors.New("domain not found")

// ErrDomainConflict 表示域名（host）已被占用：domain UNIQUE 是全局约束，
// 同一 host 只属于一个 app 的一个服务（与 compose 层 E_DOMAIN_CONFLICT
// 同口径的写面强约束）。
var ErrDomainConflict = errors.New("domain already exists")

// MaxDomainsPerApp / MaxDomainsPerService 是域名资源上限（架构 §2.4：
// 每 app ≤10、每服务 ≤5；与 compose label 解析面的私有常量同值——compose
// 常量注释与本处互指，两处变更必须同步）。state 写面（API CRUD）在此
// fail-closed；种子路径经 compose 解析期同值门。
const (
	MaxDomainsPerApp     = 10
	MaxDomainsPerService = 5
)

// DomainLimitError 是域名配额超限（Scope = "app" | "service"；Count 是
// 现值，供 API 层点名构造 4xx 文案）。
type DomainLimitError struct {
	Scope string
	Limit int
	Count int
}

func (e *DomainLimitError) Error() string {
	return fmt.Sprintf("domain limit exceeded (%s scope: %d/%d)", e.Scope, e.Count, e.Limit)
}

// DomainInput 是一次域名资源写入（API CRUD 的载荷；Domain 为归一化后的
// host——归一化属消费方职责，state 只承载事实）。
type DomainInput struct {
	Domain   string
	Service  string
	Port     string
	Protocol string
	CertMode string
}

// GetAppDomain 读取单条域名行（app 内寻址；不存在返回 ErrDomainNotFound）。
func (s *Store) GetAppDomain(ctx context.Context, appID, domain string) (Domain, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+domainColumns+` FROM domains WHERE app_id = ? AND domain = ?`, appID, domain)
	return scanDomain(row)
}

// CreateAppDomain 创建域名资源行（配额与冲突检查在同一事务内 fail-closed；
// 无行则 ErrDomainNotFound 路径不适用）。
func (s *Store) CreateAppDomain(ctx context.Context, appID string, in DomainInput) (Domain, error) {
	out := Domain{
		AppID: appID, Service: in.Service, Domain: in.Domain,
		Port: in.Port, Protocol: in.Protocol, CertMode: in.CertMode,
	}
	err := s.InTx(ctx, func(tx *Tx) error {
		if err := tx.checkDomainQuota(ctx, appID, in.Service, ""); err != nil {
			return err
		}
		var occupied int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM domains WHERE domain = ?`, in.Domain).Scan(&occupied); err != nil {
			return fmt.Errorf("state: check domain conflict %s: %w", in.Domain, err)
		}
		if occupied > 0 {
			return ErrDomainConflict
		}
		out.ID = ulid.Make().String()
		now := nowNano()
		out.CreatedAt = time.Unix(0, now).UTC()
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO domains (id, app_id, service, domain, port, protocol, cert_mode, created_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			out.ID, appID, in.Service, in.Domain, in.Port, in.Protocol, in.CertMode, now); err != nil {
			return fmt.Errorf("state: insert domain %s: %w", in.Domain, err)
		}
		return nil
	})
	if err != nil {
		return Domain{}, err
	}
	return out, nil
}

// UpdateAppDomain 更新域名资源的 service/port/protocol/cert_mode（host 是
// 资源身份，不改名——改名 = 删除 + 重建，与 env key 同口径）。空值字段
// 由调用方先行解析（API 面空 = 保持现值），本层只写事实。
func (s *Store) UpdateAppDomain(ctx context.Context, appID, domain string, in DomainInput) (Domain, error) {
	var out Domain
	err := s.InTx(ctx, func(tx *Tx) error {
		existing, err := getAppDomainTx(ctx, tx, appID, domain)
		if err != nil {
			return err
		}
		if in.Service != existing.Service {
			if err := tx.checkDomainQuota(ctx, appID, in.Service, existing.ID); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE domains SET service = ?, port = ?, protocol = ?, cert_mode = ? WHERE id = ?`,
			in.Service, in.Port, in.Protocol, in.CertMode, existing.ID); err != nil {
			return fmt.Errorf("state: update domain %s: %w", domain, err)
		}
		existing.Service = in.Service
		existing.Port = in.Port
		existing.Protocol = in.Protocol
		existing.CertMode = in.CertMode
		out = existing
		return nil
	})
	if err != nil {
		return Domain{}, err
	}
	return out, nil
}

// RemoveAppDomain 删除域名资源行（幂等不做：不存在 404——与 RemoveEnv
// 同口径）。
func (s *Store) RemoveAppDomain(ctx context.Context, appID, domain string) error {
	return s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`DELETE FROM domains WHERE app_id = ? AND domain = ?`, appID, domain)
		if err != nil {
			return fmt.Errorf("state: delete domain %s: %w", domain, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read domain delete count: %w", err)
		}
		if n == 0 {
			return ErrDomainNotFound
		}
		return nil
	})
}

// getAppDomainTx 是事务内单行读取（scanDomain 的错误归一已含 ErrDomainNotFound）。
func getAppDomainTx(ctx context.Context, tx *Tx, appID, domain string) (Domain, error) {
	row := tx.QueryRowContext(ctx,
		`SELECT `+domainColumns+` FROM domains WHERE app_id = ? AND domain = ?`, appID, domain)
	return scanDomain(row)
}

// checkDomainQuota 执行写面配额检查（excludeID 非空 = 更新场景排除自身行）。
// app 与 service 两级同时检查：超限返回 *DomainLimitError（4xx 点名材料）。
func (t *Tx) checkDomainQuota(ctx context.Context, appID, service, excludeID string) error {
	var appCount, serviceCount int
	if err := t.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM domains WHERE app_id = ? AND id <> ?`, appID, excludeID).Scan(&appCount); err != nil {
		return fmt.Errorf("state: count domains for app %s: %w", appID, err)
	}
	if appCount >= MaxDomainsPerApp {
		return &DomainLimitError{Scope: "app", Limit: MaxDomainsPerApp, Count: appCount}
	}
	if err := t.QueryRowContext(ctx,
		`SELECT COUNT(1) FROM domains WHERE app_id = ? AND service = ? AND id <> ?`,
		appID, service, excludeID).Scan(&serviceCount); err != nil {
		return fmt.Errorf("state: count domains for service %s: %w", service, err)
	}
	if serviceCount >= MaxDomainsPerService {
		return &DomainLimitError{Scope: "service", Limit: MaxDomainsPerService, Count: serviceCount}
	}
	return nil
}

// DomainServiceRoutes 是一次 per-service 域名声明（发布同步的输入单元）。
type DomainServiceRoutes struct {
	Service string
	// Port 是路由目标端口（compose expose 首端口）。
	Port    string
	Domains []string
}

// ReplaceAppDomains 已删除（IMPL-T1-1 单一写点仲裁）：域名行不再是发布声明
// 集的对账产物——写面 = API CRUD（Create/Update/Remove）与首部署播种
//（SeedAppDomainsIfEmpty），撤销 = RemoveAppDomain/DeleteAppDomains，因此
//「声明集之外整组删除」的对账语义随 label 降级为 bootstrap 种子而退役。

// SeedAppDomainsIfEmpty 是首部署播种的原子仲裁写（IMPL-T1-1）：单写事务内
// 「state 无行才播种」——返回 false = 已有行（调用方不改行，label 忽略），
// true = 本次播种完成。检空与插入同事务（BEGIN IMMEDIATE 串行化写者）：
// 并发 API 写行不会被播种路径删除/覆盖；播种行取平台缺省
// protocol=http / cert_mode=http01（label 无协议面）。
func (s *Store) SeedAppDomainsIfEmpty(ctx context.Context, appID string, declared []DomainServiceRoutes) (bool, error) {
	seeded := false
	err := s.InTx(ctx, func(tx *Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(1) FROM domains WHERE app_id = ?`, appID).Scan(&n); err != nil {
			return fmt.Errorf("state: count domains for seed %s: %w", appID, err)
		}
		if n > 0 {
			return nil
		}
		now := nowNano()
		for _, svc := range declared {
			for _, d := range svc.Domains {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO domains (id, app_id, service, domain, port, protocol, cert_mode, created_at)
					 VALUES (?, ?, ?, ?, ?, 'http', 'http01', ?)`,
					ulid.Make().String(), appID, svc.Service, d, svc.Port, now); err != nil {
					return fmt.Errorf("state: seed domain %s: %w", d, err)
				}
			}
		}
		seeded = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return seeded, nil
}

// DeleteAppDomains 删除应用的全部域名台账行（应用移除/路由撤销时调用；
// 幂等）。
func (s *Store) DeleteAppDomains(ctx context.Context, appID string) error {
	err := s.InTx(ctx, func(tx *Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM domains WHERE app_id = ?`, appID); err != nil {
			return fmt.Errorf("state: delete domains for app %s: %w", appID, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// ListAppDomains 返回应用的域名台账行（domain 字典序）。
func (s *Store) ListAppDomains(ctx context.Context, appID string) ([]Domain, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+domainColumns+` FROM domains WHERE app_id = ? ORDER BY domain`, appID)
	if err != nil {
		return nil, fmt.Errorf("state: query domains for app %s: %w", appID, err)
	}
	return collectDomains(rows)
}

// ListAllDomains 返回全部域名台账行（app_id, domain 字典序；全量配置
// 合成的事实源）。
func (s *Store) ListAllDomains(ctx context.Context) ([]Domain, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+domainColumns+` FROM domains ORDER BY app_id, domain`)
	if err != nil {
		return nil, fmt.Errorf("state: query all domains: %w", err)
	}
	return collectDomains(rows)
}

// SetDomainCert 登记证书材料（sha256 + 叶到期时间；同事务刷新登记时间）。
// 未命中（域名已被同步删除）返回 ErrDomainNotFound——签发晚于域名撤销的
// 竞态安全路径。
func (s *Store) SetDomainCert(ctx context.Context, appID, domain, certSHA256 string, notAfter time.Time) error {
	err := s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE domains SET cert_sha256 = ?, cert_not_after = ?, cert_updated_at = ?
			WHERE app_id = ? AND domain = ?`,
			certSHA256, notAfter.UnixNano(), nowNano(), appID, domain)
		if err != nil {
			return fmt.Errorf("state: update domain cert %s: %w", domain, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read domain cert update count: %w", err)
		}
		if n == 0 {
			return ErrDomainNotFound
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// collectDomains 收敛查询结果（rows 关闭与错误归一）。
func collectDomains(rows *sql.Rows) ([]Domain, error) {
	defer func() { _ = rows.Close() }()
	var out []Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate domains: %w", err)
	}
	return out, nil
}
