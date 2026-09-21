package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/oklog/ulid/v2"
)

// env_vars 表读写（architecture §2.3 权威态「env 密文」+ 变量合并链平台层，
// 三层优先链 env_file < environment < 平台 env_vars）。
//
// 生效语义（v0.1；S16-C4 契约统一——以引擎现行为准，与架构 §2.4 变量
// 合并行一致）：SetAppEnv 创建/更新行并把 status 置 pending——「随下次
// 部署生效」；发布引擎合并链（internal/envlayer）消费**全量行**
//（ListAppEnv：pending 与 effective 都参与合并——部署是 pending 的消费
// 点，随本次部署注入），部署成功后由 MarkAppEnvEffective 统一提升为
// effective；失败不提升、下次部署重试。value 列存密文（internal/secrets
// envelope 加密），审计/事件只落键名（state-model §2.9：secret 值禁止
// 进入事件/审计/日志）。

// EnvVarStatus 是 env_vars 生效状态位。
type EnvVarStatus string

const (
	// EnvStatusPending 已创建/更新但未部署消费（当前运行 env 不含它）。
	EnvStatusPending EnvVarStatus = "pending"
	// EnvStatusEffective 已随部署生效（合并链可消费）。
	EnvStatusEffective EnvVarStatus = "effective"
)

// ReservedEnvPrefix 是平台保留 env 键前缀（managed-databases 设计 §2.5，
// E4/FZ-1：`FLEETLY_`，大小写敏感——模板连接串物化键 FLEETLY_DB_<NAME>_*
// 的名字空间；用户 platform 写撞前缀 → E_ENV_KEY_RESERVED，system 物化
// 写放行）。与 label 保留前缀 fleetly.*（E_LABEL_RESERVED）同型的平台
// 保留名字空间纪律。
const ReservedEnvPrefix = "FLEETLY_"

// envVarsScanCols 是 env 行查询列清单（新增列只加在此与扫描函数）。
const envVarsScanCols = `id, app_id, key, value, source, status, created_at, updated_at`

// EnvVar 是平台层环境变量行（value 为调用方传入的存储形态——本层不解释
// 密文/明文，加密边界在 internal/secrets 与其调用方）。
type EnvVar struct {
	ID        string
	AppID     string
	Key       string
	Value     string
	Source    string // platform | system（模板连接串留位，只读）
	Status    EnvVarStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ErrEnvNotFound 表示目标 env 键不存在。
var ErrEnvNotFound = errors.New("env var not found")

// ValidateEnvKey 校验平台 env 键：非空、无空白、无 = / NUL（注入安全下限，
// 字符集从宽——compose/Docker 允许点与连字符；窄白名单属过度设计）。
func ValidateEnvKey(key string) error {
	if key == "" {
		return errors.New("env key is empty")
	}
	if strings.TrimSpace(key) != key {
		return fmt.Errorf("env key %q has surrounding whitespace", key)
	}
	for _, r := range key {
		switch r {
		case '=', 0:
			return fmt.Errorf("env key %q contains forbidden character %q", key, r)
		case ' ', '\t', '\n', '\r':
			return fmt.Errorf("env key %q contains whitespace", key)
		}
	}
	return nil
}

// SetAppEnv 创建或更新平台 env（同 app 同 key 唯一）：写值并把 status 重置
// 为 pending（既有 effective 行被覆盖同样回到 pending——生效语义 = 下次部署
// 消费）。审计同事务 fail-closed，diff 摘要只含键名与状态，不含值。
// source 取 platform / system；留空回落 platform。
//
// 保留名字空间守卫（managed-databases 设计 §2.5，E4/FZ-1）：source=platform
// 的用户写撞 `FLEETLY_` 前缀（大小写敏感）→ E_ENV_KEY_RESERVED（422）。前缀
// 是平台保留名字空间（模板连接串物化键 FLEETLY_DB_<NAME>_*），守卫同时封死
// 「system 行被用户 upsert 劫持 source」路径——现状 upsert 会改写 source，
// 若无本守卫，用户对 FLEETLY_* 键的 platform 写将顶掉 S4 物化的 system 行。
// source=system 的写是平台内部物化（DB 连接串等），任意键放行。
func (s *Store) SetAppEnv(ctx context.Context, appID, key, value, source string) (EnvVar, error) {
	if err := ValidateEnvKey(key); err != nil {
		return EnvVar{}, fmt.Errorf("state: %w", err)
	}
	if source == "" {
		source = "platform"
	}
	if source != "platform" && source != "system" {
		return EnvVar{}, fmt.Errorf("state: env source %q not in {platform, system}", source)
	}
	if source == "platform" && strings.HasPrefix(key, ReservedEnvPrefix) {
		return EnvVar{}, apperr.New("E_ENV_KEY_RESERVED",
			"env key %q uses the reserved FLEETLY_ prefix (platform-managed namespace; user writes are rejected)", key).
			WithContext("key", key)
	}
	var out EnvVar
	err := s.InTx(ctx, func(tx *Tx) error {
		row, err := tx.SetAppEnv(ctx, appID, key, value, source)
		if err != nil {
			return err
		}
		out = row
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:  "system",
			Action: "app.env_set",
			Target: "app:" + appID,
			Result: "ok",
			// 值永不入审计（state-model §2.9）：摘要只有键名与状态位。
			// B4：经 DiffSummary 构造（json.Marshal 转义）。
			DiffSummary: DiffSummary("key", key, "status", "pending"),
		})
	})
	if err != nil {
		return EnvVar{}, fmt.Errorf("state: set env %s: %w", key, err)
	}
	return out, nil
}

