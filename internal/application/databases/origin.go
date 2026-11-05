package databases

// 库卷异源 fail-closed（ADR-0097 决策 4/5）：数据库自动卷在 Ensure 前过
// 实例标记门——RuntimeUtility 挂卷执行 dbtemplate 标记脚本（在场即读、
// 缺席即铸），标记 database_id 与库行不等 = 同名陈旧卷（异源）显式拒绝；
// 同源/缺席（含预建卷与无标记存量卷）不受影响。受理面（CreateDatabase，
// 卷行已在 = 同名重建形态）前置只读探测：卷内有任何标记 = 异源（当前库
// 行尚未出生，在场标记必属他源）→ E_CONFLICT。
//
// 节点拓扑边界（决策 5，诚实声明）：swarm 工具容器恒在控制面 daemon 跑、
// docker 卷 node-local——标记门仅在卷未钉住或钉住控制面节点时执行
// （N0 单控制面口径，PinVolumes 同判据），钉住他节点跳过检测并记日志
// （fail-open 覆盖缺口；k3s 工具容器挂 PVC 由调度器回 PV 节点，天然
// 节点正确）。后续手：service 形探测容器。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/application/engineports"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// ErrVolumeForeignOrigin 是「库卷携带异源实例标记」哨兵（api 面 map 为
// E_CONFLICT 带清理/换名指引；ensure 链 = 拒绝确保、载体保持现状）。
var ErrVolumeForeignOrigin = errors.New("database volume carries a foreign instance marker")

// ensureVolumeOrigin 是 ensure 链的标记门（claim-or-check）：幂等——同源
// 标记在场即过（重放/轮换后重确保零成本）；缺席即铸（首启前平台代写，
// 「模板注入属材料面」的执行位）；异源即拒。失败一律 fail-closed（不
// Ensure——静默启动比拒绝更糟，凭证缺失路径同款判据）。
func (d *Databases) ensureVolumeOrigin(ctx context.Context, row *database.Database, tpl dbtemplate.Template, vol *volume.Volume) error {
	if d.utility == nil {
		return fmt.Errorf("volume origin gate requires the utility face (not assembled)")
	}
	// 节点拓扑：钉住他节点的卷跳过检测（决策 5 覆盖缺口，诚实记日志）。
	if vol.PinnedNodeID != "" {
		managerID, err := d.controlPlaneNode(ctx)
		if err != nil {
			return fmt.Errorf("volume origin gate: resolve control-plane node: %w", err)
		}
		if vol.PinnedNodeID != managerID {
			d.log.Warn("database reconcile: volume origin gate skipped (volume pinned off the control plane; single-manager topology assumption)",
				"database", row.ID, "volume", vol.ID, "pinned_node", vol.PinnedNodeID)
			return nil
		}
	}
	// 标记契约住 enginetemplate 内核（B2a 升格，ADR-0097 决策 4 修订）——
	// dbtemplate 再导出面注入数据库家族词面；探测镜像随绑版模板（Default，
	// TODO(B2c)：行值绑版）。
	argv, err := dbtemplate.InstanceMarkerClaimArgv(dbtemplate.SeedMountPoint,
		dbtemplate.MintInstanceMarker(row.ID, d.clock.Now()))
	if err != nil {
		return fmt.Errorf("volume origin gate: render marker: %w", err)
	}
	stdout := &limitedBuffer{max: 512}
	stderr := &limitedBuffer{max: 512}
	req := capability.UtilityRequest{
		ID:        "volorigin-" + row.ID,
		Namespace: capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, Database: row.ID, DatabaseName: row.Name},
		Image:     tpl.Image(),
		Argv:      argv,
		Networks:  d.projectNetworkFactsNamesOnly(ctx, row.ProjectID),
		Volume:    &capability.UtilityVolumeMount{VolumeID: vol.ID, Target: dbtemplate.SeedMountPoint},
	}
	if err := d.utility.RunUtility(ctx, req, stdout, stderr); err != nil {
		msg := err.Error()
		if tail := engineports.StderrTail(stderr.String()); tail != "" {
			msg += ": " + tail
		}
		return fmt.Errorf("volume origin gate: probe instance marker: %s", msg)
	}
	return checkInstanceMarker(strings.TrimSpace(stdout.String()), row)
}

