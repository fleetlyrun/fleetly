package fleetlygrpc

// InvitationsService：一次性邀请流（F0.5）。AcceptInvitation 是 PUBLIC 档
//——邀请 Token 自身就是凭证（被邀请者尚无平台 Token）。时窗内单次有效：
// 条件更新（consumed_at 为空才命中）+ 同事务建 user + membership。

import (
	"context"
	"database/sql"
	"time"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/invitation"
	"github.com/fleetlyrun/fleetly/internal/state/membership"
	"github.com/fleetlyrun/fleetly/internal/state/user"
)

type InvitationsService struct {
	identityv1.UnimplementedInvitationsServiceServer
	s *Services
}

// defaultInvitationTTL / maxInvitationTTL 是邀请时窗裁决（24h 缺省、168h
// 上限——跨时区小团队短了误事、长了失管）。
const (
	defaultInvitationTTL = 24 * time.Hour
	maxInvitationTTL     = 168 * time.Hour
)

func (svc *InvitationsService) CreateInvitation(ctx context.Context, req *identityv1.CreateInvitationRequest) (*identityv1.CreateInvitationResponse, error) {
	if req.GetRoleId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "role_id: must not be empty")
	}
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
	if teamID == "" {
		teamID = identity.DefaultTeamID
	}
	material, err := identity.NewInvitation()
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "invitation material generation failed")
	}
	inv := &invitation.Invitation{
		ID: newID(), TokenSHA256: material.SHA256, TeamID: teamID, RoleID: req.GetRoleId(),
		CreatedBy: authn.ActorFromContext(ctx),
		ExpiresAt: stateFormatTime(svc.s.DB.Clock().Now().Add(ttl)),
	}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.teamExists(teamID), svc.s.roleExists(req.GetRoleId())},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Invitations.Create(ctx, tx, inv)
		},
		events: []eventFact{identityEvent("invitation.created", "invitation", inv.ID, invitationCreatedPayload{Role: inv.RoleID})},
		audits: []*audit.Entry{identityAudit(ctx, "invitation.create", "invitation/"+inv.ID, "", inv.ExpiresAt)},
	})
	if err != nil {
		return nil, mapStateError(err, "invitation")
	}
	return &identityv1.CreateInvitationResponse{Invitation: invitationMsg(inv), Secret: material.Secret}, nil
}

func (svc *InvitationsService) ListInvitations(ctx context.Context, _ *identityv1.ListInvitationsRequest) (*identityv1.ListInvitationsResponse, error) {
	list, err := svc.s.Invitations.List(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "invitation")
	}
	out := &identityv1.ListInvitationsResponse{}
	for i := range list {
		out.Invitations = append(out.Invitations, invitationMsg(&list[i]))
	}
	return out, nil
}

// AcceptInvitation 兑换邀请：sha256 查表 → 时窗/单次校验 → 建用户 + 授
// 角色（单事务；MarkConsumed 条件更新保证并发双兑只有一个成功）。
// 审计来源是 manual（持信物的人当面操作；无 gRPC 凭证语境）。
func (svc *InvitationsService) AcceptInvitation(ctx context.Context, req *identityv1.AcceptInvitationRequest) (*identityv1.AcceptInvitationResponse, error) {
	if req.GetSecret() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "secret: must not be empty")
	}
	if req.GetUserName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "user_name: must not be empty")
	}
	if identity.TokenKind(req.GetSecret()) != "invitation" {
		return nil, invalidInvitation("invitation token required")
	}
	inv, err := svc.s.Invitations.GetBySHA256(ctx, svc.s.DB.Runner(), identity.HashToken(req.GetSecret()))
	if err != nil {
		return nil, invalidInvitation("invitation not found")
	}
	if inv.ConsumedAt != "" {
		return nil, invalidInvitation("invitation already used")
	}
	expires, err := time.Parse(time.RFC3339, inv.ExpiresAt)
	if err != nil || !svc.s.DB.Clock().Now().Before(expires) {
		return nil, invalidInvitation("invitation expired")
	}

	u := &user.User{ID: newID(), Name: req.GetUserName()}
	m := &membership.Membership{ID: newID(), UserID: u.ID, TeamID: inv.TeamID, RoleID: inv.RoleID}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			if err := svc.s.Users.Create(ctx, tx, u); err != nil {
				return err
			}
			if err := svc.s.Memberships.Create(ctx, tx, m); err != nil {
				return err
			}
			return svc.s.Invitations.MarkConsumed(ctx, tx, inv.ID)
		},
		events: []eventFact{identityEvent("invitation.accepted", "invitation", inv.ID, invitationAcceptedPayload{User: u.Name})},
		audits: []*audit.Entry{{
			ID: newID(), Actor: "user:" + u.Name, Source: audit.SourceManual,
			Action: "invitation.accept", Resource: "invitation/" + inv.ID,
			AfterFP: u.Name,
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "invitation")
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
