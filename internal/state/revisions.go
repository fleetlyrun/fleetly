package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// revisions 表写入与读取通道（release-semantics §2.4 版本快照）：每 app
// 每次成功部署一条（verified=1）。seq 为 app 内自增（事务内 max+1 分配）；
// compose_normalized 为归一化 compose 快照（env 按 key:sha256+来源，值不落
// 明文）；overlay 为平台覆盖层 JSON（镜像 digest、secret 引用等）。
//
// 保留窗（T2.12，§2.4「固定保留最近 5 次成功部署、列表即选项」）：00005
// 加法列 status 承载淘汰位——CreateRevision 固化第 6 个成功快照时把最旧
// 的标记 superseded（不物理删，审计与历史可读）；active 集即回滚选项集，
// ListRevisions / GetAppRevision 只暴露 active。重放的执行形态（含合并
// env 明文的 desired_spec 密文）仍由创建该 revision 的 succeeded deployment
// 行承载——回滚经 revision_id 反查部署行取快照（D-REL-7 部署记录读取纪律）。
//
// 注意：00001 词表不可加 CHECK——status 词表（active/superseded）由本包
// 常量承载，非法值经写入通道防御。

// Revision 是一次版本快照行。
type Revision struct {
	ID                string
	AppID             string
	Seq               int64
	ComposeNormalized string
	Overlay           string
	DesiredHash       string
	Verified          bool
	// Status 是保留窗状态位（active = 可回滚选项；superseded = 被保留窗
	// 淘汰，仅存档——列表即选项，列不出来的不可回滚）。
	Status    string
	CreatedAt time.Time
}

// 保留窗状态位词表（revisions.status，00005 加法列；无 CHECK，词表在此
// 冻结）。v0.1 只有两个值；候选态（candidate）不进 v0.1——快照仅在成功
// 终态固化（verified=1 与 active 同拍写入）。
const (
	// RevisionStatusActive 在保留窗内（回滚选项集）。
	RevisionStatusActive = "active"
	// RevisionStatusSuperseded 被更新快照挤出保留窗（存档不删）。
	RevisionStatusSuperseded = "superseded"
)

// RevisionKeepVersions 是保留窗宽度（release-semantics §2.4 / architecture
// §2.5 默认参数表：版本保留 5）。
const RevisionKeepVersions = 5

// ErrRevisionNotFound 表示版本快照不存在（含 superseded 后不可经选项面
// 解析的形态——调用方按 E_ROLLBACK_NO_TARGET 映射）。
var ErrRevisionNotFound = errors.New("revision not found")

// RevisionWrite 是一次版本快照写入。
type RevisionWrite struct {
	// ID 留空自动生成 ULID。
	ID                string
	AppID             string
	ComposeNormalized string
	Overlay           string
	DesiredHash       string
}

