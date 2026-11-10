package dataservices

// 数据服务卷异源 fail-closed（ADR-0097 决策 4/5，B4c 起实体中性化——三种数据服务类型
// 通吃）：数据服务自动卷在 Ensure 前过实例标记门——RuntimeUtility 挂卷执行
// dataservicetemplate 标记脚本（在场即读、缺席即铸），标记 kind/entity_id 与
// 行不等 = 同名陈旧卷（异源）显式拒绝；同源/缺席（含预建卷与无标记存量
// 卷）不受影响。受理面（Create*，卷行已在 = 同名重建形态）前置只读探测：
// 卷内有任何标记 = 异源（当前行尚未出生，在场标记必属他源）→ E_CONFLICT。
//
// 分类型消费面（B4c/B5）：database 类型注入 databasetemplate 词面（kind=database），
// pubsub 类型注入 pubsubtemplate 词面（kind=pubsub——nats 首启在卷内落标记
// `kind:pubsub`，database↔pubsub 跨类型复用与同类型异名陈旧卷都 fail-closed，
// 红队 B-击三防御承诺闭环）；bucketstore 类型注入 bucketstoretemplate 词面
// （kind=bucketstore——silo 首启同款落标记，B5 三种数据服务类型闭环）。
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

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/databasetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
)

// ErrVolumeForeignOrigin 是「数据服务自动卷携带异源实例标记」哨兵（api 面 map 为
// E_CONFLICT 带清理/换名指引；ensure 链 = 拒绝确保、载体保持现状）。词面用
// 三种数据服务类型统称「托管数据服务」的英文全称（ADR-0093 决策 6——去掉 data 的二词
// 短形是受管域禁词）。定义真源随契约面下沉 engine/contract（engine 去应用
// 模块 import 第九批）；本包既名经 var 别名零漂移。
var ErrVolumeForeignOrigin = contract.ErrVolumeForeignOrigin

// ensureVolumeOrigin 是 database 类型 ensure 链的标记门（claim-or-check）：幂等
// ——同源标记在场即过（重放/轮换后重确保零成本）；缺席即铸（首启前平台
// 代写，「模板注入属材料面」的执行位）；异源即拒。失败一律 fail-closed
// （不 Ensure——静默启动比拒绝更糟，凭证缺失路径同款判据）。
func (d *DataServices) ensureVolumeOrigin(ctx context.Context, row *database.Database, tpl databasetemplate.Template, vol *volume.Volume) error {
	if err := d.volumeOriginTopologyPass(ctx, vol, row.ID, "database"); err != nil {
		return err
	}
	marker := databasetemplate.MintInstanceMarker(row.ID, d.clock.Now())
	stdout, err := d.claimVolumeOriginMarker(ctx, "volorigin-"+row.ID,
		capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, Database: row.ID, DatabaseName: row.Name},
		tpl.Image(), tpl.ToolUser(), tpl.DataTarget(), marker, vol, row.ProjectID)
	if err != nil {
		return err
	}
	return checkInstanceMarker(stdout, row.Name, row.ID, databasetemplate.InstanceKind)
}

// ensurePubSubVolumeOrigin 是 pubsub 类型 ensure 链的标记门（B4c 实体中性化
// 接入——ensureVolumeOrigin 同构：类型词面 pubsub，探测镜像随绑版模板）。
func (d *DataServices) ensurePubSubVolumeOrigin(ctx context.Context, row *pubsub.PubSub, tpl pubsubtemplate.Template, vol *volume.Volume) error {
	if err := d.volumeOriginTopologyPass(ctx, vol, row.ID, "pubsub"); err != nil {
		return err
	}
	marker := dataservicetemplate.MintInstanceMarker(row.ID, pubsubtemplate.InstanceKind, d.clock.Now())
	stdout, err := d.claimVolumeOriginMarker(ctx, "volorigin-"+row.ID,
		capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name},
		tpl.Image(), tpl.ToolUser(), tpl.DataTarget(), marker, vol, row.ProjectID)
	if err != nil {
		return err
	}
	return checkInstanceMarker(stdout, row.Name, row.ID, pubsubtemplate.InstanceKind)
}

