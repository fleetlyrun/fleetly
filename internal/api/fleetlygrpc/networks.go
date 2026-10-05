package fleetlygrpc

// Networks 上下文服务实现（Network 聚合 CRUD + 跨 Project peer 声明面 +
// 网络重建维护动词，ADR-0013 附录 A / ADR-0046）。从 structure.go 拆出
//（sizeguard 1200 行预算——F3.1 写面前的既定拆分序，N2 评审 P3 注记）。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/networkpeer"
)

// ---- Networks ----

type NetworksService struct {
	structurev1.UnimplementedNetworksServiceServer
	s *Services
}

func (svc *NetworksService) CreateNetwork(ctx context.Context, req *structurev1.CreateNetworkRequest) (*structurev1.CreateNetworkResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	row := &networkrepo.Network{
		ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(), EgressNone: req.GetEgressNone(),
	}
	// 父资源存活校验（批 0 复核，同族面）：Project 级材料不得落在
	// 不存在/已删的 Project 下。
	err := svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.parentProjectAlive(req.GetProjectId())},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Networks.Create(ctx, tx, row)
		},
		events: []eventFact{structureEvent(eventNetworkCreated, "network", row.Name, row.ProjectID)},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "network.create",
			Resource: "network/" + row.Name, AfterFP: req.String(),
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	return &structurev1.CreateNetworkResponse{Network: networkMsg(*row)}, nil
}

