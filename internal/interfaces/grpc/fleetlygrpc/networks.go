package fleetlygrpc

// Networks 上下文服务实现（Network 聚合 CRUD + 跨 Project peer 声明面 +
// 网络重建维护动词，ADR-0013 附录 A / ADR-0046）。从 structure.go 拆出
//（sizeguard 1200 行预算——F3.1 写面前的既定拆分序，N2 评审 P3 注记）。

import (
	"context"
	"errors"
	"fmt"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/networkpeer"
)

// ---- Networks ----

type NetworksService struct {
	structurev1.UnimplementedNetworksServiceServer
	s *Services
}

func (svc *NetworksService) CreateNetwork(ctx context.Context, req *structurev1.CreateNetworkRequest) (*structurev1.CreateNetworkResponse, error) {
	// name 的 dns 标签形态检查（validName）与 project_id/name 必填组合检查
	// 已退役为 CreateNetworkRequest 的 protovalidate CEL 注解（P4-1 批 1
	// Run B1）：validate 拦截器在 handler 之前执法，组合必填拆 per-field
	// 文案；正则字面单源见 entityid.NamePatternSource（descriptor 对齐
	// 测试执法）。
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	row := &network.Network{
		ProjectID: req.GetProjectId(), Name: req.GetName(), EgressNone: req.GetEgressNone(),
	}
	// 父资源存活校验（批 0 复核，同族面）：Project 级材料不得落在
	// 不存在/已删的 Project 下。事件归属进事务前解析（R08-M8）。
	fact, err := svc.s.factOf(ctx, row.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventNetworkCreated, Aggregate: "network", ID: row.Name,
			Payload: mustJSON(structureEventPayload{ID: row.Name, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "network.create", Resource: "network/" + row.Name, AfterFP: req.String(),
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{svc.s.parentProjectAlive(req.GetProjectId())},
		write: func(ctx context.Context, st transact.Stores) error {
			return st.Networks.Create(ctx, row)
		},
		facts: []func() writes.FactRow{fact},
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
	list, err := svc.s.Networks.ListByProjectPage(ctx,
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
	// project_id/name 必填组合检查已退役为 RebuildNetworkRequest 的
	// protovalidate CEL 注解（P4-1 批 1 Run B1）：validate 拦截器在 handler
	// 之前执法，拆 per-field 文案。
	// 行级授权（ADR-0035）：写面受理前置。
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	res, err := svc.s.Engine.RebuildNetwork(ctx, req.GetProjectId(), req.GetName())
	if err != nil {
		return nil, mapRebuildError(err)
	}
	net, err := svc.s.Networks.GetByName(ctx, req.GetProjectId(), req.GetName())
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	payload := mustJSON(networkRebuiltEventPayload{
		ProjectID: net.ProjectID, Network: net.Name,
		Detached: int32(res.Detached), Reattached: int32(res.Reattached), //nolint:gosec // 计数域内（载体数）
	})
	fact, err := svc.s.factOf(ctx, net.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventNetworkRebuilt, Aggregate: "network", ID: net.ID,
			Payload: payload,
			TeamID:  team,
			Action:  "network.rebuild", Resource: "network/" + net.Name,
			AfterFP: fmt.Sprintf("project=%s detached=%d reattached=%d", net.ProjectID, res.Detached, res.Reattached),
			Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		facts: []func() writes.FactRow{fact},
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

// networkPeerEventPayloadOf 构造 peer 事件载荷（aggregate=network，id=网络行）。
func networkPeerEventPayloadOf(p *networkpeer.Peer, net *network.Network) []byte {
	return mustJSON(networkPeerEventPayload{
		ID: p.ID, NetworkID: p.NetworkID, NetworkName: net.Name,
		NetworkProjectID: net.ProjectID, PeerProjectID: p.PeerProjectID,
	})
}

// networkPeerFacts 构造 peer 三拍的写事实（单声明双真 + 双侧审计）：
// 首行 = 事件 + 网络侧审计（双真）；次行 = 挂靠项目侧审计（仅取证面——
// 双侧记账是 ADR-0013 的审计面形状，流面孪生只此一条）。anchorProjectID
// 是资源轴 Team 解析锚（P0-2；peer 双项目——锚网络侧归属，网络行已删时
// 调用方改锚挂靠方项目）。归属解析失败返回 error：整单拒绝（R08-M8）。
func (s *Services) networkPeerFacts(ctx context.Context, anchorProjectID, name, action string, p *networkpeer.Peer, net *network.Network) ([]func() writes.FactRow, error) {
	fp := fmt.Sprintf("peer_project=%s network=%s state=%s", p.PeerProjectID, net.Name, p.State)
	primary, err := s.factOf(ctx, anchorProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: name, Aggregate: "network", ID: p.NetworkID,
			Payload: networkPeerEventPayloadOf(p, net),
			TeamID:  team,
			Action:  action, Resource: "network/" + net.Name + "/peers/" + p.ID, AfterFP: fp,
			Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	return []func() writes.FactRow{
		primary,
		func() writes.FactRow {
			// 双真偏离（仅取证面）：挂靠项目侧的镜像审计（ADR-0013 双侧
			// 记账），无独立流面孪生——A 化前亦只此一条审计行。
			return writes.FactRow{
				Action: action, Resource: "project/" + p.PeerProjectID + "/peers/" + p.ID, AfterFP: fp,
				Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				LedgerOnly: true,
			}
		},
	}, nil
}

// loadPeerNetwork 读 peer 声明指向的网络行（活跃口径；A.1：批准面只对
// 活跃网络有意义——已删网络的挂靠无接收方）。
func (svc *NetworksService) loadPeerNetwork(ctx context.Context, networkID string) (*network.Network, error) {
	net, err := svc.s.Networks.GetByID(ctx, networkID)
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
func (svc *NetworksService) requirePeerReceiverTeam(ctx context.Context, net *network.Network) error {
	teamID, owner, err := callerTeam(ctx)
	if err != nil {
		return err
	}
	if owner {
		return nil
	}
	proj, err := svc.s.Projects.Get(ctx, net.ProjectID)
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
	if net, nerr := svc.s.Networks.GetByID(ctx, p.NetworkID); nerr == nil {
		if proj, perr := svc.s.Projects.Get(ctx, net.ProjectID); perr == nil && !proj.Deleted() && proj.TeamID == teamID {
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
	// network_id/peer_project_id 必填组合检查已退役为
	// DeclareNetworkPeerRequest 的 protovalidate CEL 注解（P4-1 批 1 Run
	// B1）：validate 拦截器在 handler 之前执法，拆 per-field 文案。
	// 同项目挂靠交叉检查（动态文案）留在服务内。
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
	p := &networkpeer.Peer{NetworkID: net.ID, PeerProjectID: req.GetPeerProjectId()}
	facts, err := svc.s.networkPeerFacts(ctx, net.ProjectID, eventNetworkPeerDeclared, "network.peer_declare", p, net)
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		// 双侧父资源存活（事务内）：网络归属方与挂靠方项目都必须在场。
		checks: []acceptanceCheck{
			svc.s.parentProjectAlive(net.ProjectID),
			svc.s.parentProjectAlive(req.GetPeerProjectId()),
		},
		write: func(ctx context.Context, st transact.Stores) error {
			return st.NetworkPeers.Create(ctx, p)
		},
		facts: facts,
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
	p, err := svc.s.NetworkPeers.Get(ctx, req.GetId())
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
	facts, err := svc.s.networkPeerFacts(ctx, net.ProjectID, eventNetworkPeerApproved, "network.peer_approve", p, net)
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, st transact.Stores) error {
			return st.NetworkPeers.Approve(ctx, p.ID)
		},
		facts: facts,
	})
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	fresh, gerr := svc.s.NetworkPeers.Get(ctx, p.ID)
	if gerr != nil {
		return nil, mapStateError(gerr, "network peer")
	}
	return &structurev1.ApproveNetworkPeerResponse{Peer: networkPeerMsg(fresh, net)}, nil
}

// RevokeNetworkPeer 撤销（任一侧；幂等）：撤销落账后进入异步隔离（R16-M4）
// ——只标记待剥离并 Kick 漂移扫描环，请求路径不再同步 replay/Ensure（剥离
// 耗时随项目内 App 数线性叠加，曾让撤销请求最长等 N×30s）。剥离由下一拍
// 漂移扫描以 isolate 重收敛完成（断存量，A.4）；失败不回滚撤销（受理面已
// 生效），漂移拍记 Warn 并兜底重试。响应字段/错误码与同步语义兼容。
func (svc *NetworksService) RevokeNetworkPeer(ctx context.Context, req *structurev1.RevokeNetworkPeerRequest) (*structurev1.RevokeNetworkPeerResponse, error) {
	p, err := svc.s.NetworkPeers.Get(ctx, req.GetId())
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
	net := &network.Network{ID: p.NetworkID}
	if active, nerr := svc.s.Networks.GetByID(ctx, p.NetworkID); nerr == nil {
		net = active
	}
	// 资源轴锚：网络行已删（上一步的可容忍形态）时改锚挂靠方 Project——
	// 两侧都不可解析则 factOf 返回 error、撤销整单拒绝（R08-M8 fail-closed）。
	anchor := net.ProjectID
	if anchor == "" {
		anchor = p.PeerProjectID
	}
	facts, err := svc.s.networkPeerFacts(ctx, anchor, eventNetworkPeerRevoked, "network.peer_revoke", p, net)
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		write: func(ctx context.Context, st transact.Stores) error {
			return st.NetworkPeers.Revoke(ctx, p.ID)
		},
		facts: facts,
	})
	if err != nil {
		return nil, mapStateError(err, "network peer")
	}
	// 异步剥离（A.4/R16-M4）：撤销已落账即返回——标记 + Kick，剥离在下一拍
	// 漂移扫描完成（失败记 Warn，漂移拍兜底重试）。Info 是"已撤销未剥离"
	// 的最小可发现性表达（不新增 proto 字段）。
	svc.s.Engine.RequestPeerIsolation(p.NetworkID, p.PeerProjectID)
	if svc.s.Log != nil {
		svc.s.Log.Info("network peer revoked: attachment removal deferred to the next drift scan tick",
			"peer", p.ID, "network", p.NetworkID, "project", p.PeerProjectID)
	}
	fresh, gerr := svc.s.NetworkPeers.Get(ctx, p.ID)
	if gerr != nil {
		return nil, mapStateError(gerr, "network peer")
	}
	return &structurev1.RevokeNetworkPeerResponse{Peer: networkPeerMsg(fresh, net)}, nil
}

func (svc *NetworksService) GetNetworkPeer(ctx context.Context, req *structurev1.GetNetworkPeerRequest) (*structurev1.GetNetworkPeerResponse, error) {
	p, err := svc.s.NetworkPeers.Get(ctx, req.GetId())
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
	list, err := svc.s.NetworkPeers.List(ctx,
		req.GetNetworkId(), req.GetPeerProjectId(), req.GetAfterPeerSeq(), int(req.GetLimit()))
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
func (svc *NetworksService) peerNetProjection(ctx context.Context, p *networkpeer.Peer) *network.Network {
	net, err := svc.s.Networks.GetByID(ctx, p.NetworkID)
	if err != nil {
		return &network.Network{ID: p.NetworkID}
	}
	return net
}

// networkPeerMsg 行 → proto 映射（网络侧便捷字段来自读投影）。
func networkPeerMsg(p *networkpeer.Peer, net *network.Network) *structurev1.NetworkPeer {
	msg := &structurev1.NetworkPeer{
		Id: p.ID, Seq: p.Seq, PeerProjectId: p.PeerProjectID, State: string(p.State),
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, ApprovedAt: p.ApprovedAt,
	}
	if net != nil {
		msg.NetworkId = p.NetworkID
		msg.NetworkName = net.Name
		msg.NetworkProjectId = net.ProjectID
	}
	return msg
}
