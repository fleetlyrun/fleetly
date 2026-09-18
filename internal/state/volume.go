package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// volumes 表读写（stateful-placement §2.4 卷注册表）：卷数据生命周期归
// 平台账——数据诞生点钉住所在节点（平台节点 ID 为锚）、命名约定名
// `fleetly-<app>-<key>-<appid8>` 防代际静默复用（无 label 用命名约定，
// state-model §2.4）。status: active（在册）/ orphaned（应用删除默认保留、
// 可发现）/ discarded（显式丢弃）。删除应用默认保留卷、删除仅显式
// --delete-volumes（v0.2）或孤儿清理——平台永不自动删卷数据。

// VolumeStatus 是卷注册表状态位。
type VolumeStatus string

const (
	// VolumeActive 在册（被应用声明或已钉节点）。
	VolumeActive VolumeStatus = "active"
	// VolumeOrphaned 应用已删除/解绑，卷保留可发现。
	VolumeOrphaned VolumeStatus = "orphaned"
	// VolumeDiscarded 显式丢弃（admin+confirm+审计）。
	VolumeDiscarded VolumeStatus = "discarded"
)

// VolumeKind 是卷类别词表。
type VolumeKind string

const (
	// VolumeKindNamed 命名卷（v0.1 受控子集唯一支持的挂载形态）。
	VolumeKindNamed VolumeKind = "named"
	// VolumeKindBind 宿主绑定挂载（v0.1 校验拒绝；词表先行对齐设计表）。
	VolumeKindBind VolumeKind = "bind"
)

// Volume 是卷注册表行投影。Name 即设计表的 docker_name（命名约定值）。
type Volume struct {
	ID             string
	AppID          string
	Key            string
	Name           string
	Kind           VolumeKind
	PlatformNodeID string
	MountPath      string
	HostPath       string
	Status         VolumeStatus
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ErrVolumeNotFound 表示注册表中无该卷。
var ErrVolumeNotFound = errors.New("volume not found")

// VolumeWrite 是一次卷登记写入（按 app_id+key 唯一 upsert）。Name 必须
// 由调用方按命名约定生成（internal/naming.VolumeName）——本层不解释命名。
type VolumeWrite struct {
	AppID          string
	Key            string
	Name           string
	Kind           VolumeKind
	PlatformNodeID string
	MountPath      string
	HostPath       string
}

// RegisterAppVolume 登记卷（重新声明转 active——孤儿卷被应用重新声明即
// 复活；数据不随声明删除，name/node 等事实字段以最新声明为准）。created_at
// 首次落账后保持（防代际静默复用的叙事锚）。返回 isNew：首次登记为 true
// （调用方据此发 volume.created 事件——数据诞生点）。
func (s *Store) RegisterAppVolume(ctx context.Context, w VolumeWrite) (Volume, bool, error) {
	if w.AppID == "" || w.Key == "" || w.Name == "" {
		return Volume{}, false, errors.New("state: volume write requires app_id, key and name")
	}
	if w.Kind == "" {
		w.Kind = VolumeKindNamed
	}
	if err := w.Validate(); err != nil {
		return Volume{}, false, err
	}
	var out Volume
	var isNew bool
	err := s.InTx(ctx, func(tx *Tx) error {
		_, err := tx.GetAppVolume(ctx, w.AppID, w.Key)
		switch {
		case errors.Is(err, ErrVolumeNotFound):
			isNew = true
		case err != nil:
			return err
		}
		row, err := tx.RegisterAppVolume(ctx, w)
		if err != nil {
			return err
		}
		out = row
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:  "system",
			Action: "volume.registered",
			Target: "volume:" + w.Name,
			Result: "ok",
			// 摘要只含事实字段，无卷数据概念。
			DiffSummary: `{"app":"` + w.AppID + `","key":"` + w.Key + `"}`,
		})
	})
	if err != nil {
		return Volume{}, false, fmt.Errorf("state: register volume %s: %w", w.Key, err)
	}
	return out, isNew, nil
}

// Validate 校验 VolumeWrite 词表（kind 必须在册）。
func (w VolumeWrite) Validate() error {
	switch w.Kind {
	case VolumeKindNamed, VolumeKindBind:
		return nil
	default:
		return fmt.Errorf("state: volume kind %q not in {named, bind}", w.Kind)
	}
}

