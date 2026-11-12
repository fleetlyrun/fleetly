package identitygrpc

import (
	"context"
	"time"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/audit"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/invitation"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/membership"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/user"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// InvitationsService 满足 identityv1.InvitationsServiceServer。
type InvitationsService struct {
	identityv1.UnimplementedInvitationsServiceServer
	deps Deps
}

// InvitationCreatedPayload 是 invitation.created 的载荷（绑定的角色）。
type InvitationCreatedPayload struct {
	Role string `json:"role"`
}

// InvitationAcceptedPayload 是 invitation.accepted 的载荷（兑取成为的用户名）。
type InvitationAcceptedPayload struct {
	User string `json:"user"`
}

// defaultInvitationTTL / maxInvitationTTL 是邀请时窗裁决（24h 缺省、168h
// 上限——跨时区小团队短了误事、长了失管）。
const (
	defaultInvitationTTL = 24 * time.Hour
	maxInvitationTTL     = 168 * time.Hour
)

func (svc *InvitationsService) CreateInvitation(ctx context.Context, req *identityv1.CreateInvitationRequest) (*identityv1.CreateInvitationResponse, error) {
	// role_id 必填检查已退役为 CreateInvitationRequest 的 protovalidate CEL
	// 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler 之前执法。ttl 的
	// 格式/范围检查（动态文案）留在服务内。
	ttl := defaultInvitationTTL
	if s := req.GetTtl(); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return nil, apperr.New("E_INVALID_ARGUMENT", "ttl: must be a positive duration like \"24h\"")
		}
		if d > maxInvitationTTL {
			return nil, apperr.New("E_INVALID_ARGUMENT", "ttl: must not exceed %s", maxInvitationTTL)
		}
		ttl = d
	}
	teamID := req.GetTeamId()
	// Team 轴（ADR-0035 决策 5）：邀请入队的目标 Team 缺省 = 调用方 Team；
	// 显式他队目标仅平台 owner 可（跨队邀请 = 跨队 membership 提权面；
	// R14-M4：缺省交 resolveTargetTeam 解析为调用方 Team）。
	resolvedTeam, err := grpcapi.ResolveTargetTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	teamID = resolvedTeam
	material, err := identity.NewInvitation()
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "invitation material generation failed").WithCause(err)
	}
	inv := &invitation.Invitation{
		TokenSHA256: material.SHA256, TeamID: teamID, RoleID: req.GetRoleId(),
		CreatedBy: authn.ActorFromContext(ctx),
		ExpiresAt: stateFormatTime(svc.deps.Clock.Now().Add(ttl)),
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		// roleInTeam（Q-16 同族面）：邀请绑 team+role——不一致的邀请兑换出
		// 跨 Team membership，同洞同修。roleGrant：角色授予特权上限
		// （R08-H1 同根）——邀请是延迟授予通道，上限必须在建邀请时判定。
		Checks: []grpcapi.Check{
			svc.deps.teamExists(teamID), svc.deps.roleInTeam(req.GetRoleId(), teamID), svc.deps.roleGrant(req.GetRoleId()),
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Invitations.Create(ctx, inv)
		},
		Facts: []func() writes.FactRow{
			// 资源轴（P0-2）：邀请随目标 Team 落队。
			func() writes.FactRow {
				return writes.FactRow{
					Name: "invitation.created", Aggregate: "invitation", ID: inv.ID,
					Payload: grpcapi.MustJSON(InvitationCreatedPayload{Role: inv.RoleID}),
					TeamID:  inv.TeamID,
					Action:  "invitation.create", Resource: "invitation/" + inv.ID, AfterFP: inv.ExpiresAt,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				}
			},
		},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "invitation")
	}
	return &identityv1.CreateInvitationResponse{Invitation: invitationMsg(inv), Secret: material.Secret}, nil
}