func (svc *NetworksService) ListNetworks(ctx context.Context, req *structurev1.ListNetworksRequest) (*structurev1.ListNetworksResponse, error) {
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	list, err := svc.s.Networks.ListByProjectPage(ctx, svc.s.DB.Runner(),
		req.GetProjectId(), req.GetAfterName(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	out := &structurev1.ListNetworksResponse{}
	for _, row := range list {
		out.Networks = append(out.Networks, networkMsg(row))
	}
	return out, nil
}

// networkRebuiltEventPayload 是 network.rebuilt 的载荷（字段只增）。
type networkRebuiltEventPayload struct {
	ProjectID  string `json:"project_id"`
	Network    string `json:"network"`
	Detached   int32  `json:"detached"`
	Reattached int32  `json:"reattached"`
}

// RebuildNetwork 是网络重建维护动词（ADR-0046）：engine 同步执行（detach
// 排水 + 复建 + re-attach 的分钟级受维护窗），成功后事件 + 审计一事务。
// 失败无事件（重试风暴自放大防护），错误信封携带 engine 的中断点与
// 收敛指引；外来附着拒绝（E_CONFLICT）列出载体名。
func (svc *NetworksService) RebuildNetwork(ctx context.Context, req *structurev1.RebuildNetworkRequest) (*structurev1.RebuildNetworkResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	res, err := svc.s.Engine.RebuildNetwork(ctx, req.GetProjectId(), req.GetName())
	if err != nil {
		return nil, mapRebuildError(err)
	}
	net, err := svc.s.Networks.GetByName(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetName())
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	payload, _ := json.Marshal(networkRebuiltEventPayload{ //nolint:errcheck // 结构体字段恒可序列化
		ProjectID: net.ProjectID, Network: net.Name,
		Detached: int32(res.Detached), Reattached: int32(res.Reattached), //nolint:gosec // 计数域内（载体数）
	})
	err = svc.s.commit(ctx, writeFact{
		events: []eventFact{{name: eventNetworkRebuilt, aggregate: "network", id: net.ID, payload: payload}},
		audits: []*audit.Entry{{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "network.rebuild",
			Resource: "network/" + net.Name,
			AfterFP:  fmt.Sprintf("project=%s detached=%d reattached=%d", net.ProjectID, res.Detached, res.Reattached),
		}},
	})
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	return &structurev1.RebuildNetworkResponse{
		Network: networkMsg(*net), Detached: int32(res.Detached), Reattached: int32(res.Reattached), //nolint:gosec // 计数域内
	}, nil
}

// mapRebuildError 把重建执行错误映射为诚实信封：NotFound → 404；外来
// 附着 → E_CONFLICT（带载体列表与处置建议）；其余 → E_INTERNAL 带因。
func mapRebuildError(err error) error {
	if errors.Is(err, state.ErrNotFound) {
		return apperr.New("E_NOT_FOUND", "%s", err.Error())
	}
	var foreign *engine.ForeignAttachmentError
	if errors.As(err, &foreign) {
		return apperr.New("E_CONFLICT", "%s", foreign.Error()).
			WithSuggestion("The rebuild refuses to touch carriers it cannot attribute. Remove the listed attachments (or restore their platform rows), then retry the rebuild.")
	}
	return apperr.New("E_INTERNAL", "network rebuild failed: %s", err.Error()).WithCause(err)
}

// ---- Network Peers（跨 Project 挂靠声明，ADR-0013 附录 A.1） ----
// ---- Network Peers（跨 Project 挂靠声明，ADR-0013 附录 A.1） ----

// networkPeerEventPayload 是 peer 三拍事件的载荷（字段只增）。
type networkPeerEventPayload struct {
	ID               string `json:"id"`
	NetworkID        string `json:"network_id"`
	NetworkName      string `json:"network_name"`
	NetworkProjectID string `json:"network_project_id"`
	PeerProjectID    string `json:"peer_project_id"`
}

// networkPeerEvent 构造 peer 事件事实（aggregate=network，id=网络行）。
func networkPeerEvent(name string, p *networkpeer.Peer, net *networkrepo.Network) eventFact {
	payload, _ := json.Marshal(networkPeerEventPayload{ //nolint:errcheck // 结构体字段恒可序列化
		ID: p.ID, NetworkID: p.NetworkID, NetworkName: net.Name,
		NetworkProjectID: net.ProjectID, PeerProjectID: p.PeerProjectID,
	})
	return eventFact{name: name, aggregate: "network", id: p.NetworkID, payload: payload}
}

// networkPeerAudits 是双方审计（ADR-0013：挂靠计入两侧）：网络侧 +
// 挂靠项目侧各一行，action 同名（ListAudit 的 action/resource 过滤面
// 两侧各自可答"谁挂在我的网上/我挂在哪里"）。
func networkPeerAudits(ctx context.Context, action string, p *networkpeer.Peer, net *networkrepo.Network) []*audit.Entry {
	fp := fmt.Sprintf("peer_project=%s network=%s state=%s", p.PeerProjectID, net.Name, p.State)
	mk := func(resource string) *audit.Entry {
		return &audit.Entry{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
			Action: action, Resource: resource, AfterFP: fp,
		}
	}
	return []*audit.Entry{
		mk("network/" + net.Name + "/peers/" + p.ID),
		mk("project/" + p.PeerProjectID + "/peers/" + p.ID),
	}
}

// loadPeerNetwork 读 peer 声明指向的网络行（活跃口径；A.1：批准面只对
// 活跃网络有意义——已删网络的挂靠无接收方）。
func (svc *NetworksService) loadPeerNetwork(ctx context.Context, networkID string) (*networkrepo.Network, error) {
	net, err := svc.s.Networks.GetByID(ctx, svc.s.DB.Runner(), networkID)
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	return net, nil
}

// requirePeerReceiverTeam 校验调用方归属于接收方团队（ADR-0013：网络
// peer 的批准权在网络归属项目所属团队；ADR-0035 owner 豁免）。调用方身份
// 从 authn ctx 取（Token 的 Team 轴）；网络归属项目的 Team 实取自 project
// 行。不等即 E_FORBIDDEN——P1 修复前按行 ID 直批不比对归属，任意项目的
// 持有者可单向自助批准跨项目挂靠（越权网络接入）。declare 侧不动：挂靠方
// 本就任意项目发起（声明≠批准；归属校验在挂靠方自己的 Project 上）。
func (svc *NetworksService) requirePeerReceiverTeam(ctx context.Context, net *networkrepo.Network) error {
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return err
	}
	if owner {
		return nil
	}
	proj, err := svc.s.Projects.Get(ctx, svc.s.DB.Runner(), net.ProjectID)
	if err != nil {
		return mapStateError(err, "project")
	}
	if proj.Deleted() {
		return apperr.New("E_NOT_FOUND", "project %s not found", net.ProjectID)
	}
	if proj.TeamID != teamID {
		return apperr.New("E_FORBIDDEN",
			"only a token of team %s (owning the network's project) can approve this peer declaration", proj.TeamID).
			WithSuggestion("Ask the receiving project's team to approve the declaration, or revoke it.")
	}
	return nil
}

// authorizePeerSide 校验调用方属于声明任一侧的 Team（ADR-0035）：网络归属
// 方（receiver）或挂靠方（peer project）都可视/撤声明——声明是双边事实。
// 网络行已删时只按挂靠方判定（撤销语义本就覆盖"接收网络已逝"）。
func (svc *NetworksService) authorizePeerSide(ctx context.Context, p *networkpeer.Peer) error {
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return err
	}
	if owner {
		return nil
	}
	if net, nerr := svc.s.Networks.GetByID(ctx, svc.s.DB.Runner(), p.NetworkID); nerr == nil {
		if proj, perr := svc.s.Projects.Get(ctx, svc.s.DB.Runner(), net.ProjectID); perr == nil && !proj.Deleted() && proj.TeamID == teamID {
			return nil
		}
	}
	if err := svc.s.authorizeProjectID(ctx, p.PeerProjectID); err == nil {
		return nil
	}
	return apperr.New("E_FORBIDDEN",
		"peer declaration %s belongs to another team; only a token of either side's team can view or revoke it", p.ID).
		WithSuggestion("Check the declaration id with the team that created it.")
}

