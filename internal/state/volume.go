package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// volumes 表读写（stateful-placement §2.4 卷注册表 + managed-databases
// 设计 §5.4 迁移①归属泛化）：卷数据生命周期归平台账——数据诞生点钉住
// 所在节点（平台节点 ID 为锚）、命名约定名 `fleetly-<app>-<key>-<appid8>`
// 防代际静默复用（无 label 用命名约定，state-model §2.4）。status: active
// （在册）/ orphaned（应用删除默认保留、可发现）/ discarded（显式丢弃）。
// 删除应用默认保留卷、删除仅显式 --delete-volumes（v0.2）或孤儿清理——
// 平台永不自动删卷数据。
//
// 归属泛化（E4，D-DB-1 库实例独立资源）：owner_kind ∈ {app, database} +
// owner_id 取代 app_id 成为核心键（UNIQUE(owner_kind, owner_id, key)）。
// 核心 API 按 owner 二元组寻址（RegisterVolume/GetVolume/…）；app 形态的
// 既有调用面（internal/placement、internal/engine、internal/api）经同名
// App 包装函数零改动复用——包装即固定 owner_kind='app'，无行为变化。

// VolumeOwnerKind 是卷归属词表（managed-databases §5.4 迁移①）。
type VolumeOwnerKind string

const (
	// VolumeOwnerApp 应用卷（既有形态；迁移前的存量行全部属于本类）。
	VolumeOwnerApp VolumeOwnerKind = "app"
	// VolumeOwnerDatabase 库实例数据卷（E4：数据诞生点钉住语义同款复用）。
	VolumeOwnerDatabase VolumeOwnerKind = "database"
)

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
	ID string
	// OwnerKind/OwnerID 是卷归属二元组（E4 泛化：app 或 database 资源的
	// 平台 ID——与 v0.1 的 app_id 列一一对应的泛化形态）。
	OwnerKind      VolumeOwnerKind
	OwnerID        string
	Key            string
	Name           string
	Kind           VolumeKind
	PlatformNodeID string
	// PrevPlatformNodeID 是上一次显式换点（rebind）前的数据节点（multi-node
	// §2.8 迁移 00010）：非空 = 源节点存在残留卷副本，清单面派生 residual
	// 清理指引；空 = 卷从未跨节点迁移（现状语义）。数据本体不随换点迁移
	// ——平台不编排远端数据移动（D-MN-10）。
	PrevPlatformNodeID string
	MountPath          string
	HostPath           string
	Status             VolumeStatus
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ErrVolumeNotFound 表示注册表中无该卷。
var ErrVolumeNotFound = errors.New("volume not found")

// VolumeWrite 是一次卷登记写入（按 owner 二元组 + key 唯一 upsert）。Name
// 必须由调用方按命名约定生成（internal/naming 的 VolumeName/DBVolumeName）
// ——本层不解释命名。
//
// 归属填写（E4 兼容口径）：新调用面显式填 OwnerKind+OwnerID；既有 app 面
// 可继续只填 AppID（OwnerKind 空时回落 VolumeOwnerApp——迁移①的 SQL
// DEFAULT 'app' 在 Go 侧的同款回落，行为零变化）。
type VolumeWrite struct {
	// AppID 是 app 归属的兼容别名（等价 OwnerKind='app' + OwnerID=AppID）；
	// 与 OwnerID 同时给出时以 OwnerID 为准。
	AppID string
	// OwnerKind 归属类别；空值回落 VolumeOwnerApp（见结构注）。
	OwnerKind VolumeOwnerKind
	// OwnerID 是归属资源的平台 ID（app ID 或库实例 ID）。
	OwnerID        string
	Key            string
	Name           string
	Kind           VolumeKind
	PlatformNodeID string
	MountPath      string
	HostPath       string
}

