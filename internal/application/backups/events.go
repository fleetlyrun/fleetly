package backups

// 事件名与载荷（P5 wave-3 随域拆包自 engine events.go 迁入；核侧经别名
// 消费——schemareg 注册面与 eventcode 注册表同源）。

import (
	"encoding/json"

	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
)

// Backup 事件名锚定（F2.2，ADR-0039；B4a 扩 manifest 面；2026-11-10 主体
// 中性化更名——database.backup_* / database.restored 旧名不再发射，无兼容
// 别名）。usage 反扫的字面量命中点。
const (
	// EventBackupSucceeded 是单次备份成功（engine 备份循环发射；载荷回执
	// B4a 起 = manifest 锚三元组：object_key=manifest 键 / digest=
	// total_digest / size_bytes=成员合计）。
	EventBackupSucceeded = "backup.succeeded"
	// EventBackupFailed 是单次备份失败（engine 备份循环发射）。
	EventBackupFailed = "backup.failed"
	// EventBackupOrphanSwept 是孤儿对账回收（B4a，红队 A3③：List 对账
	// 删除 manifest 缺失的成员对象——部分成功残留/历史半删态，宽限窗外）。
	EventBackupOrphanSwept = "backup.orphan_swept"
	// EventBackupRestored 是恢复完成（engine 恢复面发射；失败走行
	// restore_error）。
	EventBackupRestored = "backup.restored"
	// EventPlatformBackupOK 是平台备份全链成功（restic 双仓）。
	EventPlatformBackupOK = "platform.backup_succeeded"
	// EventPlatformBackupFail 是平台备份失败（restic 错误尾随载荷）。
	EventPlatformBackupFail = "platform.backup_failed"
)

// BackupEventPayload 是 backup.succeeded/backup.failed 载荷 schema（F2.2，
// ADR-0039；字段只增）。统一形态：成功携带 ObjectStore 回执三元组
// （B4a 起 manifest 锚语义：key/digest/size 见 EventBackupSucceeded 注），
// 失败携带 error（用户可见文本英文）。kind/subject_id 是主体中性轴
// （database/pubsub/bucketstore）。
type BackupEventPayload struct {
	BackupID  string `json:"backup_id"`
	Kind      string `json:"kind"`
	SubjectID string `json:"subject_id"`
	ProjectID string `json:"project_id"`
	Engine    string `json:"engine"`
	ObjectKey string `json:"object_key,omitempty"`
	Digest    string `json:"digest,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Error     string `json:"error,omitempty"`
}

// BackupOrphanSweptEventPayload 是 backup.orphan_swept 载荷 schema（B4a：
// 被回收的对象键清单——对象集前缀在事件 id 轴，键清单是完整事实面）。
type BackupOrphanSweptEventPayload struct {
	Keys []string `json:"keys"`
}

// BackupRestoredEventPayload 是 backup.restored 载荷 schema（恢复完成
// 事实：目标主体 + 源备份）。
type BackupRestoredEventPayload struct {
	Kind       string `json:"kind"`
	SubjectID  string `json:"subject_id"`
	ProjectID  string `json:"project_id"`
	FromBackup string `json:"from_backup"`
	ObjectKey  string `json:"object_key,omitempty"`
}

// PlatformBackupEventPayload 是 platform.backup_* 载荷 schema（restic 链
// 整体成败；快照细节在仓库自身——事件只携带节拍事实）。
type PlatformBackupEventPayload struct {
	Retention string `json:"retention,omitempty"`
	Error     string `json:"error,omitempty"`
}

// backupEventPayloadJSON 序列化备份事件载荷（纯标量结构，Marshal 不
// 失败；失败即编程错误）。
func backupEventPayloadJSON(b *backup.Backup, errMsg string) []byte {
	out, _ := json.Marshal(BackupEventPayload{
		BackupID:  b.ID,
		Kind:      b.Subject.Kind,
		SubjectID: b.Subject.ID,
		ProjectID: b.ProjectID,
		Engine:    b.Engine,
		ObjectKey: b.ObjectKey,
		Digest:    b.Digest,
		SizeBytes: b.SizeBytes,
		Error:     errMsg,
	})
	return out
}

// backupRestoredEventJSON 序列化恢复完成事件载荷（主体中性：kind 是目标
// 主体类型，subject_id 是目标实例行 id）。
func backupRestoredEventJSON(kind, subjectID, projectID string, src *backup.Backup) []byte {
	out, _ := json.Marshal(BackupRestoredEventPayload{
		Kind:       kind,
		SubjectID:  subjectID,
		ProjectID:  projectID,
		FromBackup: src.ID,
		ObjectKey:  src.ObjectKey,
	})
	return out
}

// orphanSweptEventJSON 序列化孤儿对账回收事件载荷（纯清单结构，Marshal
// 不失败；失败即编程错误）。
func orphanSweptEventJSON(keys []string) []byte {
	out, _ := json.Marshal(BackupOrphanSweptEventPayload{Keys: keys})
	return out
}

// platformBackupEventJSON 序列化平台备份事件载荷（同上随域迁入）。
func platformBackupEventJSON(retention, errMsg string) []byte {
	out, _ := json.Marshal(PlatformBackupEventPayload{
		Retention: retention,
		Error:     errMsg,
	})
	return out
}
