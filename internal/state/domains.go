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

// Domain 是一条域名台账行。
type Domain struct {
	ID      string
	AppID   string
	Service string
	Domain  string
	// Port 是路由目标端口（compose expose 首端口；'' = 未同步）。
	Port string
	// CertSHA256 是证书 PEM（链 + 叶）内容 sha256 hex；'' = 尚无证书。
	CertSHA256 string
	// CertNotAfter 是叶证书 NotAfter；零值 = 尚无证书。
	CertNotAfter time.Time
	// CertUpdatedAt 是证书登记最近一次写入；零值 = 从未。
	CertUpdatedAt time.Time
	// CreatedAt 是域名首 declarations 时间。
	CreatedAt time.Time
}

// domainScan 列清单（port/cert 列来自迁移 00006，旧库打开即迁移后形态）。
const domainColumns = `id, app_id, service, domain, port, cert_sha256, cert_not_after, cert_updated_at, created_at`

// scanDomain 从单行构造 Domain（row 接口同时覆盖 *sql.Row 与 *sql.Rows）。
func scanDomain(row interface{ Scan(dest ...any) error }) (Domain, error) {
	var d Domain
	var notAfter, updated sql.NullInt64
	var created int64
	if err := row.Scan(&d.ID, &d.AppID, &d.Service, &d.Domain, &d.Port, &d.CertSHA256, &notAfter, &updated, &created); err != nil {
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

// DomainServiceRoutes 是一次 per-service 域名声明（发布同步的输入单元）。
type DomainServiceRoutes struct {
	Service string
	// Port 是路由目标端口（compose expose 首端口）。
	Port    string
	Domains []string
}

// ReplaceAppDomains 把应用的域名台账对账到声明集（发布同步语义，
// architecture §2.4 域名行）：声明集内逐域名 upsert（归属 service/端口变更
// 即改写），台账中声明集之外的本应用行删除。幂等；单一写事务（fail-closed）。
func (s *Store) ReplaceAppDomains(ctx context.Context, appID string, declared []DomainServiceRoutes) error {
	return s.InTx(ctx, func(tx *Tx) error { return tx.ReplaceAppDomains(ctx, appID, declared) })
}

// ReplaceAppDomains 是事务内对账域名台账。
func (t *Tx) ReplaceAppDomains(ctx context.Context, appID string, declared []DomainServiceRoutes) error {
	now := nowNano()
	keep := map[string]string{} // domain → service
	portByService := map[string]string{}
	for _, svc := range declared {
		portByService[svc.Service] = svc.Port
		for _, d := range svc.Domains {
			keep[d] = svc.Service
		}
	}
	rows, err := t.QueryContext(ctx, `SELECT id, service, domain FROM domains WHERE app_id = ?`, appID)
	if err != nil {
		return fmt.Errorf("state: query domains for app %s: %w", appID, err)
	}
	type row struct {
		id, service, domain string
	}
	var existing []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.service, &r.domain); err != nil {
			_ = rows.Close()
			return fmt.Errorf("state: scan domain row: %w", err)
		}
		existing = append(existing, r)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("state: iterate domains: %w", err)
	}
	_ = rows.Close()

	existingByDomain := map[string]row{}
	for _, r := range existing {
		existingByDomain[r.domain] = r
	}
	// upsert 声明集。
	for _, r := range existing {
		svc, ok := keep[r.domain]
		if ok && svc == r.service {
			continue // 未变化
		}
		if !ok {
			if _, err := t.ExecContext(ctx, `DELETE FROM domains WHERE id = ?`, r.id); err != nil {
				return fmt.Errorf("state: delete domain %s: %w", r.domain, err)
			}
			continue
		}
		if _, err := t.ExecContext(ctx,
			`UPDATE domains SET service = ? WHERE id = ?`, svc, r.id); err != nil {
			return fmt.Errorf("state: move domain %s to service %s: %w", r.domain, svc, err)
		}
	}
	for d, svc := range keep {
		if _, ok := existingByDomain[d]; ok {
			continue
		}
		if _, err := t.ExecContext(ctx,
			`INSERT INTO domains (id, app_id, service, domain, port, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
			ulid.Make().String(), appID, svc, d, portByService[svc], now); err != nil {
			return fmt.Errorf("state: insert domain %s: %w", d, err)
		}
	}
	return nil
}

// SetAppServicePorts 回填既有域名行的路由端口（服务端口变化时对账；幂等）。
func (s *Store) SetAppServicePorts(ctx context.Context, appID string, portByService map[string]string) error {
	return s.InTx(ctx, func(tx *Tx) error {
		for svc, port := range portByService {
			if _, err := tx.ExecContext(ctx,
				`UPDATE domains SET port = ? WHERE app_id = ? AND service = ?`, port, appID, svc); err != nil {
				return fmt.Errorf("state: update domain port for service %s: %w", svc, err)
			}
		}
		return nil
	})
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