// DeclareNetworkPeer 是挂靠方声明（pending 行）：peer 项目请求挂靠目标
// 网络。双向声明的第一拍；批准前引用不可投影（strict fail-closed）。
func (svc *NetworksService) DeclareNetworkPeer(ctx context.Context, req *structurev1.DeclareNetworkPeerRequest) (*structurev1.DeclareNetworkPeerResponse, error) {
	if req.GetNetworkId() == "" || req.GetPeerProjectId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "network_id and peer_project_id: must not be empty")
	}
	net, err := svc.loadPeerNetwork(ctx, req.GetNetworkId())
	if err != nil {
		return nil, err
	}
	if net.ProjectID == req.GetPeerProjectId() {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"peer_project_id %s owns network %q; same-project attachment uses the project-local network name",
			req.GetPeerProjectId(), net.Name)
	}
	// 行级授权（ADR-0035）：挂靠方发起声明——Peer Project 必须归属调用方
	// Team（receiver 侧由 Approve 的 requirePeerReceiverTeam 把守）。
	if err := svc.s.authorizeProjectID(ctx, req.GetPeerProjectId()); err != nil {
		return nil, err
	}
	p := &networkpeer.Peer{ID: newID(), NetworkID: net.ID, PeerProjectID: req.GetPeerProjectId()}
	err = svc.s.commit(ctx, writeFact{
		// 双侧父资源存活（事务内）：网络归属方与挂靠方项目都必须在场。
		checks: []acceptanceCheck{
			svc.s.parentProjectAlive(net.ProjectID),
			svc.s.parentProjectAlive(req.GetPeerProjectId()),
		},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.NetworkPeers.Create(ctx, tx, p)
		},
		events: []eventFact{networkPeerEvent(eventNetworkPeerDeclared, p, net)},
		audits: networkPeerAudits(ctx, "network.peer_declare", p, net),
	})
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	return &structurev1.DeclareNetworkPeerResponse{Peer: networkPeerMsg(p, net)}, nil
}

// ApproveNetworkPeer 是接收方批准（pending → approved）。已批准幂等返回；
// 已撤销的行不可复活（重新挂靠走新声明）。批准前校验调用方归属于接收方
// 团队（ADR-0013 语义执法，安全批 P1）——任意团队不得单向自助批准。
func (svc *NetworksService) ApproveNetworkPeer(ctx context.Context, req *structurev1.ApproveNetworkPeerRequest) (*structurev1.ApproveNetworkPeerResponse, error) {
	p, err := svc.s.NetworkPeers.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	net, err := svc.loadPeerNetwork(ctx, p.NetworkID)
	if err != nil {
		return nil, err
	}
	if err := svc.requirePeerReceiverTeam(ctx, net); err != nil {
		return nil, err
	}
	if p.State == networkpeer.StateApproved {
		return &structurev1.ApproveNetworkPeerResponse{Peer: networkPeerMsg(p, net)}, nil // 幂等
	}
	if p.State == networkpeer.StateRevoked {
		return nil, apperr.New("E_CONFLICT",
			"peer declaration %s was revoked; declare a new one to re-attach", p.ID)
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.NetworkPeers.Approve(ctx, tx, p.ID)
		},
		events: []eventFact{networkPeerEvent(eventNetworkPeerApproved, p, net)},
		audits: networkPeerAudits(ctx, "network.peer_approve", p, net),
	})
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	fresh, gerr := svc.s.NetworkPeers.Get(ctx, svc.s.DB.Runner(), p.ID)
	if gerr != nil {
		return nil, mapStateError(gerr, "network peer")
	}
	return &structurev1.ApproveNetworkPeerResponse{Peer: networkPeerMsg(fresh, net)}, nil
}