// ensureBucketStoreVolumeOrigin 是 bucketstore 类型 ensure 链的标记门（B5——
// ensurePubSubVolumeOrigin 同构：类型词面 bucketstore，探测镜像随绑版模板）。
func (d *DataServices) ensureBucketStoreVolumeOrigin(ctx context.Context, row *bucketstore.BucketStore, tpl bucketstoretemplate.Template, vol *volume.Volume) error {
	if err := d.volumeOriginTopologyPass(ctx, vol, row.ID, "bucketstore"); err != nil {
		return err
	}
	marker := dataservicetemplate.MintInstanceMarker(row.ID, bucketstoretemplate.InstanceKind, d.clock.Now())
	stdout, err := d.claimVolumeOriginMarker(ctx, "volorigin-"+row.ID,
		capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, BucketStore: row.ID, BucketStoreName: row.Name},
		tpl.Image(), tpl.ToolUser(), tpl.DataTarget(), marker, vol, row.ProjectID)
	if err != nil {
		return err
	}
	return checkInstanceMarker(stdout, row.Name, row.ID, bucketstoretemplate.InstanceKind)
}

// volumeOriginTopologyPass 是决策 5 的节点拓扑边界：钉住他节点的卷跳过检测
// （fail-open 覆盖缺口，诚实记日志）；控制面节点解析失败 = fail-closed 拒。
func (d *DataServices) volumeOriginTopologyPass(ctx context.Context, vol *volume.Volume, entityID, kindWord string) error {
	if vol.PinnedNodeID == "" {
		return nil
	}
	managerID, err := d.controlPlaneNode(ctx)
	if err != nil {
		return fmt.Errorf("volume origin gate: resolve control-plane node: %w", err)
	}
	if vol.PinnedNodeID != managerID {
		d.log.Warn("dataservices reconcile: volume origin gate skipped (volume pinned off the control plane; single-manager topology assumption)",
			"kind", kindWord, "entity", entityID, "volume", vol.ID, "pinned_node", vol.PinnedNodeID)
		return nil
	}
	return nil
}

