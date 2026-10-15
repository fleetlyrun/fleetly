// Package backup 是 Backup 台账聚合的领域面（F2.2，ADR-0039 决策 6，
// ADR-0075 Repo 接口层）：行先落（pending）再执行，成功回填 ObjectStore
// 回执（key/digest/size——restore verify 的校验锚）。保留窗创建时刻冻结
// 在行上（retention_secs 快照）。行类型、对象键公式与 Repo 接口在此
// 单源，SQLite 实现住 infrastructure/persistence/backup。
package backup

import (
	"fmt"
	"time"
)

// 状态值域（migration 00019 注释同源）。
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
)

// Backup 是一次备份的台账行。
type Backup struct {
	ID            string
	Seq           int64
	ProjectID     string
	DatabaseID    string
	Engine        string
	ObjectKey     string
	Digest        string
	SizeBytes     int64
	RetentionSecs int64
	Status        string
	Error         string
	StartedAt     string
	FinishedAt    string
	CreatedAt     string
}

// KeyMint 铸对象键（backups/<projectID>/<databaseID>/<ts>-<id>；无冒号
// 紧凑 UTC 时间戳——Windows 控制面纪律，ADR-0039 决策 6）。
func KeyMint(projectID, databaseID, backupID string, now time.Time) string {
	return fmt.Sprintf("backups/%s/%s/%s-%s", projectID, databaseID, now.UTC().Format("20060102T150405Z"), backupID)
}
