package fleetlygrpc

// PlatformService 是平台级操作面（system.proto；`fleetly platform` 消费）：
// Platform Backup 的手动触发与快照列举（ADR-0039 决策 10；F2.3 升级序的
// 前置动词消费面，ADR-0015）。与 SystemService 公开诊断面分立——本面
// 全部要凭证（platform:write / platform:read）。

import (
	"context"
	"errors"
	"time"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

type PlatformService struct {
	systemv1.UnimplementedPlatformServiceServer
	s *Services
}

// TriggerPlatformBackup 同步执行一次 Platform Backup（升级序前置；与数据
// 库轨"触发不等执行"分立——restic 链对控制面数据根是本地操作，执行时长
// 有上界，同步等待面成立：升级脚本拿到的成功即事实）。
func (svc *PlatformService) TriggerPlatformBackup(ctx context.Context, _ *systemv1.TriggerPlatformBackupRequest) (*systemv1.TriggerPlatformBackupResponse, error) {
	// 平台管理员门（R08-H1）：平台仓备份是全平台材料，任何 Team 的
	// builtin-admin 不得触发（门先于引擎执行与备份事件/审计）。
	if err := RequirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	snap, err := svc.s.Engine.TriggerPlatformBackup(ctx)
	if err != nil {
		return nil, platformBackupErr(err)
	}
	return &systemv1.TriggerPlatformBackupResponse{Snapshot: platformSnapshotMsg(snap)}, nil
}

// platformBackupErr 把引擎错误映射成 API errcode（H6）：单飞冲突是状态
// 冲突（409/E_CONFLICT，"已有备份在途"），不是一次备份失败；其余错误维持
// E_PLATFORM_BACKUP_FAILED（升级序的失败硬停语义不变）。
func platformBackupErr(err error) error {
	if errors.Is(err, engine.ErrPlatformBackupInProgress) {
		return apperr.New("E_CONFLICT", "platform backup already in progress").WithCause(err)
	}
	return apperr.New("E_PLATFORM_BACKUP_FAILED", "%v", err).WithCause(err)
}

// ListPlatformBackups 新→旧分页（restic 仓直读；after_snapshot_id 游标按
// 短 id 精确匹配——restic 快照无自然序键，等值命中是唯一确定性语义，
// 未命中游标精确拒绝）。
func (svc *PlatformService) ListPlatformBackups(ctx context.Context, req *systemv1.ListPlatformBackupsRequest) (*systemv1.ListPlatformBackupsResponse, error) {
	// 平台管理员门（R08-H1）：快照清单暴露全平台备份时点，与触发面同门
	// （门先于 restic 读取）。
	if err := RequirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	snaps, err := svc.s.Engine.ListPlatformSnapshots(ctx)
	if err != nil {
		return nil, apperr.New("E_PLATFORM_BACKUP_FAILED", "%v", err).WithCause(err)
	}
	start := 0
	if after := req.GetAfterSnapshotId(); after != "" {
		start = -1
		for i := range snaps {
			if snaps[i].ID == after {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return nil, apperr.New("E_INVALID_ARGUMENT",
				"after_snapshot_id %s does not match any snapshot; list the first page without a cursor", after)
		}
	}
	page := snaps[start:]
	if limit := int(req.GetLimit()); limit > 0 && len(page) > limit { //nolint:gosec // 旗标域内钳制
		page = page[:limit]
	}
	out := &systemv1.ListPlatformBackupsResponse{}
	for i := range page {
		out.Snapshots = append(out.Snapshots, platformSnapshotMsg(&page[i]))
	}
	return out, nil
}

// platformSnapshotMsg 快照行 → proto 投影（时间截秒整秒 RFC3339——实体
// 时间惯例；restic 原生时间带亚秒精度）。
func platformSnapshotMsg(s *engine.PlatformSnapshot) *systemv1.PlatformSnapshot {
	return &systemv1.PlatformSnapshot{
		Id: s.ID, Time: s.Time.UTC().Truncate(time.Second).Format(time.RFC3339), Hostname: s.Hostname,
	}
}