// resolveOwner 归一化归属二元组（AppID 兼容回落 + 词表校验）。就地修正
// w.OwnerKind/OwnerID。
func (w *VolumeWrite) resolveOwner() error {
	if w.OwnerKind == "" {
		w.OwnerKind = VolumeOwnerApp
	}
	if w.OwnerID == "" {
		w.OwnerID = w.AppID
	}
	switch w.OwnerKind {
	case VolumeOwnerApp, VolumeOwnerDatabase:
	default:
		return fmt.Errorf("state: volume owner_kind %q not in {app, database}", w.OwnerKind)
	}
	if w.OwnerID == "" {
		return errors.New("state: volume write requires owner (owner_id or app_id), key and name")
	}
	return nil
}

// RegisterVolume 登记卷（核心形态，按 owner 二元组寻址；重新声明转
// active——孤儿卷被重新声明即复活；数据不随声明删除，name/node 等事实
// 字段以最新声明为准）。created_at 首次落账后保持（防代际静默复用的叙事
// 锚）。返回 isNew：首次登记为 true（调用方据此发 volume.created 事件
// ——数据诞生点）。
func (s *Store) RegisterVolume(ctx context.Context, w VolumeWrite) (Volume, bool, error) {
	if err := w.resolveOwner(); err != nil {
		return Volume{}, false, err
	}
	if w.Key == "" || w.Name == "" {
		return Volume{}, false, errors.New("state: volume write requires owner, key and name")
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
		_, err := tx.GetVolume(ctx, w.OwnerKind, w.OwnerID, w.Key)
		switch {
		case errors.Is(err, ErrVolumeNotFound):
			isNew = true
		case err != nil:
			return err
		}
		row, err := tx.RegisterVolume(ctx, w)
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
			DiffSummary: DiffSummary("owner_kind", string(w.OwnerKind), "owner_id", w.OwnerID, "key", w.Key), // MG-6：构造器替换手拼 JSON
		})
	})
	if err != nil {
		return Volume{}, false, fmt.Errorf("state: register volume %s: %w", w.Key, err)
	}
	return out, isNew, nil
}

