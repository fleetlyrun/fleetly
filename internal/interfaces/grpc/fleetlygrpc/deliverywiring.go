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
		Deploy: s.Modules.Deploy, Deployments: s.Delivery.Deployments, Revisions: s.Delivery.Revisions, Builds: s.Delivery.Builds,
		Uploads: s.Delivery.Uploads, UploadStore: func() sourceupload.Store { return s.Clients.UploadStore },
		Logging: func() capability.Logging { return s.Core.Logging },
		Apps:    s.Structure.Apps, Projects: lateProjectRepo{s}, Volumes: s.Structure.Volumes,
		SharedVariables: s.Structure.SharedVariables, AppVariables: s.Structure.AppVariables, Hooks: s.Delivery.Hooks,
		Registry: s.Modules.Registry, Auth: s,
		Commit:             func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		MintConfirm:        s.MintConfirm,
		ParentProjectAlive: s.parentProjectAlive,
		Wait:               waitSource(s),
	}
}
