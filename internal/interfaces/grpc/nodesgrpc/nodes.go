// Package nodesgrpc 实现 fleetly.runtime.v1.NodesService：节点观测缓存、
// 加入材料（join/rotate/relay 凭证）与 RuntimeAdmin 运维三动词。平台管理员
// 门、子面协商与审计留痕在此单源。
package nodesgrpc

import (
	"context"
	"encoding/json"
	"errors"

	"google.golang.org/grpc"

	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/runtimeops/node"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

// Nodes 是节点中继面的窄消费接口（engine 门面实现）。
type Nodes interface {
	RelayStatuses() map[string]api.RelayStatus
	DropRelay(platformID string)
	DropAllRelays()
}

// Deps 是节点服务的依赖（窄接口，不持整个 gRPC Services）。
type Deps struct {
	NodeRepo node.Repo
	Nodes    Nodes
	// Runtime 是运行时能力的动态读面（测试会替换 Services.Runtime 注入
	// 缺面子面；闭包保持晚绑定）。
	Runtime     func() capability.Runtime
	GatewayPort string
	Commit      func(ctx context.Context, fact grpcapi.WriteFact) error
}

// Service 满足 runtimev1.NodesServiceServer。
type Service struct {
	runtimev1.UnimplementedNodesServiceServer
	deps Deps
}

// New 构造节点服务。
func New(deps Deps) *Service { return &Service{deps: deps} }

// Register 注册节点服务。
func Register(srv *grpc.Server, deps Deps) {
	runtimev1.RegisterNodesServiceServer(srv, New(deps))
}

var _ runtimev1.NodesServiceServer = (*Service)(nil)

// runtime 返回动态运行时读面（零值 Deps = nil，FacesOf 全缺席）。
func (d Deps) runtime() capability.Runtime {
	if d.Runtime == nil {
		return nil
	}
	return d.Runtime()
}

// ---- Nodes（F0.20：观测缓存 + 加入材料） ----

// ListNodes 是平台级读面（R08-M1）：节点清单含 relay 在线状态与全平台节点
// 目录，节点本身无 Team 轴——读面与写动词同门（平台管理员），门在任何
// 列表读取之前（拒绝零暴露）。成员不再看到跨租户节点面。
func (svc *Service) ListNodes(ctx context.Context, req *runtimev1.ListNodesRequest) (*runtimev1.ListNodesResponse, error) {
	if err := grpcapi.RequirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	list, err := svc.deps.NodeRepo.ListPage(ctx,
		req.GetAfterNodeSeq(), grpcapi.ListLimit(req.GetLimit()))
	if err != nil {
		return nil, grpcapi.MapStateError(err, "node")
	}
	// 中继在连状态是活体观测（engine hub 快照，非 nodes 表行——代理断连
	// 即失，无持久化面；ADR-0049）。
	relay := svc.deps.Nodes.RelayStatuses()
	out := &runtimev1.ListNodesResponse{}
	for _, n := range list {
		msg := nodeMsg(n)
		if st, ok := relay[n.PlatformID]; ok {
			msg.RelayOnline = st.Online
			msg.RelayVersion = st.Version
		}
		out.Nodes = append(out.Nodes, msg)
	}
	return out, nil
}

// ListNodeContainers 实时观测节点上的在跑容器（RuntimeNodeInspect 面；
// 平台节点 ID 为锚——运维三动词同锚约定）。读面零审计（ListNodes 同款）；
// 列表面诚实空——无在跑容器是空集而非错误。平台管理员门与 ListNodes 同
// 门（R08-M1）：容器行携带 team/project/app 归属，跨租户侦察面必须清零；
// 门先于子面协商与节点存在性（403 不带存在性泄露）。
func (svc *Service) ListNodeContainers(ctx context.Context, req *runtimev1.ListNodeContainersRequest) (*runtimev1.ListNodeContainersResponse, error) {
	if err := grpcapi.RequirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	inspect := capability.FacesOf(svc.deps.runtime()).NodeInspect // 节点观测子面（FacesOf 协商点）
	if inspect == nil {
		// 可选子面缺席是 honest failure（P4-3 批 3）：精确 501，不再落
		// E_INTERNAL 脱敏 500。
		return nil, apperr.New("E_RUNTIME_FACE_UNSUPPORTED", "the runtime provider does not expose node inspection")
	}
	rows, err := inspect.NodeContainers(ctx, req.GetNodeId())
	if err != nil {
		if errors.Is(err, capability.ErrNodeNotFound) {
			return nil, apperr.New("E_NOT_FOUND", "node not found").WithCause(err)
		}
		return nil, apperr.New("E_INTERNAL", "node container listing failed").WithCause(err)
	}
	out := &runtimev1.ListNodeContainersResponse{}
	for _, row := range rows {
		out.Containers = append(out.Containers, &runtimev1.NodeContainer{
			Instance: row.Instance, WorkloadId: row.WorkloadID, Domain: row.Domain,
			Team: row.Team, ProjectId: row.Project, AppId: row.AppID, Process: row.Process,
			Image: row.Image,
		})
	}
	return out, nil
}

// EnrollNode 是节点材料动词（H10 §4.4 三分支）：无参数 = 初始 join；rotate
// = 轮换 join token + 作废全部节点 relay 凭证并断开在连中继；node_id 定向
// = 为该已锚定节点铸造个体 relay 凭证（revoke=false）或吊销（revoke=true）。
// 平台管理员门（R08-H1）：join token 等价集群成员权，`nodes:admin` 只覆盖
// 可逆运维面，不得白送集群钥匙；门在审计与 Runtime 副作用之前（拒绝零
// 副作用）。
func (svc *Service) EnrollNode(ctx context.Context, req *runtimev1.EnrollNodeRequest) (*runtimev1.EnrollNodeResponse, error) {
	if err := grpcapi.RequirePlatformAdmin(ctx); err != nil {
		return nil, err
	}
	rotate, nodeID, revoke := req.GetRotate(), req.GetNodeId(), req.GetRevoke()
	// 参数组合校验（拒绝零副作用）：rotate 与定向动作互斥；revoke 必须
	// 带 node_id（无锚点的吊销无对象）。
	if rotate && (nodeID != "" || revoke) {
		return nil, apperr.New("E_INVALID_ARGUMENT", "rotate cannot be combined with node_id or revoke")
	}
	if revoke && nodeID == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "revoke requires node_id")
	}
	switch {
	case rotate:
		return svc.enrollRotate(ctx)
	case nodeID == "":
		return svc.enrollInitial(ctx)
	case revoke:
		return svc.enrollRelayRevoke(ctx, nodeID)
	default:
		return svc.enrollRelayMaterial(ctx, nodeID)
	}
}

