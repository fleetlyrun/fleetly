package datagrpc

import "github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"

// RegisterEventSchemas 登记数据服务上下文事件的 payload 形状（database/
// pubsub/bucket_store 及子资源、browse 会话）。
func RegisterEventSchemas(reg grpcapi.EventSchemaRegistrar) {
	for _, name := range []string{
		EventDatabaseCreated, EventDatabaseDeleted, EventDatabaseRotated, EventDatabaseUpdated,
		EventPubSubCreated, EventPubSubUpdated, EventPubSubDeleted, EventPubSubRotated,
		EventBucketStoreCreated, EventBucketStoreUpdated, EventBucketStoreDeleted, EventBucketStoreRotated,
		EventTopicCreated, EventTopicUpdated, EventTopicDeleted,
		EventStreamCreated, EventStreamUpdated, EventStreamDeleted,
		EventBucketCreated, EventBucketUpdated, EventBucketDeleted,
		EventDatabaseBrowserOpened,
	} {
		reg(name, grpcapi.StructureEventPayload{})
	}
}
