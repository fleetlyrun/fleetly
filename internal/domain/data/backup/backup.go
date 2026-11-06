// Package backup 是 Backup 台账聚合的领域面（F2.2，ADR-0039 决策 6，
// ADR-0075 Repo 接口层）：行先落（pending）再执行，成功回填 ObjectStore
// 回执（key/digest/size——restore verify 的校验锚）。保留窗创建时刻冻结
// 在行上（retention_secs 快照）。行类型、对象键公式与 Repo 接口在此
// 单源，SQLite 实现住 infrastructure/persistence/backup。
//
// B4a（ADR-0096 决策 6）起产物换形为 manifest 锚定对象集（manifest.go）：
// ObjectKey 语义 = manifest 键，Digest = total_digest，SizeBytes = 成员
// 合计——消费面（verify/download/restore/prune）全 manifest 驱动。
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
	ID         string
	Seq        int64
	ProjectID  string
	DatabaseID string
	Engine     string
	// EngineVersion 是铸造时刻源库行上的版本短名（ADR-0094 决策 5：
	// RestoreCompat 的源版本锚）。'' = 旧行「版本未记录」语义（migration
	// 00039 前的行）——恢复受理对空源同引擎放行但标注降级风险自负。
	EngineVersion string
	// ObjectKey 是产物锚键。B4a 起 = manifest 键（集前缀 + manifest.json，
	// ManifestKeyOf）；B4a 前旧行 = 单对象键（legacy 形态不迁移不承诺，
	// 消费面诚实失败、保留滚动按行直删兜底）。
	ObjectKey string
	// Digest 是产物总摘要。B4a 起 = manifest total_digest（成员 sha256 的
	// 有序聚合摘要）；旧行 = 单对象 sha256。
	Digest string
	// SizeBytes 是产物字节数。B4a 起 = 成员合计（不含 manifest 自身）；
	// 旧行 = 单对象大小。
	SizeBytes     int64
	RetentionSecs int64
	Status        string
	Error         string
	StartedAt     string
	FinishedAt    string
	CreatedAt     string
}

// KeyMint 铸对象集前缀（backups/<projectID>/<databaseID>/<ts>-<id>；无冒号
// 紧凑 UTC 时间戳——Windows 控制面纪律，ADR-0039 决策 6）。B4a 起本公式的
// 产物是 manifest 锚定对象集的**集前缀**：成员（MemberKeyOf）与 manifest
// （ManifestKeyOf）都住在其下——时间戳一次铸造、集内同段（见 manifest.go）。
func KeyMint(projectID, databaseID, backupID string, now time.Time) string {
	return fmt.Sprintf("%s%s/%s/%s-%s", RootPrefix, projectID, databaseID, now.UTC().Format("20060102T150405Z"), backupID)
}