// RevokeNetworkPeer 撤销（任一侧；幂等）：即时隔离——撤销落账后引擎对
// 受影响 App isolate 重收敛剥离附件（断存量，A.4）。剥离失败不回滚撤销
// （受理面已生效），漂移扫描拍兜底重收敛。
func (svc *NetworksService) RevokeNetworkPeer(ctx context.Context, req *structurev1.RevokeNetworkPeerRequest) (*structurev1.RevokeNetworkPeerResponse, error) {
	p, err := svc.s.NetworkPeers.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	// 行级授权（ADR-0035）：撤销是任一侧的权利——双边归属判定。
	if err := svc.authorizePeerSide(ctx, p); err != nil {
		return nil, err
	}
	if p.State == networkpeer.StateRevoked {
		return &structurev1.RevokeNetworkPeerResponse{Peer: networkPeerMsg(p, nil)}, nil // 幂等
	}
	// 网络行可能已删（先删网络后撤声明的次序）：审计名缺失不阻断撤销——
	// 撤销的语义就是摘除挂靠，接收网络在不在都成立。
	net := &networkrepo.Network{ID: p.NetworkID}
	if active, nerr := svc.s.Networks.GetByID(ctx, svc.s.DB.Runner(), p.NetworkID); nerr == nil {
		net = active
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.NetworkPeers.Revoke(ctx, tx, p.ID)
		},
		events: []eventFact{networkPeerEvent(eventNetworkPeerRevoked, p, net)},
		audits: networkPeerAudits(ctx, "network.peer_revoke", p, net),
	})
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	// 即时隔离（A.4）：撤销已生效，剥离是收敛动作——失败记日志，漂移拍自愈。
	if serr := svc.s.Engine.IsolateNetworkPeer(ctx, p.NetworkID, p.PeerProjectID); serr != nil {
		if svc.s.Log != nil {
			svc.s.Log.Warn("network peer revoke: isolation reconverge deferred to the drift tick", "peer", p.ID, "err", serr)
		}
	}
	fresh, gerr := svc.s.NetworkPeers.Get(ctx, svc.s.DB.Runner(), p.ID)
	if gerr != nil {
		return nil, mapStateError(gerr, "network peer")
	}
	return &structurev1.RevokeNetworkPeerResponse{Peer: networkPeerMsg(fresh, net)}, nil
}

func (svc *NetworksService) GetNetworkPeer(ctx context.Context, req *structurev1.GetNetworkPeerRequest) (*structurev1.GetNetworkPeerResponse, error) {
	p, err := svc.s.NetworkPeers.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	if err := svc.authorizePeerSide(ctx, p); err != nil {
		return nil, err
	}
	return &structurev1.GetNetworkPeerResponse{Peer: networkPeerMsg(p, svc.peerNetProjection(ctx, p))}, nil
}

// ListNetworkPeers 新→旧分页（ADR-0026 after_* + limit）。network_id /
// peer_project_id 过滤可选（接收方与挂靠方两侧视图同面）。行级过滤
// （ADR-0035）：非 owner 只见本 Team 任一侧在场的声明。
func (svc *NetworksService) ListNetworkPeers(ctx context.Context, req *structurev1.ListNetworkPeersRequest) (*structurev1.ListNetworkPeersResponse, error) {
	list, err := svc.s.NetworkPeers.List(ctx, svc.s.DB.Runner(),
		req.GetNetworkId(), req.GetPeerProjectId(), req.GetAfterPeerId(), int(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	out := &structurev1.ListNetworkPeersResponse{}
	for i := range list {
		p := &list[i]
		if !svc.peerVisibleToCaller(ctx, p) {
			continue
		}
		out.Peers = append(out.Peers, networkPeerMsg(p, svc.peerNetProjection(ctx, p)))
	}
	return out, nil
}

// peerVisibleToCaller 是 List 面的单行归属判定（owner 恒可见；网络已删的
// 行按挂靠方一侧判定）。判定失败按不可见处理——List 面不因解析故障放大
// 为整页错误（行级执法 fail-closed：未见即拒）。
func (svc *NetworksService) peerVisibleToCaller(ctx context.Context, p *networkpeer.Peer) bool {
	return svc.authorizePeerSide(ctx, p) == nil
}

// peerNetProjection 读声明指向的网络行（读投影便捷字段；行已删 → 空值，
// 不阻断列表）。
func (svc *NetworksService) peerNetProjection(ctx context.Context, p *networkpeer.Peer) *networkrepo.Network {
	net, err := svc.s.Networks.GetByID(ctx, svc.s.DB.Runner(), p.NetworkID)
	if err != nil {
		return &networkrepo.Network{ID: p.NetworkID}
	}
	return net
}

// networkPeerMsg 行 → proto 映射（网络侧便捷字段来自读投影）。
func networkPeerMsg(p *networkpeer.Peer, net *networkrepo.Network) *structurev1.NetworkPeer {
	msg := &structurev1.NetworkPeer{
		Id: p.ID, PeerProjectId: p.PeerProjectID, State: string(p.State),
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, ApprovedAt: p.ApprovedAt,
	}
	if net != nil {
		msg.NetworkId = p.NetworkID
		msg.NetworkName = net.Name
		msg.NetworkProjectId = net.ProjectID
	}
	return msg
}
