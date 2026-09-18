package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"
)

// revisions 表写入通道（release-semantics §2.4 版本快照）：每 app 每次
// 成功部署一条（verified=1）。seq 为 app 内自增（事务内 max+1 分配）；
// compose_normalized 为归一化 compose 快照（env 按 key:sha256+来源，值不落
// 明文）；overlay 为平台覆盖层 JSON（镜像 digest、secret 引用等）。读取面
// （回滚目标 = 最近成功部署的 revision）由 deployments 历史承载，本层只
// 提供写入与按 ID 读。
//
// 注意：00001 词表的 verified INTEGER 不可加 CHECK——「active/superseded」
// 状态位未建列，最近有效版本 = 最近 succeeded deployment 的 revision_id
// （发布专项 D-REL-7：版本历史经部署记录读取，不设冗余状态位）。

// Revision 是一次版本快照行。
type Revision struct {
	ID                string
	AppID             string
	Seq               int64
	ComposeNormalized string
	Overlay           string
	DesiredHash       string
	Verified          bool
	CreatedAt         time.Time
}

// ErrRevisionNotFound 表示版本快照不存在。
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
	return Revision{
		ID:                id,
		AppID:             w.AppID,
		Seq:               seq,
		ComposeNormalized: w.ComposeNormalized,
		Overlay:           w.Overlay,
		DesiredHash:       w.DesiredHash,
		Verified:          true,
		CreatedAt:         time.Unix(0, now).UTC(),
	}, nil
}

// GetRevision 按 ID 取版本快照；不存在返回 ErrRevisionNotFound。
func (s *Store) GetRevision(ctx context.Context, id string) (Revision, error) {
	const q = `SELECT id, app_id, seq, compose_normalized, overlay, desired_hash, verified, created_at
		FROM revisions WHERE id = ?`
	var r Revision
	var verified int64
	var created int64
	err := s.db.QueryRowContext(ctx, q, id).Scan(
		&r.ID, &r.AppID, &r.Seq, &r.ComposeNormalized, &r.Overlay, &r.DesiredHash, &verified, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Revision{}, ErrRevisionNotFound
		}
		return Revision{}, fmt.Errorf("state: get revision %s: %w", id, err)
	}
	r.Verified = verified != 0
	r.CreatedAt = time.Unix(0, created).UTC()
	return r, nil
}
