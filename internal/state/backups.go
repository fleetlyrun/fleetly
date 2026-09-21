package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// 状态备份台账（T2.22；state-model §2.7 + architecture §4.2 备份基线）：
// state_backups 行 = 一次热备快照的诚实账（manifest + sha256 回读校验的
// 结果落这里；verify 失败必须以 verify_status=failed 落账——「绿色假成功」
// 是备份信任闭环的第一杀手）。触发三类：daily / pre_upgrade / post_deploy，
// 手动 = manual；kind 枚举见 00008 迁移（历史值 hot/cold 保留兼容）。
// 审计与台账行同事务（系统自动动作必入审计，state-model §2.9）。

// 备份 kind 词表（00008 CHECK 的包内镜像；写入入口校验用）。
const (
	BackupKindDaily      = "daily"
	BackupKindPreUpgrade = "pre_upgrade"
	BackupKindPostDeploy = "post_deploy"
	BackupKindManual     = "manual"
)

// verify_status 词表。
const (
	BackupVerifyPending  = "pending"
	BackupVerifyVerified = "verified"
	BackupVerifyFailed   = "failed"
)

// upload_status 词表（E3-3 上传轨，设计 §2.3/D-S3-4）。none = 未上传——
// 既含 s3.mode=unset 的合法态（外部端点未配置不算失败、不红），也含上传
// 步尚未执行的 verified 行（落账先于上传，时序窗口内的中间态）。
const (
	BackupUploadNone   = "none"
	BackupUploadOK     = "ok"
	BackupUploadFailed = "failed"
)

// StateBackup 是 state_backups 台账行的只读投影。
type StateBackup struct {
	// ID 记录 ID（ULID；与备份目录名一致——<backup.dir>/<ID>/fleetly.db）。
	ID string
	// Kind 触发类别：daily / pre_upgrade / post_deploy / manual（历史行可为
	// hot/cold——00008 之前的占位枚举）。
	Kind string
	// Path 是快照文件路径（manifest.json 与其同目录）。
	Path string
	// SHA256 是快照文件的 sha256（hex；回读校验对象）。
	SHA256 string
	// SizeBytes 是快照文件字节数。
	SizeBytes int64
	// VerifyStatus 是回读校验结论：verified / failed（pending = 落账时未校验，
	// 现行写入路径不会产生——校验先于落账，唯一诚实态是已判定）。
	VerifyStatus string
	// Error 是校验失败原因原文（verified 行为空）。
	Error string
	// CreatedAt 是台账落账时刻（UTC）。
	CreatedAt time.Time
	// UploadStatus 是远端（restic repo）上传结论：none / ok / failed
	//（E3-3 上传轨；本地 verify 语义不变——上传失败不回写 verify_status）。
	UploadStatus string
	// UploadedAt 是最近一次上传尝试的完成时刻（UTC；零值 = 从未尝试——
	// upload_status=none 的行）。ok/failed 都更新：failed 行的操作者同样
	// 需要知道尝试时点，错误详情在 UploadError。
	UploadedAt time.Time
	// UploadError 是上传失败原因摘要（截断上界见 UpdateStateBackupUpload；
	// 不含 secret——restic env 凭证值禁止进台账/事件/日志，state-model §2.9）。
	UploadError string
}

// BackupWrite 是一次备份的落账载荷（RecordStateBackup 消费）。
type BackupWrite struct {
	// ID 留空自动生成 ULID（调用方需要目录名与台账 ID 一致时自行生成后
	// 传入——Manager 先建目录后落账，传显式 ID）。
	ID string
	// At 落账时刻（零值取写入时刻）。
	At time.Time
	// Kind 必须是注册词表之一（unknown kind 直接报错——台账不接受不可归类
	// 的行）。
	Kind string
	// Path / SHA256 / SizeBytes 是快照产物事实。
	Path   string
	SHA256 string
	Size   int64
	// Verify 必须是 verified / failed（pending 不可落账——落账即判定）。
	Verify string
	// Error 校验失败原因（failed 行必填）。
	Error string
	// AuditActor 审计 actor（默认 "system"——手动触发的行由 RPC 层传 token
	// actor 形态，但备份本体恒为 daemon 执行，默认值覆盖全部自动路径）。
	AuditActor string
}