// claimVolumeOriginMarker 执行「在场即读、缺席即铸」探测（挂卷工具容器跑
// dataservicetemplate 标记脚本），返回回读 stdout（幂等 claim 形态：同源恒过）。
// user 是模板 ToolUser 声明（B8-fix：非 root 引擎镜像声明 root——root 既
// 写 root 属主卷根，也使脚本内 chown --reference 属主播种生效）；dataTarget
// 是模板数据目录（属主参照源，镜像内目录的属主即引擎载体用户）。
func (d *DataServices) claimVolumeOriginMarker(ctx context.Context, reqID string, ns capability.NamespaceRef, image, user, dataTarget string, marker dataservicetemplate.InstanceMarker, vol *volume.Volume, projectID string) (string, error) {
	if d.utility == nil {
		return "", fmt.Errorf("volume origin gate requires the utility face (not assembled)")
	}
	// 标记契约住 dataservicetemplate 内核（B2a 升格，ADR-0097 决策 4 修订）——
	// 类型消费面只注入词面；探测镜像随绑版模板（tpl 由调用方按行值绑版
	// 传入，B2c）。
	argv, err := dataservicetemplate.InstanceMarkerClaimArgv(dataservicetemplate.SeedMountPoint, dataTarget, marker)
	if err != nil {
		return "", fmt.Errorf("volume origin gate: render marker: %w", err)
	}
	stdout := &limitedBuffer{max: 512}
	stderr := &limitedBuffer{max: 512}
	req := capability.UtilityRequest{
		ID:        reqID,
		Namespace: ns,
		Image:     image,
		User:      user,
		Argv:      argv,
		Networks:  d.projectNetworkFactsNamesOnly(ctx, projectID),
		Volume:    &capability.UtilityVolumeMount{VolumeID: vol.ID, Target: dataservicetemplate.SeedMountPoint},
	}
	if err := d.utility.RunUtility(ctx, req, stdout, stderr); err != nil {
		msg := err.Error()
		if tail := contract.StderrTail(stderr.String()); tail != "" {
			msg += ": " + tail
		}
		return "", fmt.Errorf("volume origin gate: probe instance marker: %s", msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// CheckVolumeOriginForCreate 是 Create* 受理面的前置只读探测（B4c 实体
// 中性化：kind 词面分派——database 走 databasetemplate、pubsub 走 pubsubtemplate、
// bucketstore 走 bucketstoretemplate〔B5〕）：挂靠卷行已在（= 同名重建形态）才探测；卷内有
// 任何实例标记（含坏形态）= 异源——当前行尚未出生，在场标记必属他源
// （含引擎换装/跨类型复用：旧 pgdata 对新 pubsub 同样是异源事实）。缺席 =
// 通过（铸记归 ensure 链）。
func (d *DataServices) CheckVolumeOriginForCreate(ctx context.Context, kind, projectID, name, engineName string) error {
	vol, err := d.repos.Volumes.GetByName(ctx, projectID, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil // 全新卷名：无存量面可查
		}
		return err
	}
	var image, user string
	switch kind {
	case dataservicetemplate.KindDatabase:
		tpl, err := databasetemplate.For(engineName, "")
		if err != nil {
			return nil // 引擎值域由受理位在册执法；此处不可达防御
		}
		image, user = tpl.Image(), tpl.ToolUser()
	case dataservicetemplate.KindPubSub:
		tpl, err := pubsubtemplate.For(engineName, "")
		if err != nil {
			return nil // 同上
		}
		image, user = tpl.Image(), tpl.ToolUser()
	case dataservicetemplate.KindBucketStore:
		tpl, err := bucketstoretemplate.For(engineName, "")
		if err != nil {
			return nil // 同上
		}
		image, user = tpl.Image(), tpl.ToolUser()
	default:
		return fmt.Errorf("volume origin gate: kind %q has no create-face probe wired", kind)
	}
	if err := d.readVolumeOriginForCreate(ctx, "volorigin-create-"+name, projectID, image, user, vol); err != nil {
		return fmt.Errorf("%w: volume %q still holds a previous %s instance", err, name, kind)
	}
	return nil
}

// readVolumeOriginForCreate 是受理前置读的共享半边（挂卷只读探测）：无标记
// （预建卷/无标记存量卷）= 通过；有任何标记（含坏形态）= ErrVolumeForeignOrigin。
// 前置读失败不拦受理（轻量化；ensure 链标记门是权威执法位）。
func (d *DataServices) readVolumeOriginForCreate(ctx context.Context, reqID, projectID, image, user string, vol *volume.Volume) error {
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
		ID:        reqID,
		Namespace: capability.NamespaceRef{Team: d.projectTeamOf(projectID), Project: projectID},
		Image:     image,
		User:      user, // 只读面同声明（ToolUser 通道一致——非 root 镜像读卷无需 root，但通道单形）
		Argv:      dataservicetemplate.InstanceMarkerReadArgv(dataservicetemplate.SeedMountPoint),
		Networks:  d.projectNetworkFactsNamesOnly(ctx, projectID),
		Volume:    &capability.UtilityVolumeMount{VolumeID: vol.ID, Target: dataservicetemplate.SeedMountPoint, ReadOnly: true},
	}
	if err := d.utility.RunUtility(ctx, req, stdout, stderr); err != nil {
		// 前置读失败不拦受理（轻量化；ensure 链标记门是权威执法位）。
		return nil
	}
	if strings.TrimSpace(stdout.String()) == "" {
		return nil // 无标记：预建卷/无标记存量卷——铸记收编面
	}
	return ErrVolumeForeignOrigin
}

// checkInstanceMarker 比对探测回读（stdout = 脚本 cat 的标记 JSON）。实体
// 中性标记（entity_id/kind）双轴比对（B4c 起签名类型词面注入）：kind
// 不等 = 卷被跨类型复用（ADR-0097 B-击三：pubsub/database 同名卷互撞即拒）；
// entity_id 不等 = 同类型异名陈旧卷。
func checkInstanceMarker(stdout, name, entityID, kind string) error {
	if stdout == "" {
		return fmt.Errorf("volume origin gate: marker claim returned no output (volume %s)", name)
	}
	marker, err := dataservicetemplate.ParseInstanceMarker([]byte(stdout))
	if err != nil {
		return fmt.Errorf("%w: volume %q carries an unreadable instance marker", ErrVolumeForeignOrigin, name)
	}
	if marker.Kind != kind {
		return fmt.Errorf("%w: volume %q was minted for a %s-kind instance (this %s instance is %s)",
			ErrVolumeForeignOrigin, name, marker.Kind, kind, entityID)
	}
	if marker.EntityID != entityID {
		return fmt.Errorf("%w: volume %q was minted for %s instance %s (this instance is %s)",
			ErrVolumeForeignOrigin, name, kind, marker.EntityID, entityID)
	}
	return nil
}
