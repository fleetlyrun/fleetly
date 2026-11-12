package fleetlygrpc

// datawiring.go 是 datagrpc 的组合根投影：Services 的宽依赖在此收窄为
// datagrpc.Deps，供注册、模板复合编排与项目删除级联共用。

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/datagrpc"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
)

func (s *Services) dataDeps() datagrpc.Deps {
	return datagrpc.Deps{
		Data: s.Data, Backup: s.Backup, Browse: s.Browse, Anchor: s.Anchor,
		Databases: s.Databases, PubSubs: s.PubSubs, BucketStores: s.BucketStores,
		Topics: s.Topics, Streams: s.Streams, Buckets: s.Buckets,
		Backups: s.Backups, Secrets: s.Secrets, Projects: s.Projects, Cipher: s.Cipher,
		Auth: s, Tickets: s, Clock: s.DB.Clock(),
		Commit: func(ctx context.Context, fact grpcapi.WriteFact) error { return s.commit(ctx, fact) },
	}
}

func (s *Services) cascadeDeleteDatabases(ctx context.Context, projectID string) error {
	return s.dataDeps().CascadeDeleteDatabases(ctx, projectID)
}

func (s *Services) cascadeDeletePubSubs(ctx context.Context, projectID string) error {
	return s.dataDeps().CascadeDeletePubSubs(ctx, projectID)
}

func (s *Services) cascadeDeleteBucketStores(ctx context.Context, projectID string) error {
	return s.dataDeps().CascadeDeleteBucketStores(ctx, projectID)
}
