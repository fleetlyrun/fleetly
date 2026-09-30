package fleetlygrpc

// Delivery 上下文服务实现（Deployments/Revisions/Builds）。Deploy 是一站式
// 入口：两源归一化 → 校验 → Revision 冻结（内容寻址复用）→ admission。

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/spec"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/revision"
)

type DeploymentsService struct {
	deliveryv1.UnimplementedDeploymentsServiceServer
	s *Services
}

// Deploy 归一化两源（F0.8）并受理部署（admission 判定全在 engine.Submit）。
func (svc *DeploymentsService) Deploy(ctx context.Context, req *deliveryv1.DeployRequest) (*deliveryv1.DeployResponse, error) {
	if req.GetAppId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "app_id: must not be empty")
	}
	appRow, err := svc.s.Apps.Get(ctx, svc.s.DB.Runner(), req.GetAppId())
	if err != nil {
		return nil, mapStateError(err, "app")
	}

	appSpec, err := normalizeDeploySource(req, appRow)
	if err != nil {
		return nil, err
	}
	body, err := marshalSpec(appSpec)
	if err != nil {
		return nil, mapStateError(err, "app spec")
	}

	// Revision 冻结（内容寻址复用：同内容只冻结一份；R1..Rn 序号 App 内单调）。
	var rev *revision.Revision
	err = svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if existing, ferr := svc.s.Revisions.FindByDigest(ctx, tx, appRow.ID, revision.Digest(body)); ferr == nil {
			rev = existing
			return nil
		}
		seq, serr := svc.s.Revisions.NextSeq(ctx, tx, appRow.ID)
		if serr != nil {
			return serr
		}
		rev = &revision.Revision{ID: newID(), AppID: appRow.ID, Seq: seq, Spec: body}
		if cerr := svc.s.Revisions.Create(ctx, tx, rev); cerr != nil {
			return cerr
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx), Action: "revision.create",
			Resource: "revision/" + rev.ID, AfterFP: rev.Digest,
		})
	})
	if err != nil {
		return nil, mapStateError(err, "revision")
	}

	d, err := svc.s.Engine.Submit(ctx, engine.SubmitRequest{
		AppID: appRow.ID, RevisionID: rev.ID,
		IdempotencyKey: req.GetIdempotencyKey(), CommitSHA: req.GetCommitSha(),
		Supersede: req.GetSupersede(),
	})
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}
	return &deliveryv1.DeployResponse{Deployment: deploymentMsg(*d)}, nil
}

// normalizeDeploySource 归一化两源：image 直投 / Compose 受控子集。
func normalizeDeploySource(req *deliveryv1.DeployRequest, appRow *app.App) (*specv1.AppSpec, error) {
	switch {
	case req.GetImage() != "":
		s, err := spec.ImageDeploy(appRow.ID, appRow.ProjectID, req.GetImage(), req.GetProcessName())
		if err != nil {
			return nil, mapValidationError(err)
		}
		return s, nil
	case req.GetComposeYaml() != "":
		var doc spec.ComposeDoc
		if err := yaml.Unmarshal([]byte(req.GetComposeYaml()), &doc); err != nil {
			return nil, apperr.New("E_INVALID_ARGUMENT", "compose_yaml: invalid YAML: %s", err.Error()).WithCause(err)
		}
		s, err := spec.NormalizeCompose(doc, appRow.ID, appRow.ProjectID)
		if err != nil {
			return nil, mapValidationError(err)
		}
		return s, nil
	default:
		return nil, apperr.New("E_INVALID_ARGUMENT", "one of image or compose_yaml is required")
	}
}

func (svc *DeploymentsService) GetDeployment(ctx context.Context, req *deliveryv1.GetDeploymentRequest) (*deliveryv1.GetDeploymentResponse, error) {
	d, err := svc.s.Deployments.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}
	return &deliveryv1.GetDeploymentResponse{Deployment: deploymentMsg(*d)}, nil
}

