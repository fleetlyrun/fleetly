package backups

import "github.com/fleetlyrun/fleetly/internal/application/engine/contract"

// Name implements contract.Module.
func (bk *Backups) Name() string { return "backups" }

// Loops implements contract.Module: the backup loop.
func (bk *Backups) Loops() []contract.LoopSpec {
	return []contract.LoopSpec{{Name: "backup", Step: bk.Step}}
}

var _ contract.Module = (*Backups)(nil)

// Backup 域应用模块的核契约面（engine 去应用模块 import 第八批，2026-11-10）：
// 核只持 contract.Backup 接口，装配器经 engine.Deps.BackupFactory 注入。
var _ contract.Backup = (*Backups)(nil)

// Deps 与 contract.BackupPorts 的形状同构锚：两 struct 可互转即字段名/
// 顺序/类型全同——组合根工厂逐字段转发零漂移的编译期执法（字段增删改
// 不同批同步两侧即红）。
var (
	_ = contract.BackupPorts(Deps{})
	_ = Deps(contract.BackupPorts{})
)