// SetAppEnv 是事务内 env 写（供与事件等同事务组合）。
func (t *Tx) SetAppEnv(ctx context.Context, appID, key, value, source string) (EnvVar, error) {
	now := nowNano()
	id := ulid.Make().String()
	const q = `INSERT INTO env_vars (id, app_id, key, value, source, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'pending', ?, ?)
		ON CONFLICT(app_id, key) DO UPDATE SET
			value = excluded.value,
			source = excluded.source,
			status = 'pending',
			updated_at = excluded.updated_at`
	if _, err := t.ExecContext(ctx, q, id, appID, key, value, source, now, now); err != nil {
		return EnvVar{}, fmt.Errorf("state: upsert env %s: %w", key, err)
	}
	return t.GetAppEnv(ctx, appID, key)
}

// GetAppEnv 取单条 env（任意状态位）；不存在返回 ErrEnvNotFound。
func (s *Store) GetAppEnv(ctx context.Context, appID, key string) (EnvVar, error) {
	const q = `SELECT ` + envVarsScanCols + ` FROM env_vars WHERE app_id = ? AND key = ?`
	row := s.db.QueryRowContext(ctx, q, appID, key)
	return scanEnvVar(row)
}

// GetAppEnv 是事务内取单条（供写后回读）。
func (t *Tx) GetAppEnv(ctx context.Context, appID, key string) (EnvVar, error) {
	const q = `SELECT ` + envVarsScanCols + ` FROM env_vars WHERE app_id = ? AND key = ?`
	return scanEnvVar(t.QueryRowContext(ctx, q, appID, key))
}

// DeleteAppEnv 删除单条 env（幂等：键不存在返回 ErrEnvNotFound）。审计同
// 事务 fail-closed，摘要只含键名。
func (s *Store) DeleteAppEnv(ctx context.Context, appID, key string) error {
	return s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM env_vars WHERE app_id = ? AND key = ?`, appID, key)
		if err != nil {
			return fmt.Errorf("state: delete env %s: %w", key, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read env delete count: %w", err)
		}
		if n == 0 {
			return ErrEnvNotFound
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:       "system",
			Action:      "app.env_removed",
			Target:      "app:" + appID,
			Result:      "ok",
			DiffSummary: DiffSummary("key", key), // B4：构造器替换手拼 JSON
		})
	})
}

// DeleteAppEnv 是事务内的 env 行删除原语（供与引用登记等业务写组合在同一
// 事务——E4 planner 的物化行清理：label 移除后同事务删除失配的 system
// 物化行，managed-databases §2.4）。与 Store 形态的差异：幂等（无行时返回
// false 不报错）、不在本层写审计（组合事务的调用方决定审计粒度——planner
// 的派生登记维护不逐键落审计，登记语义随部署记录）。
func (t *Tx) DeleteAppEnv(ctx context.Context, appID, key string) (bool, error) {
	res, err := t.ExecContext(ctx, `DELETE FROM env_vars WHERE app_id = ? AND key = ?`, appID, key)
	if err != nil {
		return false, fmt.Errorf("state: delete env %s: %w", key, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("state: read env delete count: %w", err)
	}
	return n > 0, nil
}

// ListAppEnv 返回该 app 全部 env 行（含 pending，按 key 字典序）。消费面
// 三处：发布引擎合并链（pending 参与合并——部署即消费点，S16-C4）、CLI/
// API 展示（键名/来源/状态位投影）、日志脱敏值集（pending 值同样不得进
// 日志）。
func (s *Store) ListAppEnv(ctx context.Context, appID string) ([]EnvVar, error) {
	const q = `SELECT ` + envVarsScanCols + ` FROM env_vars WHERE app_id = ? ORDER BY key ASC`
	return queryEnvVars(ctx, s.db, q, appID)
}

// MarkAppEnvEffective 把该 app 全部 pending 行提升为 effective（部署成功
// 终态后的消费点，observing.go succeedDeployment 在 env 注入并成功后调用；
// kind=rollback 不提升——重放的 env 随快照，pending 未被本次部署消费），
// 返回提升行数。幂等。
func (s *Store) MarkAppEnvEffective(ctx context.Context, appID string) (int, error) {
	var n int64
	err := s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE env_vars SET status = 'effective', updated_at = ?
			WHERE app_id = ? AND status = 'pending'`, nowNano(), appID)
		if err != nil {
			return fmt.Errorf("state: promote env: %w", err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read env promote count: %w", err)
		}
		if n > 0 {
			return tx.WriteAudit(ctx, AuditEntry{
				Actor:       "system",
				Action:      "app.env_applied",
				Target:      "app:" + appID,
				Result:      "ok",
				DiffSummary: DiffSummary("promoted", n), // B4：构造器替换手拼 JSON（数值保持非引号形态）
			})
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("state: mark env effective: %w", err)
	}
	return int(n), nil
}

// scanEnvVar 从单行构造 EnvVar（row 接口同时覆盖 *sql.Row 与 *sql.Rows）。
func scanEnvVar(row interface{ Scan(dest ...any) error }) (EnvVar, error) {
	var v EnvVar
	var status string
	var created, updated int64
	if err := row.Scan(&v.ID, &v.AppID, &v.Key, &v.Value, &v.Source, &status, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return EnvVar{}, ErrEnvNotFound
		}
		return EnvVar{}, fmt.Errorf("state: scan env var: %w", err)
	}
	v.Status = EnvVarStatus(status)
	v.CreatedAt = time.Unix(0, created).UTC()
	v.UpdatedAt = time.Unix(0, updated).UTC()
	return v, nil
}

// queryEnvVars 执行多行 env 查询并按序返回。
func queryEnvVars(ctx context.Context, db *sql.DB, query string, args ...any) ([]EnvVar, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("state: query env vars: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []EnvVar
	for rows.Next() {
		v, err := scanEnvVar(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate env vars: %w", err)
	}
	return out, nil
}