func (svc *DeploymentsService) ListDeployments(ctx context.Context, req *deliveryv1.ListDeploymentsRequest) (*deliveryv1.ListDeploymentsResponse, error) {
	list, err := svc.s.Deployments.ListByApp(ctx, svc.s.DB.Runner(), req.GetAppId())
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}
	out := &deliveryv1.ListDeploymentsResponse{}
	for _, d := range list {
		out.Deployments = append(out.Deployments, deploymentMsg(d))
	}
	return out, nil
}

func (svc *DeploymentsService) CancelDeployment(ctx context.Context, req *deliveryv1.CancelDeploymentRequest) (*deliveryv1.CancelDeploymentResponse, error) {
	d, err := svc.s.Engine.Cancel(ctx, req.GetId())
	if err != nil {
		return nil, mapStateError(err, "deployment")
	}
	return &deliveryv1.CancelDeploymentResponse{Deployment: deploymentMsg(*d)}, nil
}

func (svc *DeploymentsService) Rollback(ctx context.Context, req *deliveryv1.RollbackRequest) (*deliveryv1.RollbackResponse, error) {
	d, err := svc.s.Engine.Rollback(ctx, req.GetAppId(), req.GetToRevision())
	if err != nil {
		if strings.Contains(err.Error(), "no successful baseline") {
			return nil, apperr.New("E_NO_BASELINE", "app %s has no successful deployment to roll back to", req.GetAppId()).WithCause(err)
		}
		return nil, mapStateError(err, "rollback")
	}
	return &deliveryv1.RollbackResponse{Deployment: deploymentMsg(*d)}, nil
}

// ---- Revisions ----

type RevisionsService struct {
	deliveryv1.UnimplementedRevisionsServiceServer
	s *Services
}

func (svc *RevisionsService) ListRevisions(ctx context.Context, req *deliveryv1.ListRevisionsRequest) (*deliveryv1.ListRevisionsResponse, error) {
	list, err := svc.s.Revisions.ListByApp(ctx, svc.s.DB.Runner(), req.GetAppId())
	if err != nil {
		return nil, mapStateError(err, "revision")
	}
	out := &deliveryv1.ListRevisionsResponse{}
	for _, rev := range list {
		out.Revisions = append(out.Revisions, &deliveryv1.Revision{
			Id: rev.ID, AppId: rev.AppID, Seq: rev.Seq, Digest: rev.Digest, CreatedAt: rev.CreatedAt,
		})
	}
	return out, nil
}

func (svc *RevisionsService) DiffRevisions(ctx context.Context, req *deliveryv1.DiffRevisionsRequest) (*deliveryv1.DiffRevisionsResponse, error) {
	if req.GetAppId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "app_id: must not be empty")
	}
	from, err := svc.s.Revisions.GetBySeq(ctx, svc.s.DB.Runner(), req.GetAppId(), req.GetFromSeq())
	if err != nil {
		return nil, mapStateError(err, "revision")
	}
	to, err := svc.s.Revisions.GetBySeq(ctx, svc.s.DB.Runner(), req.GetAppId(), req.GetToSeq())
	if err != nil {
		return nil, mapStateError(err, "revision")
	}
	entries, err := spec.Diff(from.Spec, to.Spec)
	if err != nil {
		return nil, mapStateError(err, "revision diff")
	}
	out := &deliveryv1.DiffRevisionsResponse{}
	for _, e := range entries {
		out.Entries = append(out.Entries, &deliveryv1.DiffEntry{
			Path: e.Path, OldValue: scalarJSON(e.Old), NewValue: scalarJSON(e.New),
		})
	}
	return out, nil
}

// ---- Builds ----

type BuildsService struct {
	deliveryv1.UnimplementedBuildsServiceServer
	s *Services
}

func (svc *BuildsService) ListBuilds(ctx context.Context, req *deliveryv1.ListBuildsRequest) (*deliveryv1.ListBuildsResponse, error) {
	list, err := svc.s.Builds.ListByApp(ctx, svc.s.DB.Runner(), req.GetAppId())
	if err != nil {
		return nil, mapStateError(err, "build")
	}
	out := &deliveryv1.ListBuildsResponse{}
	for _, b := range list {
		out.Builds = append(out.Builds, buildMsg(b))
	}
	return out, nil
}

// scalarJSON 渲染 diff 标量（nil → 空串 = 缺失侧）。
func scalarJSON(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
