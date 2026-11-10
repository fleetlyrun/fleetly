package backups

// 事件载荷编码面（P5 wave-3 随域拆包自 engine events.go 迁入；事件名与
// 载荷 schema 真源随 engine 去应用模块 import 第八批下沉 engine/contract
// ——核 schemareg 注册面与 eventcode 注册表同源，应用模块侧直接消费
// contract.*）。

import (
	"encoding/json"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
)

// backupEventPayloadJSON 序列化备份事件载荷（纯标量结构，Marshal 不
// 失败；失败即编程错误）。
func backupEventPayloadJSON(b *backup.Backup, errMsg string) []byte {
	out, _ := json.Marshal(contract.BackupEventPayload{
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
	out, _ := json.Marshal(contract.BackupRestoredEventPayload{
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
	out, _ := json.Marshal(contract.BackupOrphanSweptEventPayload{Keys: keys})
	return out
}

// platformBackupEventJSON 序列化平台备份事件载荷（同上随域迁入）。
func platformBackupEventJSON(retention, errMsg string) []byte {
	out, _ := json.Marshal(contract.PlatformBackupEventPayload{
		Retention: retention,
		Error:     errMsg,
	})
	return out
}
