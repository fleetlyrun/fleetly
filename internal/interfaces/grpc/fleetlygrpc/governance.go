package fleetlygrpc

// Governance 上下文服务实现（F1.9，ADR-0017 附录 A.3）：Change Freeze 的
// 生命周期面（set/lift/list）。执法在 governance 拦截器（authn 之后、幂等
// 执法器之前）——本服务只拥有冻结窗行；写路径走受理位统一写原语（四件
// 一拍：freeze.set / freeze.lifted 事件 + 审计）。

import (
	"context"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/freeze"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
)

// freeze 事件名锚定（usage 反扫的字面量命中点）。
const (
	eventFreezeSet    = "freeze.set"
	eventFreezeLifted = "freeze.lifted"
)

type GovernanceService struct {
	systemv1.UnimplementedGovernanceServiceServer
	s *Services
}

// freezeEventPayload 是 freeze.set / freeze.lifted 的载荷（字段只增）。
type freezeEventPayload struct {
	ID     string `json:"id"`
	TeamID string `json:"team_id"` // 空串 = 全局冻结
	Reason string `json:"reason"`
}

// SetChangeFreeze 落一条活跃冻结（team_id 空串 = 全局；reason 必填——拒绝
// 信封回带给调用方）。同 scope 已有活跃冻结 → E_ALREADY_EXISTS（先 lift）。
// Team 轴（ADR-0035 决策 7）：team 冻结限本队；全局冻结（空 team_id）是
// 跨 Team 平台动作，仅平台 owner 可设。
func (svc *GovernanceService) SetChangeFreeze(ctx context.Context, req *systemv1.SetChangeFreezeRequest) (*systemv1.SetChangeFreezeResponse, error) {
	// reason 必填检查已退役为 SetChangeFreezeRequest 的 protovalidate CEL
	// 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return nil, err
	}
	target := req.GetTeamId()
	if target == "" {
		if !owner {
			return nil, apperr.New("E_FORBIDDEN",
				"a global (all-team) change freeze is a platform action; only a platform owner token can set one").
				WithSuggestion("Set a team-scoped freeze (team_id of your team) instead, or ask the platform owner.")
		}
	} else if target != teamID && !owner {
		return nil, errTeamTargetMismatch(target, teamID)
	}
	row := &freeze.Freeze{
		TeamID: target, Reason: req.GetReason(),
		CreatedBy: authn.ActorFromContext(ctx),
	}
	checks := []acceptanceCheck{}
	if row.TeamID != "" {
		checks = append(checks, svc.s.teamExists(row.TeamID))
	}
	err = svc.s.commit(ctx, writeFact{
		checks: checks,
		write: func(ctx context.Context, st transact.Stores) error {
			return st.Freezes.Create(ctx, row)
		},
		facts: []func() writes.FactRow{
			// 资源轴（P0-2）：全局冻结 TeamID='' → 平台级可见。
			func() writes.FactRow {
				return writes.FactRow{
					Name: eventFreezeSet, Aggregate: "freeze", ID: row.ID,
					Payload: mustJSON(freezeEventPayload{ID: row.ID, TeamID: row.TeamID, Reason: row.Reason}),
					TeamID:  row.TeamID,
					Action:  "freeze.set", Resource: "freeze/" + row.ID,
					AfterFP: row.TeamID + ":" + row.Reason,
					Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			},
		},
	})
	if err != nil {
		return nil, mapStateError(err, "freeze")
	}
	return &systemv1.SetChangeFreezeResponse{Freeze: freezeMsg(row)}, nil
}

// LiftChangeFreeze 解除冻结（落 lifted_at；幂等：已 lift 再 lift 成功返回
// 历史行）。行级授权（ADR-0035 决策 7）：本队行可解；全局行（team_id 空）
// 仅平台 owner。
func (svc *GovernanceService) LiftChangeFreeze(ctx context.Context, req *systemv1.LiftChangeFreezeRequest) (*systemv1.LiftChangeFreezeResponse, error) {
	row, err := svc.s.Freezes.Get(ctx, req.GetId())
	if err != nil {
		return nil, mapStateError(err, "freeze")
	}
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return nil, err
	}
	if !owner && row.TeamID != teamID {
		return nil, apperr.New("E_FORBIDDEN",
			"freeze %s covers team %q; only that team's tokens (or a platform owner) can lift it", row.ID, row.TeamID)
	}
	if row.Active() {
		err = svc.s.commit(ctx, writeFact{
			write: func(ctx context.Context, st transact.Stores) error {
				return st.Freezes.Lift(ctx, row.ID)
			},
			facts: []func() writes.FactRow{
				// 资源轴（P0-2）：全局冻结 TeamID='' → 平台级可见。
				func() writes.FactRow {
					return writes.FactRow{
						Name: eventFreezeLifted, Aggregate: "freeze", ID: row.ID,
						Payload: mustJSON(freezeEventPayload{ID: row.ID, TeamID: row.TeamID, Reason: row.Reason}),
						TeamID:  row.TeamID,
						Action:  "freeze.lift", Resource: "freeze/" + row.ID,
						AfterFP: row.TeamID + ":" + row.Reason,
						Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					}
				},
			},
		})
		if err != nil {
			return nil, mapStateError(err, "freeze")
		}
		row, err = svc.s.Freezes.Get(ctx, req.GetId())
		if err != nil {
			return nil, mapStateError(err, "freeze")
		}
	}
	return &systemv1.LiftChangeFreezeResponse{Freeze: freezeMsg(row)}, nil
}

// ListChangeFreezes 列冻结行（含历史；after_* + limit 惯例）。非 owner 只
// 见本队行 + 全局行（全局冻结影响调用方，reason 需可读；ADR-0035 决策 7）。
func (svc *GovernanceService) ListChangeFreezes(ctx context.Context, req *systemv1.ListChangeFreezesRequest) (*systemv1.ListChangeFreezesResponse, error) {
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := svc.s.Freezes.List(ctx, req.GetAfterFreezeSeq(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "freeze")
	}
	out := &systemv1.ListChangeFreezesResponse{}
	for i := range rows {
		if !owner && rows[i].TeamID != teamID && rows[i].TeamID != "" {
			continue
		}
		out.Freezes = append(out.Freezes, freezeMsg(&rows[i]))
	}
	return out, nil
}

// freezeMsg 把 Freeze 行投影为 proto 消息。
func freezeMsg(f *freeze.Freeze) *systemv1.ChangeFreeze {
	return &systemv1.ChangeFreeze{
		Id: f.ID, Seq: f.Seq, TeamId: f.TeamID, Reason: f.Reason,
		CreatedBy: f.CreatedBy, CreatedAt: f.CreatedAt, LiftedAt: f.LiftedAt,
	}
}
