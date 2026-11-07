package apitest_test

// 子资源管理面 e2e（B8，ADR-0096 决策 10 / design §4.7）：Topic（kafka 协议
// 族）/Stream（nats）/Bucket（silo）三面的受理链——创建/列举/Update 确认
// 闭环（预演 diff + 铸票 + 单用途票）/删除披露行（wide）与声明式 GC 落行；
// 跨引擎可用性拒（kafka → Stream 面 / nats → Topic 面的 E_RUNTIME_FACE_
// UNSUPPORTED）。收敛段的实况 ensure 由 dataservices 单测与 e2e dind 段承载
// （本面 fake 工具容器无引擎实况）。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"

	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

func TestTopicLifecycle(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	projectID := h.CreateProject(t, "kafkabus").GetId()
	pubsubs := structurev1.NewPubSubsServiceClient(h.Conn)

	kafka, err := pubsubs.CreatePubSub(ctx, &structurev1.CreatePubSubRequest{
		ProjectId: projectID, Name: "kbus", Engine: "kafka",
	})
	require.NoError(t, err)
	kafkaID := kafka.GetPubsub().GetId()

	// nats 实例上的 Topic 面 = 引擎方言可用性拒（501 族——G17）。
	nats, err := pubsubs.CreatePubSub(ctx, &structurev1.CreatePubSubRequest{
		ProjectId: projectID, Name: "nbus", Engine: "nats",
	})
	require.NoError(t, err)
	_, err = pubsubs.CreateTopic(ctx, &structurev1.CreateTopicRequest{
		PubsubId: nats.GetPubsub().GetId(), Name: "orders",
	})
	require.Error(t, err)
	assert.Equal(t, codes.Unimplemented, status.Code(err), "E_RUNTIME_FACE_UNSUPPORTED maps to Unimplemented")
	assert.Contains(t, err.Error(), "topic face belongs to kafka-protocol engines")

	created, err := pubsubs.CreateTopic(ctx, &structurev1.CreateTopicRequest{
		PubsubId: kafkaID, Name: "orders", Partitions: 3, RetentionMs: 3600000,
	})
	require.NoError(t, err)
	row := created.GetTopic()
	assert.NotEmpty(t, row.GetId())
	assert.Equal(t, int32(3), row.GetPartitions())
	assert.Equal(t, int64(3600000), row.GetRetentionMs())
	assert.Equal(t, "pending", row.GetStatus())

	// 名字值域拒 + 同父同名冲突。
	_, err = pubsubs.CreateTopic(ctx, &structurev1.CreateTopicRequest{PubsubId: kafkaID, Name: "bad name"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = pubsubs.CreateTopic(ctx, &structurev1.CreateTopicRequest{PubsubId: kafkaID, Name: "orders"})
	assert.Equal(t, codes.AlreadyExists, status.Code(err))

	list, err := pubsubs.ListTopics(ctx, &structurev1.ListTopicsRequest{PubsubId: kafkaID})
	require.NoError(t, err)
	require.Len(t, list.GetTopics(), 1)

	// Update 确认闭环：无票拒 → 分区减拒（方言层受理半边）→ 预演 diff +
	// 铸票 → 带票落行 → 票单用途。
	updateReq := &structurev1.UpdateTopicRequest{PubsubId: kafkaID, TopicId: row.GetId(), Partitions: int32Ptr(5)}
	_, err = pubsubs.UpdateTopic(ctx, updateReq)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CONFIRMATION_REQUIRED")
	shrink := &structurev1.UpdateTopicRequest{PubsubId: kafkaID, TopicId: row.GetId(), Partitions: int32Ptr(2),
		Confirmation: &deliveryv1.Confirmation{Skip: true}}
	_, err = pubsubs.UpdateTopic(ctx, shrink)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only increase")

	preview, err := pubsubs.PreviewUpdateTopic(ctx, &structurev1.PreviewUpdateTopicRequest{Update: updateReq})
	require.NoError(t, err)
	require.Len(t, preview.GetChanges(), 1)
	assert.Equal(t, "partitions", preview.GetChanges()[0].GetKey())
	assert.Equal(t, "3", preview.GetChanges()[0].GetFrom())
	assert.Equal(t, "5", preview.GetChanges()[0].GetTo())
	assert.NotEmpty(t, preview.GetTicket())

	updateReq.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
	updated, err := pubsubs.UpdateTopic(ctx, updateReq)
	require.NoError(t, err)
	assert.Equal(t, int32(5), updated.GetTopic().GetPartitions())
	assert.Contains(t, updated.GetConvergedNote(), "convergence segment")
	_, err = pubsubs.UpdateTopic(ctx, updateReq)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CONFIRMATION_MISMATCH", "tickets are single-use")

	// 删除披露行（wide）：无票拒 → 预演（kafka CLI 无数据量面——如实披露）
	// → 带票 tombstone → 行不可见。
	_, err = pubsubs.DeleteTopic(ctx, &structurev1.DeleteTopicRequest{PubsubId: kafkaID, TopicId: row.GetId()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CONFIRMATION_REQUIRED")
	del, err := pubsubs.PreviewDeleteTopic(ctx, &structurev1.PreviewDeleteTopicRequest{PubsubId: kafkaID, TopicId: row.GetId()})
	require.NoError(t, err)
	assert.Contains(t, del.GetDisclosure(), "unrecoverable")
	assert.NotEmpty(t, del.GetTicket())
	_, err = pubsubs.DeleteTopic(ctx, &structurev1.DeleteTopicRequest{
		PubsubId: kafkaID, TopicId: row.GetId(), Confirmation: &deliveryv1.Confirmation{Ticket: del.GetTicket()},
	})
	require.NoError(t, err)
	list, err = pubsubs.ListTopics(ctx, &structurev1.ListTopicsRequest{PubsubId: kafkaID})
	require.NoError(t, err)
	assert.Empty(t, list.GetTopics(), "the tombstoned row is invisible on the user face")
}

func TestStreamLifecycle(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	projectID := h.CreateProject(t, "natsbus").GetId()
	pubsubs := structurev1.NewPubSubsServiceClient(h.Conn)

	nats, err := pubsubs.CreatePubSub(ctx, &structurev1.CreatePubSubRequest{
		ProjectId: projectID, Name: "nbus", Engine: "nats",
	})
	require.NoError(t, err)
	natsID := nats.GetPubsub().GetId()

	// kafka 实例上的 Stream 面 = 引擎方言可用性拒。
	kafka, err := pubsubs.CreatePubSub(ctx, &structurev1.CreatePubSubRequest{
		ProjectId: projectID, Name: "kbus", Engine: "kafka",
	})
	require.NoError(t, err)
	_, err = pubsubs.CreateStream(ctx, &structurev1.CreateStreamRequest{
		PubsubId: kafka.GetPubsub().GetId(), Name: "ORDERS",
	})
	require.Error(t, err)
	assert.Equal(t, codes.Unimplemented, status.Code(err))

	created, err := pubsubs.CreateStream(ctx, &structurev1.CreateStreamRequest{
		PubsubId: natsID, Name: "ORDERS", Subjects: []string{"orders.>"},
		Retention: "limits", Storage: "file", MaxStreamSize: 1048576,
	})
	require.NoError(t, err)
	row := created.GetStream()
	assert.Equal(t, []string{"orders.>"}, row.GetSubjects())
	assert.Equal(t, "limits", row.GetRetention())
	assert.Equal(t, "file", row.GetStorage())
	assert.Equal(t, int64(1048576), row.GetMaxStreamSize())

	// 轴值域拒。
	_, err = pubsubs.CreateStream(ctx, &structurev1.CreateStreamRequest{
		PubsubId: natsID, Name: "BAD", Retention: "forever",
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	got, err := pubsubs.GetStream(ctx, &structurev1.GetStreamRequest{PubsubId: natsID, StreamId: row.GetId()})
	require.NoError(t, err)
	assert.Equal(t, "ORDERS", got.GetStream().GetName())
	list, err := pubsubs.ListStreams(ctx, &structurev1.ListStreamsRequest{PubsubId: natsID})
	require.NoError(t, err)
	require.Len(t, list.GetStreams(), 1)

	// Update 确认闭环：subjects 全量替换 + max 撤上限。
	streamUpdate := &structurev1.UpdateStreamRequest{
		PubsubId: natsID, StreamId: row.GetId(),
		Subjects:      []string{"orders.>", "bills.>"},
		MaxStreamSize: int64Ptr(0),
	}
	_, err = pubsubs.UpdateStream(ctx, streamUpdate)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CONFIRMATION_REQUIRED")
	preview, err := pubsubs.PreviewUpdateStream(ctx, &structurev1.PreviewUpdateStreamRequest{Update: streamUpdate})
	require.NoError(t, err)
	require.Len(t, preview.GetChanges(), 2)
	streamUpdate.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
	updated, err := pubsubs.UpdateStream(ctx, streamUpdate)
	require.NoError(t, err)
	assert.Equal(t, []string{"bills.>", "orders.>"}, updated.GetStream().GetSubjects())
	assert.Equal(t, int64(0), updated.GetStream().GetMaxStreamSize(), "0 = unlimited after the clear")

	// 删除披露行（wide）+ tombstone。
	del, err := pubsubs.PreviewDeleteStream(ctx, &structurev1.PreviewDeleteStreamRequest{PubsubId: natsID, StreamId: row.GetId()})
	require.NoError(t, err)
	assert.Contains(t, del.GetDisclosure(), "unrecoverable")
	_, err = pubsubs.DeleteStream(ctx, &structurev1.DeleteStreamRequest{
		PubsubId: natsID, StreamId: row.GetId(), Confirmation: &deliveryv1.Confirmation{Ticket: del.GetTicket()},
	})
	require.NoError(t, err)
	list, err = pubsubs.ListStreams(ctx, &structurev1.ListStreamsRequest{PubsubId: natsID})
	require.NoError(t, err)
	assert.Empty(t, list.GetStreams())
}

func TestBucketLifecycle(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	projectID := h.CreateProject(t, "storage").GetId()
	stores := structurev1.NewBucketStoresServiceClient(h.Conn)

	store, err := stores.CreateBucketStore(ctx, &structurev1.CreateBucketStoreRequest{
		ProjectId: projectID, Name: "objects", Engine: "silo",
	})
	require.NoError(t, err)
	storeID := store.GetBucketStore().GetId()

	created, err := stores.CreateBucket(ctx, &structurev1.CreateBucketRequest{
		BucketStoreId: storeID, Name: "media", QuotaBytes: 1073741824,
	})
	require.NoError(t, err)
	row := created.GetBucket()
	assert.Equal(t, int64(1073741824), row.GetQuotaBytes())
	assert.Equal(t, "pending", row.GetStatus())

	// S3 名值域拒 + 同父同名冲突。
	_, err = stores.CreateBucket(ctx, &structurev1.CreateBucketRequest{BucketStoreId: storeID, Name: "UPPER"})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = stores.CreateBucket(ctx, &structurev1.CreateBucketRequest{BucketStoreId: storeID, Name: "media"})
	assert.Equal(t, codes.AlreadyExists, status.Code(err))

	list, err := stores.ListBuckets(ctx, &structurev1.ListBucketsRequest{BucketStoreId: storeID})
	require.NoError(t, err)
	require.Len(t, list.GetBuckets(), 1)

	// Update 确认闭环（quota diff）。
	updateReq := &structurev1.UpdateBucketRequest{BucketStoreId: storeID, BucketId: row.GetId(), QuotaBytes: int64Ptr(0)}
	_, err = stores.UpdateBucket(ctx, updateReq)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_CONFIRMATION_REQUIRED")
	preview, err := stores.PreviewUpdateBucket(ctx, &structurev1.PreviewUpdateBucketRequest{Update: updateReq})
	require.NoError(t, err)
	require.Len(t, preview.GetChanges(), 1)
	assert.Equal(t, "quota_bytes", preview.GetChanges()[0].GetKey())
	updateReq.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
	updated, err := stores.UpdateBucket(ctx, updateReq)
	require.NoError(t, err)
	assert.Equal(t, int64(0), updated.GetBucket().GetQuotaBytes())

	// 删除披露行（wide）+ tombstone。
	del, err := stores.PreviewDeleteBucket(ctx, &structurev1.PreviewDeleteBucketRequest{BucketStoreId: storeID, BucketId: row.GetId()})
	require.NoError(t, err)
	assert.Contains(t, del.GetDisclosure(), "unrecoverable")
	_, err = stores.DeleteBucket(ctx, &structurev1.DeleteBucketRequest{
		BucketStoreId: storeID, BucketId: row.GetId(), Confirmation: &deliveryv1.Confirmation{Ticket: del.GetTicket()},
	})
	require.NoError(t, err)
	list, err = stores.ListBuckets(ctx, &structurev1.ListBucketsRequest{BucketStoreId: storeID})
	require.NoError(t, err)
	assert.Empty(t, list.GetBuckets())

	// 父实例删除级联：子资源行随之清理（DeleteBucketStore 的 tombstone 事务
	// 内硬删——重建同父后旧行不可见）。
	_, err = stores.CreateBucket(ctx, &structurev1.CreateBucketRequest{BucketStoreId: storeID, Name: "media"})
	require.NoError(t, err)
	_, err = stores.DeleteBucketStore(ctx, &structurev1.DeleteBucketStoreRequest{Id: storeID})
	require.NoError(t, err)
}

// int32Ptr / int64Ptr 是 proto optional 标量指针帮手。
func int32Ptr(v int32) *int32 { return &v }
func int64Ptr(v int64) *int64 { return &v }
