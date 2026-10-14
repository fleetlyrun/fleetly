package fleetlygrpc

// RegistryService 实现（IA v3 二期⑤b）：受管镜像仓内容只读代理面——
// catalog 按项目前纲过滤、tags 校验前纲归属（engine/registrycontent.go
// 单源）。零状态迁移：纯读代理，无受理位/无事件/无审计（读面审计不落——
// 与 ListApps 同口径）。

import (
	"context"
	"errors"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
)

type RegistryService struct {
	deliveryv1.UnimplementedRegistryServiceServer
	s *Services
}

// ListRegistryCatalog 列项目前纲下的仓名（Registry 面停用 = 精确失败，
// 与 QueryMetrics 的 metrics.addr 同口径）。
func (svc *RegistryService) ListRegistryCatalog(ctx context.Context, req *deliveryv1.ListRegistryCatalogRequest) (*deliveryv1.ListRegistryCatalogResponse, error) {
	if req.GetProjectId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id: must not be empty")
	}
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	repos, err := svc.s.Engine.RegistryCatalog(ctx, req.GetProjectId())
	if err != nil {
		if errors.Is(err, engine.ErrRegistryUnavailable) {
			// 面缺席族 → 503（P0-7.2）：配置级 off 态不是内部故障——
			// engine 文案即用户面文案（含 set registry.addr 处置提示）。
			return nil, apperr.New("E_REGISTRY_UNAVAILABLE", "%s", err.Error()).WithCause(err)
		}
		return nil, apperr.New("E_INTERNAL", "%s", err.Error())
	}
	return &deliveryv1.ListRegistryCatalogResponse{Repositories: repos}, nil
}

// ListRegistryTags 列仓名下的 tag 事实；越纲仓名按 not found（不泄漏跨
// 项目存在性）。
func (svc *RegistryService) ListRegistryTags(ctx context.Context, req *deliveryv1.ListRegistryTagsRequest) (*deliveryv1.ListRegistryTagsResponse, error) {
	if req.GetProjectId() == "" || req.GetRepository() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and repository: must not be empty")
	}
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	tags, err := svc.s.Engine.RegistryTags(ctx, req.GetProjectId(), req.GetRepository())
	if err != nil {
		switch {
		case errors.Is(err, state.ErrNotFound):
			return nil, apperr.New("E_NOT_FOUND", "repository %s not found in project scope", req.GetRepository())
		case errors.Is(err, engine.ErrRegistryUnavailable):
			return nil, apperr.New("E_REGISTRY_UNAVAILABLE", "%s", err.Error()).WithCause(err)
		}
		return nil, apperr.New("E_INTERNAL", "%s", err.Error())
	}
	out := &deliveryv1.ListRegistryTagsResponse{Repository: req.GetRepository()}
	for _, tag := range tags {
		out.Tags = append(out.Tags, &deliveryv1.RegistryTag{
			Name: tag.Name, Digest: tag.Digest, SizeBytes: tag.SizeBytes, PushedAt: tag.PushedAt,
		})
	}
	return out, nil
}
