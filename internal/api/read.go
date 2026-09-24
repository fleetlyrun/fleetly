package api

import (
	"context"
	"errors"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/ingress"
	"github.com/fleetlyrun/fleetly/internal/placement"
	"github.com/fleetlyrun/fleetly/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 只读资源面：Placement / Revisions / Domains（T2.17）。E1-7 起 Placement
// 扩容显式换点（UpdatePlacement）与跨 app 卷清单（ListVolumes）、迁移
// runbook（GetPlacementMigrationPlan）——multi-node §2.6/§2.8。

// PlacementService 实现 server.v1.PlacementService。res 是放置解析器
// （E1-7：换点裁决在 internal/placement——目标校验/数据处置门/同事务落库；
// api 面只做应用解析与投影；nil resolver = 测试形态的只读降级面——写面
// RPC 如实报不可用）。
type PlacementService struct {
	serverv1.UnimplementedPlacementServiceServer
	st  *state.Store
	res *placement.Resolver
}

// NewPlacementService 构造 PlacementService。
func NewPlacementService(st *state.Store, res *placement.Resolver) *PlacementService {
	return &PlacementService{st: st, res: res}
}

// ShowPlacement 放置绑定视图（未绑定时 placement 不输出）+ 卷注册表
// （T2.18：卷的钉住语义由放置绑定决定，与绑定同面展示）。
func (s *PlacementService) ShowPlacement(ctx context.Context, req *serverv1.ShowPlacementRequest) (*serverv1.ShowPlacementResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	// 角色门（W2-S4 第 2 门；层级随方法 scope 登记映射——读面 read、
	// UpdatePlacement=admin）。
	if err := requireAppAccess(ctx, s.st, app); err != nil {
		return nil, err
	}
	resp := &serverv1.ShowPlacementResponse{App: app.Name, Volumes: []*serverv1.VolumeView{}}
	if p, err := s.st.GetPlacement(ctx, app.ID); err == nil {
		resp.Placement = placementView(p)
	} else if !errors.Is(err, state.ErrPlacementNotFound) {
		return nil, err
	}
	volumes, err := s.st.ListAppVolumes(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	for _, v := range volumes {
		resp.Volumes = append(resp.Volumes, volumeView(v))
	}
	return resp, nil
}

// UpdatePlacement 显式换点（E1-7，multi-node §2.6；admin scope——破坏性
// 确认路径）：裁决在 internal/placement.Rebind（目标校验 + data_ack 门 +
// 同事务落库/事件/审计），换点不自动部署。
func (s *PlacementService) UpdatePlacement(ctx context.Context, req *serverv1.UpdatePlacementRequest) (*serverv1.UpdatePlacementResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	// 角色门（W2-S4 第 2 门；层级随方法 scope 登记映射——读面 read、
	// UpdatePlacement=admin）。
	if err := requireAppAccess(ctx, s.st, app); err != nil {
		return nil, err
	}
	if s.res == nil {
		return nil, status.Error(codes.Unavailable, "placement resolver unavailable (not assembled)")
	}
	res, err := s.res.Rebind(ctx, placement.RebindInput{
		AppID:   app.ID,
		Node:    req.GetNode(),
		DataAck: req.GetDataAck(),
		Confirm: req.GetConfirm(),
		Actor:   "human",
	})
	if err != nil {
		return nil, err
	}
	out := &serverv1.UpdatePlacementResponse{
		App:        app.Name,
		Placement:  placementView(res.Placement),
		Volumes:    []*serverv1.VolumeView{},
	}
	for _, v := range res.Volumes {
		out.Volumes = append(out.Volumes, volumeView(v))
	}
	return out, nil
}

// ListVolumes 跨 app 卷清单（E1-7，multi-node §2.8；read scope）：active/
// orphaned/discarded 行 + residual 派生标记（prev_platform_node_id 非空的
// active 行 = 源节点有待清理副本）。status 过滤缺省输出全部；不建生命
// 周期 API、不做远端删除（D18）。卷归属可从命名约定名读取
//（fleetly-<app>-<key>-<appid8>，state-model §2.4）。
func (s *PlacementService) ListVolumes(ctx context.Context, req *serverv1.ListVolumesRequest) (*serverv1.ListVolumesResponse, error) {
	rows, err := s.st.ListAllVolumes(ctx)
	if err != nil {
		return nil, err
	}
	out := &serverv1.ListVolumesResponse{Volumes: []*serverv1.VolumeView{}}
	for _, v := range rows {
		if filter := req.GetStatus(); filter != "" && string(v.Status) != filter {
			continue
		}
		residual := v.Status == state.VolumeActive && v.PrevPlatformNodeID != ""
		if req.GetResidual() && !residual {
			continue
		}
		out.Volumes = append(out.Volumes, volumeView(v))
	}
	return out, nil
}

// GetPlacementMigrationPlan restic 迁移 runbook（E1-7，multi-node §2.8/
// D-MN-10）：服务端生成步骤文档（真实卷名/节点名填充）；只读面，不触发
// 任何状态变更。
func (s *PlacementService) GetPlacementMigrationPlan(ctx context.Context, req *serverv1.GetPlacementMigrationPlanRequest) (*serverv1.GetPlacementMigrationPlanResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	// 角色门（W2-S4 第 2 门；层级随方法 scope 登记映射——读面 read、
	// UpdatePlacement=admin）。
	if err := requireAppAccess(ctx, s.st, app); err != nil {
		return nil, err
	}
	if s.res == nil {
		return nil, status.Error(codes.Unavailable, "placement resolver unavailable (not assembled)")
	}
	plan, err := s.res.MigrationPlan(ctx, app.ID, app.Name, req.GetTo())
	if err != nil {
		return nil, err
	}
	out := &serverv1.GetPlacementMigrationPlanResponse{
		App:      app.Name,
		FromNode: plan.FromNode,
		ToNode:   plan.ToNode,
		Volumes:  []*serverv1.VolumeView{},
		Steps:    []*serverv1.MigrationStep{},
		Warnings: plan.Warnings,
	}
	for _, v := range plan.Volumes {
		out.Volumes = append(out.Volumes, volumeView(v))
	}
	for _, st := range plan.Steps {
		out.Steps = append(out.Steps, &serverv1.MigrationStep{Title: st.Title, Detail: st.Detail})
	}
	return out, nil
}

// RevisionsService 实现 server.v1.RevisionsService。
type RevisionsService struct {
	serverv1.UnimplementedRevisionsServiceServer
	st *state.Store
}

// NewRevisionsService 构造 RevisionsService。
func NewRevisionsService(st *state.Store) *RevisionsService {
	return &RevisionsService{st: st}
}

// ListRevisions 版本快照列表（保留窗；列表即回滚选项集）。
func (s *RevisionsService) ListRevisions(ctx context.Context, req *serverv1.ListRevisionsRequest) (*serverv1.ListRevisionsResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	// 角色门（W2-S4 第 2 门；层级随方法 scope 登记映射——读面 read、
	// UpdatePlacement=admin）。
	if err := requireAppAccess(ctx, s.st, app); err != nil {
		return nil, err
	}
	rows, err := s.st.ListRevisions(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.RevisionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, &serverv1.RevisionView{
			Id:          r.ID,
			Seq:         r.Seq,
			DesiredHash: r.DesiredHash,
			Status:      r.Status,
			Verified:    r.Verified,
			CreatedAt:   tstamp(r.CreatedAt),
		})
	}
	return &serverv1.ListRevisionsResponse{Revisions: out}, nil
}

// GetRevisionSpec 单条快照的归一化 compose 正文（canonical JSON；T2.18
// plan 的 RPC 基线消费——env 为 key:sha256，值明文结构性不在快照中）。
// superseded → 404（与回滚选项面同口径：列不出来的不可消费）。
func (s *RevisionsService) GetRevisionSpec(ctx context.Context, req *serverv1.GetRevisionSpecRequest) (*serverv1.GetRevisionSpecResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	// 角色门（W2-S4 第 2 门；层级随方法 scope 登记映射——读面 read、
	// UpdatePlacement=admin）。
	if err := requireAppAccess(ctx, s.st, app); err != nil {
		return nil, err
	}
	rev, err := s.st.GetAppRevision(ctx, app.ID, req.GetRevisionId())
	if err != nil {
		if errors.Is(err, state.ErrRevisionNotFound) {
			return nil, notFound("revision not found: " + req.GetRevisionId())
		}
		return nil, err
	}
	return &serverv1.GetRevisionSpecResponse{
		RevisionId: rev.ID,
		Seq:        rev.Seq,
		Compose:    rev.ComposeNormalized,
	}, nil
}

// DomainsService 实现 server.v1.DomainsService。
type DomainsService struct {
	serverv1.UnimplementedDomainsServiceServer
	st  *state.Store
	mgr *ingress.Manager
}

// NewDomainsService 构造 DomainsService。
func NewDomainsService(st *state.Store, mgr *ingress.Manager) *DomainsService {
	return &DomainsService{st: st, mgr: mgr}
}

// ListAppDomains 域名台账（只读；写入方唯一 = internal/ingress 发布对账）。
func (s *DomainsService) ListAppDomains(ctx context.Context, req *serverv1.ListAppDomainsRequest) (*serverv1.ListAppDomainsResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	// 角色门（W2-S4 第 2 门；层级随方法 scope 登记映射——读面 read、
	// UpdatePlacement=admin）。
	if err := requireAppAccess(ctx, s.st, app); err != nil {
		return nil, err
	}
	rows, err := s.st.ListAppDomains(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	out := make([]*serverv1.DomainView, 0, len(rows))
	for _, d := range rows {
		out = append(out, &serverv1.DomainView{
			Service:      d.Service,
			Domain:       d.Domain,
			Port:         d.Port,
			CertSha256:   d.CertSHA256,
			CertNotAfter: tstamp(d.CertNotAfter),
			CreatedAt:    tstamp(d.CreatedAt),
		})
	}
	return &serverv1.ListAppDomainsResponse{Domains: out}, nil
}

// VerifyAppDomains 本机视角域名验证（ingress.VerifyDomains；探测材料
// 如实记录，判断权在操作者）。
func (s *DomainsService) VerifyAppDomains(ctx context.Context, req *serverv1.VerifyAppDomainsRequest) (*serverv1.VerifyAppDomainsResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
		return nil, err
	}
	// 角色门（W2-S4 第 2 门；层级随方法 scope 登记映射——读面 read、
	// UpdatePlacement=admin）。
	if err := requireAppAccess(ctx, s.st, app); err != nil {
		return nil, err
	}
	rows, err := s.st.ListAppDomains(ctx, app.ID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return &serverv1.VerifyAppDomainsResponse{}, nil
	}
	domains := make([]string, 0, len(rows))
	for _, d := range rows {
		domains = append(domains, d.Domain)
	}
	// E4（S19）：探测端口与入口发布端口同源（Manager 配置）——非默认
	// 端口部署不再探测 80/443 假目标；无 ingress 装配的测试面回落缺省。
	httpPort, httpsPort := 80, 443
	if s.mgr != nil {
		httpPort, httpsPort = s.mgr.Config().HTTPPort, s.mgr.Config().HTTPSPort
	}
	checks := ingress.VerifyDomains(ctx, domains, httpPort, httpsPort)
	out := make([]*serverv1.DomainCheckView, 0, len(checks))
	for _, c := range checks {
		out = append(out, &serverv1.DomainCheckView{
			Domain:       c.Domain,
			Ips:          c.IPs,
			Resolved:     c.Resolved,
			Http_80:      c.HTTP80,
			Https_443:    c.HTTPS443,
			CertSubject:  c.CertSubject,
			CertDnsNames: c.CertDNSNames,
			Error:        c.Err,
		})
	}
	return &serverv1.VerifyAppDomainsResponse{Checks: out}, nil
}