// enrollInitial 是初始 enrollment：只返回 join 命令（H10：中继材料不再随
// 初始 enrollment 产出——join token 退出 relay 鉴权面；节点锚定后按节点
// 定向铸造）。材料生成是敏感读+写意图，先审计后生成（N0.1 P2-11）。
func (svc *Service) enrollInitial(ctx context.Context) (*runtimev1.EnrollNodeResponse, error) {
	if err := svc.deps.Commit(ctx, grpcapi.WriteFact{
		Facts: []func() writes.FactRow{
			// 双真偏离（仅取证面）：集群材料动词未入事件流（node 聚合的
			// 流面只铺 joined/left 迁移；A 化前亦无事件行，行集合零漂移）。
			func() writes.FactRow {
				return writes.FactRow{
					Action: "node.enroll", Resource: "cluster/join-material",
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					LedgerOnly: true,
				}
			},
		},
	}); err != nil {
		return nil, err
	}
	kit, err := svc.deps.runtime().Enrollment(ctx, false, capability.EnrollmentOptions{GatewayPort: svc.deps.GatewayPort})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "enrollment")
	}
	return &runtimev1.EnrollNodeResponse{JoinCommand: kit.Command}, nil
}

// enrollRotate 是泄漏处置面（H10 §4.4 步骤 3 / §4.6）：Runtime 轮换是外部
// 副作用，先于 DB 事务（事务失败时 join token 已轮换、旧 relay 凭证仍在，
// 重试或逐节点重铸均可收敛，错误如实上抛）；事务删全部 relay 凭证 + 审计；
// 提交后断开全部在连中继（先让凭证失效，再断连接；即使断开失败，重连也
// 会 401）。
func (svc *Service) enrollRotate(ctx context.Context) (*runtimev1.EnrollNodeResponse, error) {
	kit, err := svc.deps.runtime().Enrollment(ctx, true, capability.EnrollmentOptions{GatewayPort: svc.deps.GatewayPort})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "enrollment")
	}
	if err := svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Nodes.DeleteAllRelayCredentials(ctx)
		},
		Facts: []func() writes.FactRow{
			// 双真偏离（仅取证面）：集群材料动词未入事件流（node 聚合的
			// 流面只铺 joined/left 迁移；A 化前亦无事件行，行集合零漂移）。
			// relay 凭证全量作废是 auth 材料吊销，不是聚合状态迁移。
			func() writes.FactRow {
				return writes.FactRow{
					Action: "node.rotate_join_tokens", Resource: "cluster/join-material",
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					LedgerOnly: true,
					Detail:     auditDetailJSON(map[string]any{"relay_credentials_revoked": true}),
				}
			},
		},
	}); err != nil {
		return nil, err
	}
	svc.deps.Nodes.DropAllRelays()
	return &runtimev1.EnrollNodeResponse{JoinCommand: kit.Command}, nil
}

