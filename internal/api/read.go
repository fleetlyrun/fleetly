package api

import (
	"context"
	"errors"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/ingress"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// 只读资源面：Placement / Revisions / Domains（T2.17）。

// PlacementService 实现 server.v1.PlacementService。
type PlacementService struct {
	serverv1.UnimplementedPlacementServiceServer
	st *state.Store
}

// NewPlacementService 构造 PlacementService。
func NewPlacementService(st *state.Store) *PlacementService {
	return &PlacementService{st: st}
}

// ShowPlacement 放置绑定视图（未绑定时 placement 不输出）+ 卷注册表
// （T2.18：卷的钉住语义由放置绑定决定，与绑定同面展示）。
func (s *PlacementService) ShowPlacement(ctx context.Context, req *serverv1.ShowPlacementRequest) (*serverv1.ShowPlacementResponse, error) {
	app, err := resolveApp(ctx, s.st, req.GetApp())
	if err != nil {
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
		resp.Volumes = append(resp.Volumes, &serverv1.VolumeView{
			Key:       v.Key,
			Name:      v.Name,
			Kind:      string(v.Kind),
			NodeId:    v.PlatformNodeID,
			MountPath: v.MountPath,
			Status:    string(v.Status),
		})
	}
	return resp, nil
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