// CheckVolumeOriginForCreate 是受理面（CreateDatabase）的前置只读探测：
// 挂靠卷行已在（= 同名重建形态）才探测；卷内有任何实例标记（含坏形态）
// = 异源——当前库行尚未出生，在场标记必属他源（含引擎换装：旧 pgdata
// 同样是异源事实）。缺席 = 通过（铸记归 ensure 链）。
func (d *Databases) CheckVolumeOriginForCreate(ctx context.Context, projectID, name, engineName string) error {
	vol, err := d.repos.Volumes.GetByName(ctx, projectID, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil // 全新卷名：无存量面可查
		}
		return err
	}
	tpl, err := dbtemplate.For(engineName, "")
	if err != nil {
		return nil // 引擎值域由受理位在册执法；此处不可达防御
	}
	if d.utility == nil {
		return nil // 面缺席：受理前置退让，ensure 链标记门兜底（fail-closed 在彼处）
	}
	if vol.PinnedNodeID != "" {
		managerID, err := d.controlPlaneNode(ctx)
		if err == nil && vol.PinnedNodeID != managerID {
			return nil // 拓扑缺口同 ensure 链：跳过前置读（ensure 链同款跳过）
		}
		// 解析失败不前置拦（受理面轻量化）；ensure 链如实执法。
	}
	stdout := &limitedBuffer{max: 512}
	stderr := &limitedBuffer{max: 512}
	req := capability.UtilityRequest{
		ID:        "volorigin-create-" + name,
		Namespace: capability.NamespaceRef{Team: d.projectTeamOf(projectID), Project: projectID},
		Image:     tpl.Image(),
		Argv:      dbtemplate.InstanceMarkerReadArgv(dbtemplate.SeedMountPoint),
		Networks:  d.projectNetworkFactsNamesOnly(ctx, projectID),
		Volume:    &capability.UtilityVolumeMount{VolumeID: vol.ID, Target: dbtemplate.SeedMountPoint, ReadOnly: true},
	}
	if err := d.utility.RunUtility(ctx, req, stdout, stderr); err != nil {
		// 前置读失败不拦受理（轻量化；ensure 链标记门是权威执法位）。
		return nil
	}
	if strings.TrimSpace(stdout.String()) == "" {
		return nil // 无标记：预建卷/无标记存量卷——铸记收编面
	}
	return fmt.Errorf("%w: volume %q still holds a previous database instance", ErrVolumeForeignOrigin, name)
}

// checkInstanceMarker 比对探测回读（stdout = 脚本 cat 的标记 JSON）。实体
// 中性标记（entity_id/family）双轴比对：family 不等 = 卷被跨家族复用（B2a
// 起三家族通吃的异源面）；entity_id 不等 = 同家族异库名陈旧卷。
func checkInstanceMarker(stdout string, row *database.Database) error {
	if stdout == "" {
		return fmt.Errorf("volume origin gate: marker claim returned no output (volume %s)", row.Name)
	}
	marker, err := dbtemplate.ParseInstanceMarker([]byte(stdout))
	if err != nil {
		return fmt.Errorf("%w: volume %q carries an unreadable instance marker", ErrVolumeForeignOrigin, row.Name)
	}
	if marker.Family != dbtemplate.InstanceFamily {
		return fmt.Errorf("%w: volume %q was minted for a %s-family instance (this database is %s)",
			ErrVolumeForeignOrigin, row.Name, marker.Family, row.ID)
	}
	if marker.EntityID != row.ID {
		return fmt.Errorf("%w: volume %q was minted for database %s (this database is %s)",
			ErrVolumeForeignOrigin, row.Name, marker.EntityID, row.ID)
	}
	return nil
}