// enrollRelayMaterial 为已锚定节点铸造个体 relay 凭证（H10 §4.4 步骤 5）：
// 节点行 → manager/空 carrier 拒绝 → RuntimeRelayMaterial face（k3s 无 →
// 501）→ 铸材质 → 渲染脚本（事务前，无副作用）→ 事务 UPSERT hash + 审计
// → 断开该节点旧连接（重铸即断，旧凭证即刻失效）。
func (svc *Service) enrollRelayMaterial(ctx context.Context, nodeID string) (*runtimev1.EnrollNodeResponse, error) {
	n, err := svc.deps.NodeRepo.Get(ctx, nodeID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "node")
	}
	// manager 的 relay 由进程内回环承载，节点侧脚本会造成双 relay 抖动；
	// 空 carrier 无法绑定（凭证的 carrier 轴没有值）。
	if n.CarrierID == "" || n.Role == "manager" {
		return nil, apperr.New("E_INVALID_ARGUMENT",
			"node %s cannot receive relay material: manager nodes run the in-process relay and a node without a carrier id cannot be bound",
			nodeID)
	}
	face := capability.FacesOf(svc.deps.runtime()).RelayMaterial
	if face == nil {
		// 可选子面缺席是 honest failure：k3s 集中形态无节点侧材料。
		return nil, apperr.New("E_RUNTIME_FACE_UNSUPPORTED", "the runtime provider does not expose node relay material")
	}
	material, err := node.NewRelayCredential()
	if err != nil {
		return nil, apperr.New("E_INTERNAL", "node relay credential could not be minted").WithCause(err)
	}
	script, err := face.NodeRelayCommand(ctx, material.Secret, capability.EnrollmentOptions{GatewayPort: svc.deps.GatewayPort})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "relay material")
	}
	// 事务失败不返回脚本（脚本是单次可见的明文材料——不落库、不回显）。
	carrierID, prefix := n.CarrierID, material.Prefix
	if err := svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Nodes.PutRelayCredential(ctx, &node.RelayCredential{
				PlatformID: n.PlatformID, CarrierID: carrierID, SHA256: material.SHA256,
			})
		},
		Facts: []func() writes.FactRow{
			// 双真偏离（仅取证面）：auth 材料铸造不是聚合状态迁移，无 outbox
			// 孪生事件；Detail 只带 carrier 与明文前缀（绝不落明文/完整 sha）。
			func() writes.FactRow {
				return writes.FactRow{
					Action: "node.relay_credential_issued", Resource: "node/" + n.PlatformID,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					LedgerOnly: true,
					Detail: auditDetailJSON(map[string]string{
						"carrier_id": carrierID, "prefix": prefix,
					}),
				}
			},
		},
	}); err != nil {
		return nil, err
	}
	svc.deps.Nodes.DropRelay(n.PlatformID)
	return &runtimev1.EnrollNodeResponse{RelayCommand: script}, nil
}

// enrollRelayRevoke 吊销单节点 relay 凭证（H10 §4.4 步骤 6）：无行 = 幂等
// 成功且不写审计（无删除事实）；实际删行才落 node.relay_credential_revoked
// （Detail: carrier_id, reason="manual"）并断开该节点在连中继。不要求
// RuntimeRelayMaterial face（k3s 空删幂等；runtime 换届后也可清理存量行）。
func (svc *Service) enrollRelayRevoke(ctx context.Context, nodeID string) (*runtimev1.EnrollNodeResponse, error) {
	n, err := svc.deps.NodeRepo.Get(ctx, nodeID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "node")
	}
	carrierID := n.CarrierID
	if err := svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			deleted, err := st.Nodes.DeleteRelayCredential(ctx, nodeID)
			if err != nil || !deleted {
				return err
			}
			// 条件事实：只有实际删行才有删除事实可记（幂等空删不写审计）。
			// writeFact.facts 声明是前定的、逐条必发——条件分支只能在写段
			// 内以同一事务的 kernel 调用表达（不导出新的声明面）。
			return writes.Sequence(ctx, grpcapi.FactRepos(st), func() writes.FactRow {
				// 双真偏离（仅取证面）：auth 材料吊销不是聚合状态迁移，无
				// outbox 孪生事件；Detail 只带 carrier 与吊销原因。
				return writes.FactRow{
					Action: "node.relay_credential_revoked", Resource: "node/" + nodeID,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					LedgerOnly: true,
					Detail:     auditDetailJSON(map[string]string{"carrier_id": carrierID, "reason": "manual"}),
				}
			})
		},
	}); err != nil {
		return nil, err
	}
	svc.deps.Nodes.DropRelay(nodeID)
	return &runtimev1.EnrollNodeResponse{}, nil
}

