package fleetlygrpc

// deliverywiring.go 是 deliverygrpc 的组合根投影：Services 的宽依赖在此
// 收窄为 deliverygrpc.Deps。

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/deliverygrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

func (s *Services) deliveryDeps() deliverygrpc.Deps {
	return deliverygrpc.Deps{
		Deploy: s.Deploy, Deployments: s.Deployments, Revisions: s.Revisions, Builds: s.Builds,
		Uploads: s.Uploads, UploadStore: func() sourceupload.Store { return s.UploadStore },
		Logging: func() capability.Logging { return s.Logging },
		Apps:    s.Apps, Projects: lateProjectRepo{s}, Volumes: s.Volumes,
		SharedVariables: s.SharedVariables, AppVariables: s.AppVariables, Hooks: s.Hooks,
		Registry: s.Registry, Auth: s,
		Commit:             func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		MintConfirm:        s.MintConfirm,
		ParentProjectAlive: s.parentProjectAlive,
		Wait:               waitSource(s),
	}
}
