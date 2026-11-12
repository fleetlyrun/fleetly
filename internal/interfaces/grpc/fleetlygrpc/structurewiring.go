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
		Projects: lateProjectRepo{s}, Apps: s.Apps, Secrets: s.Secrets, Configs: s.Configs,
		SharedVariables: s.SharedVariables, AppVariables: s.AppVariables, Volumes: s.Volumes,
		Networks: s.Networks, NetworkPeers: s.NetworkPeers, Revisions: s.Revisions,
		Deployments: s.Deployments, Databases: s.Databases, Runtime: s.Runtime,
		Anchor: s.Anchor, System: s.System, Nodes: s.Nodes, Cipher: s.Cipher, Deploy: s.Deploy, Log: s.Log,
		Commit:              func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
		MintConfirm:         s.MintConfirm,
		PeekConfirm:         s.PeekConfirm,
		DiscardConfirm:      s.DiscardConfirm,
		CascadeDatabases:    s.cascadeDeleteDatabases,
		CascadePubSubs:      s.cascadeDeletePubSubs,
		CascadeBucketStores: s.cascadeDeleteBucketStores,
	}
}