// auditDetailJSON 编码审计 Detail（结构受控；编码失败退回空串——审计行
// 仍落，动作可见性不因 Detail 缺失而丢失）。
func auditDetailJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// DrainNode 把节点置为排空（F0.19 RuntimeAdmin 面；平台节点 ID 为锚）。
func (svc *Service) DrainNode(ctx context.Context, req *runtimev1.DrainNodeRequest) (*runtimev1.DrainNodeResponse, error) {
	if err := svc.nodeAdmin(ctx, req.GetNodeId(), "node.drain", func(a capability.RuntimeAdmin, nodeID string) error {
		return a.Drain(ctx, nodeID)
	}); err != nil {
		return nil, err
	}
	return &runtimev1.DrainNodeResponse{}, nil
}

// CordonNode 封锁节点（拒绝新调度，存量不动）。
func (svc *Service) CordonNode(ctx context.Context, req *runtimev1.CordonNodeRequest) (*runtimev1.CordonNodeResponse, error) {
	if err := svc.nodeAdmin(ctx, req.GetNodeId(), "node.cordon", func(a capability.RuntimeAdmin, nodeID string) error {
		return a.Cordon(ctx, nodeID)
	}); err != nil {
		return nil, err
	}
	return &runtimev1.CordonNodeResponse{}, nil
}

// UncordonNode 解除封锁。
func (svc *Service) UncordonNode(ctx context.Context, req *runtimev1.UncordonNodeRequest) (*runtimev1.UncordonNodeResponse, error) {
	if err := svc.nodeAdmin(ctx, req.GetNodeId(), "node.uncordon", func(a capability.RuntimeAdmin, nodeID string) error {
		return a.Uncordon(ctx, nodeID)
	}); err != nil {
		return nil, err
	}
	return &runtimev1.UncordonNodeResponse{}, nil
}

// nodeAdmin 是三个 RuntimeAdmin 动词的公共骨架：校验锚点 → 判子面可用 →
// 执行 → 落审计行。副作用在编排器侧、不可与审计同事务：先变更后留痕，
// 审计失败如实报错（F0.7 全部写操作留痕契约）；结果即动作本身，无前后
// 值指纹可记。
//
// node_id 必填已退役 descriptor 注解（P4-1 批 1 Run B2）：三请求
// DrainNode/CordonNode/UncordonNode 的 node_id 由 protovalidate 拦截器在
// 受理面拦截，文案与退役前逐字节一致。
func (svc *Service) nodeAdmin(ctx context.Context, nodeID, action string, op func(capability.RuntimeAdmin, string) error) error {
	// 平台管理员门（R08-H1）：节点排空/封锁是集群面动作（影响全平台
	// 调度），门在子面协商、编排器副作用与审计之前。
	if err := grpcapi.RequirePlatformAdmin(ctx); err != nil {
		return err
	}
	admin := capability.FacesOf(svc.deps.runtime()).Admin // 节点管理子面（FacesOf 协商点）
	if admin == nil {
		// 可选子面缺席是 honest failure（P4-3 批 3）：精确 501，不再落
		// E_INTERNAL 脱敏 500。
		return apperr.New("E_RUNTIME_FACE_UNSUPPORTED", "the runtime provider does not expose node administration")
	}
	if err := op(admin, nodeID); err != nil {
		if errors.Is(err, capability.ErrNodeNotFound) {
			return apperr.New("E_NOT_FOUND", "node not found").WithCause(err)
		}
		return apperr.New("E_INTERNAL", "node administration failed").WithCause(err)
	}
	return svc.deps.Commit(ctx, grpcapi.WriteFact{
		Facts: []func() writes.FactRow{
			// 双真偏离（仅取证面）：节点管理动词的副作用在编排器侧（不可与
			// 审计同事务），流面无孪生事实（A 化前亦无事件行，行集合零漂移）。
			func() writes.FactRow {
				return writes.FactRow{
					Action: action, Resource: "node/" + nodeID,
					Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
					LedgerOnly: true,
				}
			},
		},
	})
}

// nodeMsg 把节点行投影为 proto 消息。
func nodeMsg(n node.Node) *runtimev1.Node {
	return &runtimev1.Node{
		PlatformId: n.PlatformID, Seq: n.Seq, CarrierId: n.CarrierID, Hostname: n.Hostname,
		Role: n.Role, Available: n.Available, FirstSeenAt: n.FirstSeenAt, LastSeenAt: n.LastSeenAt,
	}
}