// ListInvitations 非 owner 按 Team 过滤（ADR-0035：邀请密文哈希与目标队
// 是治理目录面）。
func (svc *InvitationsService) ListInvitations(ctx context.Context, _ *identityv1.ListInvitationsRequest) (*identityv1.ListInvitationsResponse, error) {
	teamID, owner, err := grpcapi.CallerTeam(ctx)
	if err != nil {
		return nil, err
	}
	list, err := svc.deps.Invitations.List(ctx)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "invitation")
	}
	out := &identityv1.ListInvitationsResponse{}
	for i := range list {
		if !owner && list[i].TeamID != teamID {
			continue
		}
		out.Invitations = append(out.Invitations, invitationMsg(&list[i]))
	}
	return out, nil
}

// AcceptInvitation 兑换邀请：sha256 查表 → 时窗/单次校验 → 建用户 + 授
// 角色（单事务；MarkConsumed 条件更新保证并发双兑只有一个成功）。
// 审计来源是 manual（持信物的人当面操作；无 gRPC 凭证语境）。
func (svc *InvitationsService) AcceptInvitation(ctx context.Context, req *identityv1.AcceptInvitationRequest) (*identityv1.AcceptInvitationResponse, error) {
	// secret/user_name 必填组合检查已退役为 AcceptInvitationRequest 的
	// protovalidate CEL 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler
	// 之前执法，拆 per-field 文案。信物有效性检查（统一
	// E_INVALID_INVITATION，防枚举探测）留在服务内。
	if identity.TokenKind(req.GetSecret()) != "invitation" {
		return nil, invalidInvitation("invitation token required")
	}
	inv, err := svc.deps.Invitations.GetBySHA256(ctx, identity.HashToken(req.GetSecret()))
	if err != nil {
		return nil, invalidInvitation("invitation not found")
	}
	if inv.ConsumedAt != "" {
		return nil, invalidInvitation("invitation already used")
	}
	expires, err := time.Parse(time.RFC3339, inv.ExpiresAt)
	if err != nil || !svc.deps.Clock.Now().Before(expires) {
		return nil, invalidInvitation("invitation expired")
	}

	u := &user.User{Name: req.GetUserName()}
	m := &membership.Membership{UserID: u.ID, TeamID: inv.TeamID, RoleID: inv.RoleID}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			if err := st.Users.Create(ctx, u); err != nil {
				return err
			}
			// membership 归属事务内铸出的用户 id（ADR-0070）。
			m.UserID = u.ID
			if err := st.Memberships.Create(ctx, m); err != nil {
				return err
			}
			return st.Invitations.MarkConsumed(ctx, inv.ID)
		},
		Facts: []func() writes.FactRow{
			// 资源轴（P0-2）；审计来源是 manual（持信物的人当面操作；无
			// gRPC 凭证语境）。
			func() writes.FactRow {
				return writes.FactRow{
					Name: "invitation.accepted", Aggregate: "invitation", ID: inv.ID,
					Payload: grpcapi.MustJSON(InvitationAcceptedPayload{User: u.Name}),
					TeamID:  inv.TeamID,
					Action:  "invitation.accept", Resource: "invitation/" + inv.ID,
					AfterFP: u.Name,
					Actor:   "user:" + u.Name, Source: audit.SourceManual,
				}
			},
		},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "invitation")
	}
	return &identityv1.AcceptInvitationResponse{User: userMsg(u)}, nil
}

// invalidInvitation 是邀请兑换失败的统一信封（不区分"不存在/已用/过期"
// 的细节给匿名面——避免枚举探测；提示语只说无效）。
func invalidInvitation(reason string) error {
	e := apperr.New("E_INVALID_INVITATION", "invitation is invalid or expired")
	return e.WithContext("reason", reason)
}

// stateFormatTime 与 state.FormatTime 同形态（包内直通避免多一层 import
// 面扩散——identity 服务统一经本函数落时间戳）。
func stateFormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }
