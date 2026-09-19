// Package statebackup 是控制面状态热备（T2.22，备份基线与信任闭环）：
// SQLite 一致性快照（VACUUM INTO）→ 回读校验（integrity_check + 关键表
// 行数抽查 + schema 版本）→ sha256 落 manifest → state_backups 台账（与
// 审计同事务）。触发三类：每次部署成功后（引擎成功路径挂钩，异步不阻塞
// 部署主链）+ 每日定时（本包守护循环）+ 手动（daemon RPC → CLI）。
//
// 诚实契约（architecture §4.2 横切硬指标 + state-model §2.7）：
//   - verify 任何一步失败 → 台账 verify_status=failed + 审计 backup.failed
//     (result=error) + 系统状态 backup 组件不健康——「绿色成功但实际没备份」
//     的路径结构性不存在（负面测试钉死）；
//   - 备份产物布局 <dir>/<ULID>/fleetly.db + manifest.json；主密钥
//     （fleetly.key）绝不进备份目录——manifest 只记密钥文件的 sha256 指纹
//     （恢复时人工核对，docs/runbooks/backup-restore.md），构造期显式拒绝
//     密钥落在备份目录内的配置；
//   - 保留策略：keep 份上限，超限删最旧（台账删行 + 审计 backup.pruned +
//     目录清除——台账只描述真实存在的备份，不保留幽灵行）。
package statebackup

import (
	"time"
)

// DefaultKeep 是保留份数缺省值（config backup.keep 非正值回落——保留策略
// 是契约默认，不允许被误配成 0 而静默关闭）。
const DefaultKeep = 7

// DefaultInterval / DefaultTriggerTimeout 是每日备份周期与单次备份预算
// （快照+校验+落账；VACUUM INTO 对 v0.1 规模的库是亚秒级，预算是防御上限）。
const (
	DefaultInterval       = 24 * time.Hour
	DefaultTriggerTimeout = 5 * time.Minute
)

// Config 是备份核心配置（config 键 backup.*；缺省值经 Normalize 回落——
// 单一事实源在本包；dir 缺省依赖 state 库路径，由装配点给出后传入）。
type Config struct {
	// Dir 是备份根目录（backup.dir；空 = 装配点回落 <state 库同目录>/backups）。
	Dir string
	// Keep 是保留份数上限（backup.keep；非正值回落 DefaultKeep）。
	Keep int
	// Interval 是每日备份周期（测试注入点；零值回落 DefaultInterval）。
	Interval time.Duration
	// TriggerTimeout 是单次备份的预算上限（零值回落 DefaultTriggerTimeout）。
	TriggerTimeout time.Duration
}

// Normalize 回落缺省值（Keep 非正值 → DefaultKeep；Interval/TriggerTimeout
// 零或负值 → 默认；Dir 原样——空目录在装配点显式回落，包内不留猜测）。
func (c Config) Normalize() Config {
	if c.Keep <= 0 {
		c.Keep = DefaultKeep
	}
	if c.Interval <= 0 {
		c.Interval = DefaultInterval
	}
	if c.TriggerTimeout <= 0 {
		c.TriggerTimeout = DefaultTriggerTimeout
	}
	return c
}