// CreateRevision 在事务内创建 verified=1 的版本快照（seq = app 内 max+1）。
// 与部署终态写、审计同事务组合（fail-closed）。
func (t *Tx) CreateRevision(ctx context.Context, w RevisionWrite) (Revision, error) {
	if w.AppID == "" {
		return Revision{}, errors.New("state: create revision: app_id is empty")
	}
	id := w.ID
	if id == "" {
		id = ulid.Make().String()
	}
	var maxSeq sql.NullInt64
	if err := t.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM revisions WHERE app_id = ?`, w.AppID).Scan(&maxSeq); err != nil {
		return Revision{}, fmt.Errorf("state: read max revision seq: %w", err)
	}
	seq := maxSeq.Int64 + 1
	now := nowNano()
	const q = `INSERT INTO revisions
		(id, app_id, seq, compose_normalized, overlay, desired_hash, verified, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?)`
	if _, err := t.ExecContext(ctx, q, id, w.AppID, seq, w.ComposeNormalized, w.Overlay, w.DesiredHash, now); err != nil {
		return Revision{}, fmt.Errorf("state: insert revision: %w", err)
	}
	if err := t.trimRevisions(ctx, w.AppID); err != nil {
		return Revision{}, err
	}
	return Revision{
		ID:                id,
		AppID:             w.AppID,
		Seq:               seq,
		ComposeNormalized: w.ComposeNormalized,
		Overlay:           w.Overlay,
		DesiredHash:       w.DesiredHash,
		Verified:          true,
		Status:            RevisionStatusActive,
		CreatedAt:         time.Unix(0, now).UTC(),
	}, nil
}

// ReusableRevisionByDesiredHash 在事务内裁决成功固化的幂等复用（S18-A6）：
// 按 (app_id, desired_hash) 查既有 active 快照，**且该行同时是该 app 最新
// active 快照**时返回复用（seq 不递增、不重复插入）；否则返回未命中走插入。
//
// 「最新 active」判据兼顾两侧契约：
//   - 崩溃重放幂等：两事务间崩溃留下的残行必然是最新 active（同 app 互斥
//     ——残行所属部署未终态，同 app 无更晚成功部署），命中复用，重跑窗口
//     再固化不产生重复行；
//   - 回滚/旧态重部署语义：目标 hash 的既有行不是最新（其后已有更新版本）
//     时不复用——按新 revision 固化，回退版本在「列表即选项」里前移到最新
//     位（release-semantics §2.4「回滚 = 一次成功部署」）。
//
// superseded 行（保留窗外存档）恒不复用；空哈希（旧形态行）无可比性，
// 恒走插入。
func (t *Tx) ReusableRevisionByDesiredHash(ctx context.Context, appID, desiredHash string) (Revision, bool, error) {
	if desiredHash == "" {
		return Revision{}, false, nil // 旧形态行无哈希：无幂等键可比，走插入
	}
	const q = `SELECT id, app_id, seq, compose_normalized, overlay, desired_hash, verified, status, created_at
		FROM revisions
		WHERE app_id = ? AND desired_hash = ? AND status = 'active' AND verified = 1
		ORDER BY seq DESC LIMIT 1`
	var r Revision
	var verified, created int64
	err := t.QueryRowContext(ctx, q, appID, desiredHash).Scan(
		&r.ID, &r.AppID, &r.Seq, &r.ComposeNormalized, &r.Overlay,
		&r.DesiredHash, &verified, &r.Status, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Revision{}, false, nil
	}
	if err != nil {
		return Revision{}, false, fmt.Errorf("state: query revision by desired_hash: %w", err)
	}
	r.Verified = verified != 0
	r.CreatedAt = time.Unix(0, created).UTC()

	// 最新 active 判据：该 app active 集 max(seq) 与命中行一致。
	var maxSeq sql.NullInt64
	if err := t.QueryRowContext(ctx,
		`SELECT MAX(seq) FROM revisions WHERE app_id = ? AND status = 'active'`,
		appID).Scan(&maxSeq); err != nil {
		return Revision{}, false, fmt.Errorf("state: read max revision seq: %w", err)
	}
	if !maxSeq.Valid || maxSeq.Int64 != r.Seq {
		return Revision{}, false, nil // 既有行不是最新：回滚/旧态重部署，按新行固化
	}
	return r, true, nil
}

// trimRevisions 执行保留窗裁剪（§2.4：第 6 个成功固化时淘汰最旧——标记
// superseded，不物理删）。active 集按 seq 降序保留最新 RevisionKeepVersions
// 条，其余翻 superseded。幂等；与快照写入同事务（fail-closed）。
func (t *Tx) trimRevisions(ctx context.Context, appID string) error {
	const q = `UPDATE revisions SET status = 'superseded'
		WHERE app_id = ? AND status = 'active' AND id NOT IN (
			SELECT id FROM revisions
			WHERE app_id = ? AND status = 'active'
			ORDER BY seq DESC LIMIT ?
		)`
	if _, err := t.ExecContext(ctx, q, appID, appID, RevisionKeepVersions); err != nil {
		return fmt.Errorf("state: trim revisions window: %w", err)
	}
	return nil
}

// ListRevisions 返回该 app 保留窗内的版本快照（active，seq 降序 = 最新
// 在前，release-semantics §2.4「列表即选项」——返回集即可回滚目标全集）。
func (s *Store) ListRevisions(ctx context.Context, appID string) ([]Revision, error) {
	const q = `SELECT id, app_id, seq, compose_normalized, overlay, desired_hash, verified, status, created_at
		FROM revisions WHERE app_id = ? AND status = 'active' AND verified = 1
		ORDER BY seq DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, q, appID)
	if err != nil {
		return nil, fmt.Errorf("state: query revisions: %w", err)
	}
	return scanRevisions(ctx, rows)
}

// GetAppRevision 解析该 app 的一个可回滚目标（active + app 归属双谓词）；
// 不存在 / 已被保留窗淘汰 / 归属不符 → ErrRevisionNotFound（列表即选项：
// 列不出来的不可回滚）。
func (s *Store) GetAppRevision(ctx context.Context, appID, revisionID string) (Revision, error) {
	const q = `SELECT id, app_id, seq, compose_normalized, overlay, desired_hash, verified, status, created_at
		FROM revisions WHERE id = ? AND app_id = ? AND status = 'active' AND verified = 1`
	rows, err := s.db.QueryContext(ctx, q, revisionID, appID)
	if err != nil {
		return Revision{}, fmt.Errorf("state: query revisions: %w", err)
	}
	out, err := scanRevisions(ctx, rows)
	if err != nil {
		return Revision{}, err
	}
	if len(out) == 0 {
		return Revision{}, ErrRevisionNotFound
	}
	return out[0], nil
}

// scanRevisions 迭代版本快照查询结果。
func scanRevisions(ctx context.Context, rows *sql.Rows) ([]Revision, error) {
	defer func() { _ = rows.Close() }()
	var out []Revision
	for rows.Next() {
		var r Revision
		var verified int64
		var created int64
		if err := rows.Scan(&r.ID, &r.AppID, &r.Seq, &r.ComposeNormalized, &r.Overlay,
			&r.DesiredHash, &verified, &r.Status, &created); err != nil {
			return nil, fmt.Errorf("state: scan revision: %w", err)
		}
		r.Verified = verified != 0
		r.CreatedAt = time.Unix(0, created).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate revisions: %w", err)
	}
	return out, nil
}

// GetRevision 按 ID 取版本快照（不限保留窗状态——存档行仍可读）；不存在
// 返回 ErrRevisionNotFound。回滚目标解析须走 GetAppRevision（active 谓词）。
func (s *Store) GetRevision(ctx context.Context, id string) (Revision, error) {
	const q = `SELECT id, app_id, seq, compose_normalized, overlay, desired_hash, verified, status, created_at
		FROM revisions WHERE id = ?`
	rows, err := s.db.QueryContext(ctx, q, id)
	if err != nil {
		return Revision{}, fmt.Errorf("state: query revisions: %w", err)
	}
	out, err := scanRevisions(ctx, rows)
	if err != nil {
		return Revision{}, err
	}
	if len(out) == 0 {
		return Revision{}, ErrRevisionNotFound
	}
	return out[0], nil
}