// RegisterAppVolume 登记应用卷（E4 泛化前的既有形态：固定 owner_kind=
// 'app' 的 RegisterVolume 包装——placement/engine 调用面零改动）。
func (s *Store) RegisterAppVolume(ctx context.Context, w VolumeWrite) (Volume, bool, error) {
	return s.RegisterVolume(ctx, w)
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

// RegisterVolume 是事务内卷登记（供与事件等同事务组合）。
func (t *Tx) RegisterVolume(ctx context.Context, w VolumeWrite) (Volume, error) {
	if err := w.resolveOwner(); err != nil {
		return Volume{}, err
	}
	now := nowNano()
	const q = `INSERT INTO volumes
		(id, owner_kind, owner_id, key, name, kind, platform_node_id, mount_path, host_path, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT(owner_kind, owner_id, key) DO UPDATE SET
			name = excluded.name,
			kind = excluded.kind,
			platform_node_id = excluded.platform_node_id,
			mount_path = excluded.mount_path,
			host_path = excluded.host_path,
			status = 'active',
			updated_at = excluded.updated_at`
	if _, err := t.ExecContext(ctx, q, ulid.Make().String(), string(w.OwnerKind), w.OwnerID, w.Key, w.Name,
		string(w.Kind), w.PlatformNodeID, w.MountPath, w.HostPath, now, now); err != nil {
		return Volume{}, fmt.Errorf("state: upsert volume: %w", err)
	}
	return t.GetVolume(ctx, w.OwnerKind, w.OwnerID, w.Key)
}

// RegisterAppVolume 是事务内的应用卷登记（RegisterVolume 的 app 形态包装）。
func (t *Tx) RegisterAppVolume(ctx context.Context, w VolumeWrite) (Volume, error) {
	return t.RegisterVolume(ctx, w)
}

// GetVolume 取单卷（owner 二元组寻址）；不存在返回 ErrVolumeNotFound。
func (s *Store) GetVolume(ctx context.Context, ownerKind VolumeOwnerKind, ownerID, key string) (Volume, error) {
	const q = `SELECT ` + volumeScanCols + ` FROM volumes WHERE owner_kind = ? AND owner_id = ? AND key = ?`
	return scanVolume(s.db.QueryRowContext(ctx, q, string(ownerKind), ownerID, key))
}

// GetVolume 是事务内取单卷（写后回读）。
func (t *Tx) GetVolume(ctx context.Context, ownerKind VolumeOwnerKind, ownerID, key string) (Volume, error) {
	const q = `SELECT ` + volumeScanCols + ` FROM volumes WHERE owner_kind = ? AND owner_id = ? AND key = ?`
	return scanVolume(t.QueryRowContext(ctx, q, string(ownerKind), ownerID, key))
}

// GetAppVolume 取应用单卷（GetVolume 的 app 形态包装；不存在返回
// ErrVolumeNotFound）。
func (s *Store) GetAppVolume(ctx context.Context, appID, key string) (Volume, error) {
	return s.GetVolume(ctx, VolumeOwnerApp, appID, key)
}

// GetAppVolume 是事务内取应用单卷（GetVolume 的 app 形态包装）。
func (t *Tx) GetAppVolume(ctx context.Context, appID, key string) (Volume, error) {
	return t.GetVolume(ctx, VolumeOwnerApp, appID, key)
}

// ListOwnerVolumes 返回该归属（app 或库实例）的全部卷（按 key 字典序）。
func (s *Store) ListOwnerVolumes(ctx context.Context, ownerKind VolumeOwnerKind, ownerID string) ([]Volume, error) {
	const q = `SELECT ` + volumeScanCols + ` FROM volumes WHERE owner_kind = ? AND owner_id = ? ORDER BY key ASC`
	return queryVolumes(ctx, s.db, q, string(ownerKind), ownerID)
}

// ListAppVolumes 返回该 app 全部卷（ListOwnerVolumes 的 app 形态包装）。
func (s *Store) ListAppVolumes(ctx context.Context, appID string) ([]Volume, error) {
	return s.ListOwnerVolumes(ctx, VolumeOwnerApp, appID)
}

// ListAllVolumes 返回跨归属全部卷（multi-node §2.8 ListVolumes 只读面的
// 数据源：active 在册行、orphaned 孤儿行——删除应用默认保留、residual
// 派生标记由消费方按 prev_platform_node_id 计算； discarded 行同样如实
// 列出。E4 起含 database 归属行——OwnerKind/OwnerID 随行投影，消费方按
// 归属分面）。按 name 升序稳定排序。
func (s *Store) ListAllVolumes(ctx context.Context) ([]Volume, error) {
	const q = `SELECT ` + volumeScanCols + ` FROM volumes ORDER BY name ASC`
	return queryVolumes(ctx, s.db, q)
}

// MarkOwnerVolumesOrphaned 把该归属（app 或库实例）全部 active 卷置
// orphaned（应用删除默认保留卷语义的泛化形态；调用方在 tombstone 流程
// 触发）。返回置孤儿卷数。幂等。
func (s *Store) MarkOwnerVolumesOrphaned(ctx context.Context, ownerKind VolumeOwnerKind, ownerID string) (int, error) {
	var n int64
	err := s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE volumes SET status = 'orphaned', updated_at = ?
			WHERE owner_kind = ? AND owner_id = ? AND status = 'active'`, nowNano(), string(ownerKind), ownerID)
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
				Target:      string(ownerKind) + ":" + ownerID,
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

// MarkAppVolumesOrphaned 把该 app 全部 active 卷置 orphaned（应用删除默认
// 保留卷语义；MarkOwnerVolumesOrphaned 的 app 形态包装）。
func (s *Store) MarkAppVolumesOrphaned(ctx context.Context, appID string) (int, error) {
	return s.MarkOwnerVolumesOrphaned(ctx, VolumeOwnerApp, appID)
}

// MarkOwnerVolumesDiscarded 是事务内卷显式丢弃（owner 二元组寻址）：该归属
// 全部非 discarded 卷置 discarded（E4 库实例 delete_volumes=true 的 reap
// 路径——底座卷由调用方先行移除，台账随后如实落账；幂等）。返回置
// discarded 卷数。调用方负责随写审计（显式丢弃 = 数据安全动作）。
func (t *Tx) MarkOwnerVolumesDiscarded(ctx context.Context, ownerKind VolumeOwnerKind, ownerID string) (int64, error) {
	switch ownerKind {
	case VolumeOwnerApp, VolumeOwnerDatabase:
	default:
		return 0, fmt.Errorf("state: volume owner_kind %q not in {app, database}", ownerKind)
	}
	if ownerID == "" {
		return 0, errors.New("state: discard volumes requires owner_id")
	}
	res, err := t.ExecContext(ctx,
		`UPDATE volumes SET status = 'discarded', updated_at = ?
		WHERE owner_kind = ? AND owner_id = ? AND status <> 'discarded'`, nowNano(), string(ownerKind), ownerID)
	if err != nil {
		return 0, fmt.Errorf("state: discard volumes: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("state: read discard count: %w", err)
	}
	return n, nil
}

// RebindVolumes 是事务内卷换绑（泛化形态）：该归属的全部 active 卷
// platform_node_id → 目标节点，原节点登记进 prev_platform_node_id
//（multi-node §2.6 换点落库面；restored/discarded 两种数据处置都登记
// prev——源节点副本都是待清理残留）。幂等：已在目标节点的卷行不动（prev
// 不被同节点重绑洗写）。返回发生迁移的卷数。
func (t *Tx) RebindVolumes(ctx context.Context, ownerKind VolumeOwnerKind, ownerID, targetPlatformNodeID string) (int64, error) {
	res, err := t.ExecContext(ctx,
		`UPDATE volumes SET
			prev_platform_node_id = platform_node_id,
			platform_node_id = ?,
			updated_at = ?
		WHERE owner_kind = ? AND owner_id = ? AND status = 'active'
		  AND platform_node_id <> ? AND platform_node_id <> ''`,
		targetPlatformNodeID, nowNano(), string(ownerKind), ownerID, targetPlatformNodeID)
	if err != nil {
		return 0, fmt.Errorf("state: rebind volumes: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("state: read rebind count: %w", err)
	}
	return n, nil
}

// RebindAppVolumes 是事务内应用卷换绑（RebindVolumes 的 app 形态包装）。
func (t *Tx) RebindAppVolumes(ctx context.Context, appID, targetPlatformNodeID string) (int64, error) {
	return t.RebindVolumes(ctx, VolumeOwnerApp, appID, targetPlatformNodeID)
}

// volumeScanCols 是卷行查询列清单（新增列只加在此与扫描函数）。
const volumeScanCols = `id, owner_kind, owner_id, key, name, kind, platform_node_id,
	prev_platform_node_id, mount_path, host_path, status, created_at, updated_at`

// scanVolume 从单行构造 Volume。
func scanVolume(row interface{ Scan(dest ...any) error }) (Volume, error) {
	var v Volume
	var kind, status, ownerKind string
	var nodeID, prevNodeID sql.NullString
	var created, updated int64
	if err := row.Scan(&v.ID, &ownerKind, &v.OwnerID, &v.Key, &v.Name, &kind, &nodeID,
		&prevNodeID, &v.MountPath, &v.HostPath, &status, &created, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Volume{}, ErrVolumeNotFound
		}
		return Volume{}, fmt.Errorf("state: scan volume: %w", err)
	}
	v.OwnerKind = VolumeOwnerKind(ownerKind)
	v.Kind = VolumeKind(kind)
	v.Status = VolumeStatus(status)
	if nodeID.Valid {
		v.PlatformNodeID = nodeID.String
	}
	if prevNodeID.Valid {
		v.PrevPlatformNodeID = prevNodeID.String
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