// RecordStateBackup 落一行备份台账（与审计同事务，fail-closed：审计写不出
// 合法行则台账行一并回滚——备份账与审计账互为证据）。kind/verify 词表
// 校验在此收口，非法值整单拒绝。
func (s *Store) RecordStateBackup(ctx context.Context, w BackupWrite) (StateBackup, error) {
	if !validBackupKind(w.Kind) {
		return StateBackup{}, fmt.Errorf("state: record backup: unknown kind %q", w.Kind)
	}
	if w.Verify != BackupVerifyVerified && w.Verify != BackupVerifyFailed {
		return StateBackup{}, fmt.Errorf("state: record backup: verify status must be verified|failed, got %q", w.Verify)
	}
	if w.Verify == BackupVerifyFailed && strings.TrimSpace(w.Error) == "" {
		return StateBackup{}, fmt.Errorf("state: record backup: failed row requires error detail")
	}
	id := w.ID
	if id == "" {
		id = ulid.Make().String()
	}
	at := w.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	actor := w.AuditActor
	if actor == "" {
		actor = "system"
	}
	action := "backup.completed"
	if w.Verify == BackupVerifyFailed {
		action = "backup.failed"
	}
	err := s.InTx(ctx, func(tx *Tx) error {
		const q = `INSERT INTO state_backups
			(id, created_at, kind, path, sha256, size_bytes, verify_status, error, upload_status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
		if _, err := tx.ExecContext(ctx, q,
			id, at.UnixNano(), w.Kind, w.Path, w.SHA256, w.Size, w.Verify, w.Error,
			BackupUploadNone); err != nil {
			return fmt.Errorf("state: insert state_backup: %w", err)
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:  actor,
			Action: action,
			Target: "backup:" + id,
			Result: mapVerifyResult(w.Verify),
			// 失败行 diff_summary 携带归因（错误原文属于诚实账的一部分；
			// 不含密钥材料——secret 禁入审计，state-model §2.9）。
			DiffSummary: backupAuditSummary(w),
		})
	})
	if err != nil {
		return StateBackup{}, err
	}
	return StateBackup{
		ID: id, Kind: w.Kind, Path: w.Path, SHA256: w.SHA256,
		SizeBytes: w.Size, VerifyStatus: w.Verify, Error: w.Error, CreatedAt: at,
		// 落账即初始态：上传轨从 none 起步（上传步随后经
		// UpdateStateBackupUpload 推进）。
		UploadStatus: BackupUploadNone,
	}, nil
}

// mapVerifyResult 把 verify 结论映射为审计 result 枚举（ok/error）。
func mapVerifyResult(verify string) string {
	if verify == BackupVerifyVerified {
		return "ok"
	}
	return "error"
}

// backupAuditSummary 生成审计 diff 摘要（脱敏：路径/kind/结论/错误原文；
// 不含密钥与数据内容）。MG-6：经 DiffSummary 构造——%q 是 Go 转义非
// JSON 转义（错误原文含引号/控制字符时会产出破包 JSON），json.Marshal
// 才是唯一正确转义。
func backupAuditSummary(w BackupWrite) string {
	kvs := []any{"kind", w.Kind, "verify", w.Verify}
	if w.Error != "" {
		kvs = append(kvs, "error", w.Error)
	}
	return DiffSummary(kvs...)
}

// validBackupKind 报告 kind 是否在注册词表（含历史兼容值）。
func validBackupKind(kind string) bool {
	switch kind {
	case BackupKindDaily, BackupKindPreUpgrade, BackupKindPostDeploy, BackupKindManual,
		"hot", "cold":
		return true
	}
	return false
}

// ListStateBackups 按 created_at 倒序返回最近 n 条台账（n<=0 = 全部——
// 台账量级 = 保留份数 + 失败行，天花板低）。
func (s *Store) ListStateBackups(ctx context.Context, n int) ([]StateBackup, error) {
	const qAll = `SELECT id, created_at, kind, path, sha256, size_bytes, verify_status, error,
		upload_status, uploaded_at, upload_error
		FROM state_backups ORDER BY created_at DESC, id DESC`
	const qLim = qAll + ` LIMIT ?`
	var (
		rows *sql.Rows
		err  error
	)
	if n > 0 {
		rows, err = s.db.QueryContext(ctx, qLim, n)
	} else {
		rows, err = s.db.QueryContext(ctx, qAll)
	}
	if err != nil {
		return nil, fmt.Errorf("state: list state_backups: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []StateBackup
	for rows.Next() {
		var r StateBackup
		var atNano int64
		var uploadedAtNano *int64
		var uploadError *string
		if err := rows.Scan(&r.ID, &atNano, &r.Kind, &r.Path, &r.SHA256, &r.SizeBytes,
			&r.VerifyStatus, &r.Error,
			&r.UploadStatus, &uploadedAtNano, &uploadError); err != nil {
			return nil, fmt.Errorf("state: scan state_backup: %w", err)
		}
		r.CreatedAt = time.Unix(0, atNano).UTC()
		if uploadedAtNano != nil {
			r.UploadedAt = time.Unix(0, *uploadedAtNano).UTC()
		}
		if uploadError != nil {
			r.UploadError = *uploadError
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("state: iterate state_backups: %w", err)
	}
	return out, nil
}

// uploadErrorMaxBytes 是 upload_error 的截断上界（合理长度：错误摘要可
// 定位即可，不收整段 restic 输出——输出尾部可能携带路径/端点等现场信息，
// 4KiB 上界兼顾归因与台账卫生）。
const uploadErrorMaxBytes = 4096

// UpdateStateBackupUpload 落一次上传尝试的结论（E3-3）：status 必须是
// ok|failed；failed 行必须带 upload_error（截断到 uploadErrorMaxBytes——
// 调用方负责不含 secret，存储层兜底截断防整段输出入库）。uploaded_at
// 记录本次尝试完成时刻（ok/failed 都更新，见 StateBackup.UploadedAt 注）。
func (s *Store) UpdateStateBackupUpload(ctx context.Context, id, status, errText string) error {
	if status != BackupUploadOK && status != BackupUploadFailed {
		return fmt.Errorf("state: update backup upload: status must be ok|failed, got %q", status)
	}
	if status == BackupUploadFailed && strings.TrimSpace(errText) == "" {
		return fmt.Errorf("state: update backup upload: failed status requires error detail")
	}
	if len(errText) > uploadErrorMaxBytes {
		errText = errText[:uploadErrorMaxBytes]
	}
	now := nowNano()
	err := s.InTx(ctx, func(tx *Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE state_backups SET upload_status = ?, uploaded_at = ?, upload_error = ? WHERE id = ?`,
			status, now, errText, id)
		if err != nil {
			return fmt.Errorf("state: update state_backup upload: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return fmt.Errorf("state: update state_backup upload: row %s not found", id)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// GetStateBackup 按 id 读单条台账行（上传步回读更新后的行投影用；缺失
// 返回 ErrObjectNotFound 语义的显式错误）。
func (s *Store) GetStateBackup(ctx context.Context, id string) (StateBackup, error) {
	rows, err := s.ListStateBackups(ctx, 0)
	if err != nil {
		return StateBackup{}, err
	}
	for _, r := range rows {
		if r.ID == id {
			return r, nil
		}
	}
	return StateBackup{}, fmt.Errorf("state: get state_backup %s: %w", id, ErrObjectNotFound)
}

// LatestStateBackup 返回最近一条台账（无备份记录 → nil, nil——调用方据此
// 表达「从未成功备份」的不健康态，不谎报）。
func (s *Store) LatestStateBackup(ctx context.Context) (*StateBackup, error) {
	rows, err := s.ListStateBackups(ctx, 1)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// DeleteStateBackup 删除一行台账（保留期清理用；与 backup.pruned 审计同
// 事务——删账必留痕）。返回删除行的 path（调用方负责目录清除）。
func (s *Store) DeleteStateBackup(ctx context.Context, id string) (string, error) {
	var path string
	err := s.InTx(ctx, func(tx *Tx) error {
		if err := tx.QueryRowContext(ctx,
			`SELECT path FROM state_backups WHERE id = ?`, id).Scan(&path); err != nil {
			return fmt.Errorf("state: read state_backup %s: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM state_backups WHERE id = ?`, id); err != nil {
			return fmt.Errorf("state: delete state_backup %s: %w", id, err)
		}
		return tx.WriteAudit(ctx, AuditEntry{
			Actor:  "system",
			Action: "backup.pruned",
			Target: "backup:" + id,
			Result: "ok",
		})
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// VacuumInto 产出数据库一致性快照到 target 路径（SQLite VACUUM INTO——
// 单语句原子：读事务内整库落盘，WAL 下与读写并行安全；目标文件必须不
// 存在，由 SQLite 自身强制。目标经绑定参数下发——SQLite 的 INTO 子句
// 接受表达式实参，modernc 驱动实证支持，无字面量拼接面）。
func (s *Store) VacuumInto(ctx context.Context, target string) error {
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, target); err != nil {
		return fmt.Errorf("state: vacuum into %s: %w", target, err)
	}
	return nil
}

// SchemaVersion 返回已应用迁移版本（goose 版本表最大 version_id；manifest
// 的 schema_version 字段来源）。空库不该发生（Open 即迁移），查询失败照
// 实上抛。
func (s *Store) SchemaVersion(ctx context.Context) (int64, error) {
	var v int64
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version_id), 0) FROM `+gooseVersionTableName).Scan(&v); err != nil {
		return 0, fmt.Errorf("state: read schema version: %w", err)
	}
	return v, nil
}
