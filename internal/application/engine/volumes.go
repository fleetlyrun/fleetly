package engine

// 卷附件边界解析与存量 adoption 契约测试（ADR-0097）：VolumeAttachment.VolumeId
// 的值语义 = **卷行 id**（26 位 ULID）——name 只活在受理输入与卷表行，
// 运行时零 name（ADR-0092 边界解析律延伸到卷附件面）。两个解析位：
//   - 受理咽喉 composeSpec（合并+变量合成之后、冻结之前）——新冻结体恒
//     id 形，Deploy/Preview 四源同喉收敛；
//   - 冻结体装载点 loadSpec——存量冻结体（本 ADR 之前冻结、附件携带
//     name）双形解析（内存态改写，不改冻结字节）。
//
// 数据库自动卷的解析在 databases 域（ensureDatabaseVolume 返回行，投影以
// 行 id 为载体键——库名确定性公式的 id 化形态）。

import (
	"context"
	"errors"
	"fmt"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// resolveVolumeRefs 把 spec 全部卷附件从 name 形就地解析为卷行 id 形
// （entityid.LooksMinted 直通——双形幂等：新冻结体恒 id 形重入零查表；
// 形如 ULID 的卷名按 id 直通，与旧名键载体行为逐字节一致，ADR-0092 决策 3
// 同款判据豁免）。卷名不在册 = 精确失败（受理位即拒，不再留给部署期
// pinVolumes 才炸——「引用在册性留给引擎部署期」的旧口径随本 ADR 收口）。
func (e *Engine) resolveVolumeRefs(ctx context.Context, s *specv1.AppSpec) error {
	projectID := s.GetApp().GetProject()
	for _, p := range s.GetProcesses() {
		for _, v := range p.GetVolumes() {
			if entityid.LooksMinted(v.GetVolumeId()) {
				continue // 已是卷行 id（新冻结体 / 幂等重入）
			}
			row, err := e.repos.Volumes.GetByName(ctx, projectID, v.GetVolumeId())
			if errors.Is(err, state.ErrNotFound) {
				// NotFound 在本位是校验风味（用户可修输入），不透传
				// state.ErrNotFound——避免 api 层落 E_NOT_FOUND 的资源语义。
				return fmt.Errorf("%w: process %q mounts volume %q (create it with 'fleetly volumes create' and resubmit, or drop the mount)",
					ErrVolumeUnresolved, p.GetName(), v.GetVolumeId())
			}
			if err != nil {
				return fmt.Errorf("resolve volume %q for process %q: %w", v.GetVolumeId(), p.GetName(), err)
			}
			v.VolumeId = row.ID
		}
	}
	return nil
}

// GuardVolumeCarriers 执法存量 adoption（ADR-0097 决策 3，materialize 与
// databases 收敛循环共用的单源契约测试）：对期望集引用的每个卷行，探测运行侧
// 载体——新键（行 id）在场即过；新键缺席而旧名键在场 = 存量名键载体
// （本 ADR 之前部署的物理卷）→ 显式拒绝。探测经 RuntimeVolumeInspection
// 扩展接口（载体名公式 Provider 私有）；面缺席时契约测试跳过（Inspector 降级文化
// ——两在册 Runtime 均实现，假底座按测试面选配）。卷行读取失败如实上抛
// （fail-closed：契约测试本身不可静默失明）。
func (e *Engine) GuardVolumeCarriers(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, projectID string) error {
	probe := capability.FacesOf(e.runtime).VolumeInspection
	if probe == nil {
		return nil // 面缺席：降级跳过（见注释）
	}
	seen := map[string]bool{}
	for _, w := range ws {
		for _, v := range w.Volumes {
			if seen[v.VolumeID] {
				continue
			}
			seen[v.VolumeID] = true
			row, err := e.repos.Volumes.Get(ctx, v.VolumeID)
			if err != nil {
				return fmt.Errorf("adoption guard: resolve volume %q: %w", v.VolumeID, err)
			}
			current, err := probe.VolumeCarrierExists(ctx, ns, row.ID)
			if err != nil {
				return fmt.Errorf("adoption guard: probe carrier for volume %q: %w", row.Name, err)
			}
			if current {
				continue // 新键在场：现行形态
			}
			legacy, err := probe.VolumeCarrierExists(ctx, ns, row.Name)
			if err != nil {
				return fmt.Errorf("adoption guard: probe legacy carrier for volume %q: %w", row.Name, err)
			}
			if legacy {
				return fmt.Errorf("%w: volume %q still lives in its pre-ADR-0097 name-keyed carrier (docker volumes cannot be renamed); move the data to a volume created after the upgrade, or remove the runtime-side carrier and redeploy (fresh volume)",
					ErrLegacyVolumeCarrier, row.Name)
			}
		}
	}
	return nil
}
