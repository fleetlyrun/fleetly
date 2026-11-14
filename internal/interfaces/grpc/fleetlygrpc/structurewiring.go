package fleetlygrpc

// structurewiring.go 是 structuregrpc 的组合根投影：Services 的宽依赖在此
// 收窄为 structuregrpc.Deps，供注册与模板复合编排共用。

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/structuregrpc"
)

func (s *Services) structureDeps() structuregrpc.Deps {
	return structuregrpc.Deps{
		Projects: lateProjectRepo{s}, Apps: s.Structure.Apps, Secrets: s.Structure.Secrets, Configs: s.Structure.Configs,
		SharedVariables: s.Structure.SharedVariables, AppVariables: s.Structure.AppVariables, Volumes: s.Structure.Volumes,
		Networks: s.Structure.Networks, NetworkPeers: s.Structure.NetworkPeers, Revisions: s.Delivery.Revisions,
		Deployments: s.Delivery.Deployments, Databases: s.Data.Databases, Runtime: s.Core.Runtime,
		Anchor: s.Core.Anchor, System: s.Modules.System, Nodes: s.Modules.Nodes, Cipher: s.Core.Cipher, Deploy: s.Modules.Deploy, Log: s.Core.Log,
		Commit:              func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		MintConfirm:         s.MintConfirm,
		PeekConfirm:         s.PeekConfirm,
		DiscardConfirm:      s.DiscardConfirm,
		CascadeDatabases:    s.cascadeDeleteDatabases,
		CascadePubSubs:      s.cascadeDeletePubSubs,
		CascadeBucketStores: s.cascadeDeleteBucketStores,
	}
}