// RegisterAppVolume 是事务内卷登记（供与事件等同事务组合）。
func (t *Tx) RegisterAppVolume(ctx context.Context, w VolumeWrite) (Volume, error) {
	now := nowNano()
	const q = `INSERT INTO volumes
		(id, app_id, key, name, kind, platform_node_id, mount_path, host_path, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT(app_id, key) DO UPDATE SET
			name = excluded.name,
			kind = excluded.kind,
			platform_node_id = excluded.platform_node_id,
			mount_path = excluded.mount_path,
			host_path = excluded.host_path,
			status = 'active',
			updated_at = excluded.updated_at`
	if _, err := t.ExecContext(ctx, q, ulid.Make().String(), w.AppID, w.Key, w.Name,
		string(w.Kind), w.PlatformNodeID, w.MountPath, w.HostPath, now, now); err != nil {
		return Volume{}, fmt.Errorf("state: upsert volume: %w", err)
	}
	return t.GetAppVolume(ctx, w.AppID, w.Key)
}

// GetAppVolume 取单卷；不存在返回 ErrVolumeNotFound。
func (s *Store) GetAppVolume(ctx context.Context, appID, key string) (Volume, error) {
	const q = `SELECT ` + volumeScanCols + ` FROM volumes WHERE app_id = ? AND key = ?`
	return scanVolume(s.db.QueryRowContext(ctx, q, appID, key))
}

// GetAppVolume 是事务内取单卷（写后回读）。
func (t *Tx) GetAppVolume(ctx context.Context, appID, key string) (Volume, error) {
	const q = `SELECT ` + volumeScanCols + ` FROM volumes WHERE app_id = ? AND key = ?`
	return scanVolume(t.QueryRowContext(ctx, q, appID, key))
}

// ListAppVolumes 返回该 app 全部卷（按 key 字典序）。
func (s *Store) ListAppVolumes(ctx context.Context, appID string) ([]Volume, error) {
	const q = `SELECT ` + volumeScanCols + ` FROM volumes WHERE app_id = ? ORDER BY key ASC`
	return queryVolumes(ctx, s.db, q, appID)
}

// MarkAppVolumesOrphaned 把该 app 全部 active 卷置 orphaned（应用删除默认
// 保留卷语义；调用方在 tombstone 流程触发）。返回置孤儿卷数。幂等。
func (s *Store) MarkAppVolumesOrphaned(ctx context.Context, appID string) (int, error) {
	var n int64
	err := s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE volumes SET status = 'orphaned', updated_at = ?
			WHERE app_id = ? AND status = 'active'`, nowNano(), appID)
		if err != nil {
			return fmt.Errorf("state: orphan volumes: %w", err)
		}
		n, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("state: read orphan count: %w", err)
		}
		if n > 0 {
			return tx.WriteAudit(ctx, AuditEntry{
				Actor:       "system",
				Action:      "volume.orphaned",
				Target:      "app:" + appID,
				Result:      "ok",
				DiffSummary: fmt.Sprintf(`{"count":%d}`, n),
			})
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("state: mark volumes orphaned: %w", err)
	}
	return int(n), nil
}

// volumeScanCols 是卷行查询列清单（新增列只加在此与扫描函数）。
const volumeScanCols = `id, app_id, key, name, kind, platform_node_id, mount_path,
	host_path, status, created_at, updated_at`

// scanVolume 从单行构造 Volume。
func scanVolume(row interface{ Scan(dest ...any) error }) (Volume, error) {
	var v Volume
	var kind, status string
	var nodeID sql.NullString
	var created, updated int64
	if err := row.Scan(&v.ID, &v.AppID, &v.Key, &v.Name, &kind, &nodeID,
		&v.MountPath, &v.HostPath, &status, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Volume{}, ErrVolumeNotFound
		}
		return Volume{}, fmt.Errorf("state: scan volume: %w", err)
	}
	v.Kind = VolumeKind(kind)
	v.Status = VolumeStatus(status)
	if nodeID.Valid {
		v.PlatformNodeID = nodeID.String
	}
	v.CreatedAt = time.Unix(0, created).UTC()
	v.UpdatedAt = time.Unix(0, updated).UTC()
	return v, nil
}

// queryVolumes 执行多行卷查询。
func queryVolumes(ctx context.Context, db *sql.DB, query string, args ...any) ([]Volume, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("state: query volumes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Volume
	for rows.Next() {
		v, err := scanVolume(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate volumes: %w", err)
	}
	return out, nil
}
