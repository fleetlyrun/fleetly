package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// db_references 读写（managed-databases 设计 §2.4）：引用方 app 服务对库
// 实例的声明关系倒排登记。单一真源 = 引用方 compose（服务 label
// fleetly.databases）——本表是「可从各 app 当前 revision 重建的派生登记」，
// planner 在部署 releasing 前同事务维护：先清该 app 全部行、再按当前
// 声明 upsert（全清 + 替换语义，label 移除 = 行删除）。库删除前置哨兵按
// db_id 反查：非空 → E_DB_REFERENCED（api 层 S2 映射，附引用清单）。

// ErrDatabaseReferenceNotFound 表示目标引用行不存在（幂等清理时的显式
// 信号——DeleteDatabaseReferencesForApp 的全清语义不产生该错误）。
var ErrDatabaseReferenceNotFound = errors.New("database reference not found")

// DatabaseReference 是一行引用关系（PRIMARY KEY (db_id, app_id, service)）。
type DatabaseReference struct {
	// DatabaseID/AppID/Service 是三元主键（库实例、引用方 app、引用方
	// compose 服务名）。
	DatabaseID string
	AppID      string
	Service    string
	// EnvPrefix 是该引用物化 env 的前缀（实例名 '-'→'_' 大写形——同 app
	// 引用撞前缀的冲突哨兵输入，E_DB_ENV_PREFIX_CONFLICT 的判定面）。
	EnvPrefix string
	CreatedAt time.Time
}

// UpsertDatabaseReference 登记一行引用（同三元主键幂等覆盖 env_prefix；
// created_at 首次落账后保持）。planner 同事务维护入口之一（Tx 形态）。
func (t *Tx) UpsertDatabaseReference(ctx context.Context, ref DatabaseReference) error {
	if ref.DatabaseID == "" || ref.AppID == "" || ref.Service == "" || ref.EnvPrefix == "" {
		return errors.New("state: upsert database reference: db_id, app_id, service and env_prefix are required")
	}
	const q = `INSERT INTO db_references (db_id, app_id, service, env_prefix, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(db_id, app_id, service) DO UPDATE SET env_prefix = excluded.env_prefix`
	if _, err := t.ExecContext(ctx, q,
		ref.DatabaseID, ref.AppID, ref.Service, ref.EnvPrefix, nowNano()); err != nil {
		return fmt.Errorf("state: upsert database reference %s/%s/%s: %w",
			ref.DatabaseID, ref.AppID, ref.Service, err)
	}
	return nil
}

// UpsertDatabaseReference 是 Store 形态的单行登记（独立事务薄壳）。
func (s *Store) UpsertDatabaseReference(ctx context.Context, ref DatabaseReference) error {
	return s.InTx(ctx, func(tx *Tx) error {
		return tx.UpsertDatabaseReference(ctx, ref)
	})
}

// DeleteDatabaseReferencesForApp 清空该 app 的全部引用行（planner 每次
// 部署 releasing 前的「全清 + 替换」第一拍——与同事务的重新 upsert 组合
// 即当前声明的镜像；引用 app 删除 = 行级联清理的入口）。幂等：无行时
// 零删除不报错。
func (t *Tx) DeleteDatabaseReferencesForApp(ctx context.Context, appID string) (int64, error) {
	res, err := t.ExecContext(ctx, `DELETE FROM db_references WHERE app_id = ?`, appID)
	if err != nil {
		return 0, fmt.Errorf("state: delete database references for app %s: %w", appID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("state: read database reference delete count: %w", err)
	}
	return n, nil
}

// DeleteDatabaseReferencesForApp 是 Store 形态的全清（独立事务薄壳）。
func (s *Store) DeleteDatabaseReferencesForApp(ctx context.Context, appID string) (int64, error) {
	var n int64
	err := s.InTx(ctx, func(tx *Tx) error {
		var err error
		n, err = tx.DeleteDatabaseReferencesForApp(ctx, appID)
		return err
	})
	if err != nil {
		return 0, err
	}
	return n, nil
}

// ListDatabaseReferencesByDB 按库实例反查全部引用（库删除守卫的数据源：
// 非空 → 引用清单随 E_DB_REFERENCED 披露；reap 前置 = 引用清零）。按
// app_id、service 字典序稳定排序。
func (s *Store) ListDatabaseReferencesByDB(ctx context.Context, dbID string) ([]DatabaseReference, error) {
	const q = `SELECT db_id, app_id, service, env_prefix, created_at
		FROM db_references WHERE db_id = ? ORDER BY app_id ASC, service ASC`
	return queryDatabaseReferences(ctx, s.db, q, dbID)
}

// ListDatabaseReferencesByApp 按引用方 app 正查全部引用（部署重建前的
// 当前声明快照对账面）。按 db_id、service 字典序稳定排序。
func (s *Store) ListDatabaseReferencesByApp(ctx context.Context, appID string) ([]DatabaseReference, error) {
	const q = `SELECT db_id, app_id, service, env_prefix, created_at
		FROM db_references WHERE app_id = ? ORDER BY db_id ASC, service ASC`
	return queryDatabaseReferences(ctx, s.db, q, appID)
}

// queryDatabaseReferences 执行多行引用查询。
func queryDatabaseReferences(ctx context.Context, db *sql.DB, query string, args ...any) ([]DatabaseReference, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("state: query database references: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []DatabaseReference
	for rows.Next() {
		var r DatabaseReference
		var created int64
		if err := rows.Scan(&r.DatabaseID, &r.AppID, &r.Service, &r.EnvPrefix, &created); err != nil {
			return nil, fmt.Errorf("state: scan database reference: %w", err)
		}
		r.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate database references: %w", err)
	}
	return out, nil
}
