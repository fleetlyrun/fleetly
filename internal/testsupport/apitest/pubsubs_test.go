package apitest_test

// PubSubsService e2e（B4b，ADR-0096）：创建受理（engine 值域/配额/保留前缀）、
// 凭证 Secret 铸造（pubsub:<name>）与永不回显、收敛到 PubSub 域载体
// （fleetly-pub 载体名/pub-<id> 寻址由 Provider 翻译，此处断言 NamespaceRef
// 轴）、删除收口（载体拆除 + 卷与 Secret 残留）。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

func TestPubSubLifecycle(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	projectID := h.CreateProject(t, "bus").GetId()
	pubsubs := structurev1.NewPubSubsServiceClient(h.Conn)
	secrets := structurev1.NewSecretsServiceClient(h.Conn)
	volumes := structurev1.NewVolumesServiceClient(h.Conn)

	// 值域外 engine → InvalidArgument（列合法值）。
	_, err := pubsubs.CreatePubSub(ctx, &structurev1.CreatePubSubRequest{
		ProjectId: projectID, Name: "events", Engine: "kafka",
	})
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err))

	created, err := pubsubs.CreatePubSub(ctx, &structurev1.CreatePubSubRequest{
		ProjectId: projectID, Name: "events", Engine: "nats",
	})
	require.NoError(t, err)
	row := created.GetPubsub()
	assert.NotEmpty(t, row.GetId())
	assert.Equal(t, "nats", row.GetEngine())
	assert.Equal(t, "2.11", row.GetVersion(), "nats Default version")
	assert.Equal(t, "pubsub:events", row.GetCredentialsRef())
	assert.Equal(t, "pub-"+strings.ToLower(row.GetId()), row.GetHost())
	assert.Equal(t, int32(4222), row.GetPort())
	assert.Equal(t, "pending", row.GetStatus())

	// 凭证 Secret 已铸造（指纹面；值永不回显）。
	secretList, err := secrets.ListSecrets(ctx, &structurev1.ListSecretsRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, secretList.GetSecrets(), 1)
	assert.Equal(t, "pubsub:events", secretList.GetSecrets()[0].GetName())
	assert.NotEmpty(t, secretList.GetSecrets()[0].GetFingerprint())

	// 同名活跃行 → AlreadyExists；保留前缀 PutSecret → Conflict。
	_, err = pubsubs.CreatePubSub(ctx, &structurev1.CreatePubSubRequest{
		ProjectId: projectID, Name: "events", Engine: "nats",
	})
	assert.Equal(t, codes.AlreadyExists, status.Code(err))
	_, err = secrets.PutSecret(ctx, &structurev1.PutSecretRequest{
		ProjectId: projectID, Name: "pubsub:events", Value: "hijack",
	})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "E_CONFLICT maps to FailedPrecondition on gRPC")

	// 收敛：PubSub 轴 NamespaceRef + 挂项目网。
	h.Drive(ctx)
	calls := h.Runtime.Calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, capability.NamespaceRef{
		Team: "default", Project: projectID, PubSub: row.GetId(), PubSubName: "events",
	}, last.NS)
	w := last.Spec["nats"]
	require.NotNil(t, w)
	assert.Equal(t, []string{"default"}, w.Networks)

	// 观测 running → 状态列推进；卷行自愈补建（名 = 实例名）。
	h.Runtime.ReportRunning(row.GetId(), last.Gen)
	h.Drive(ctx)
	got, err := pubsubs.GetPubSub(ctx, &structurev1.GetPubSubRequest{Id: row.GetId()})
	require.NoError(t, err)
	assert.Equal(t, "running", got.GetPubsub().GetStatus())
	volList, err := volumes.ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, volList.GetVolumes(), 1)
	assert.Equal(t, "events", volList.GetVolumes()[0].GetName())

	// Update 期望态（static 旋钮落行）：在服门 + 不可变轴拒。
	_, err = pubsubs.UpdatePubSub(ctx, &structurev1.UpdatePubSubRequest{
		PubsubId: row.GetId(), Engine: "nats",
	})
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "engine is immutable")
	updated, err := pubsubs.UpdatePubSub(ctx, &structurev1.UpdatePubSubRequest{
		PubsubId: row.GetId(),
		Settings: &structurev1.DatabaseSettingsUpdate{Values: map[string]string{"jetstream_max_file": "2gb"}},
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"jetstream_max_file": "2gb"}, updated.GetPubsub().GetSettings())
	assert.Equal(t, []string{"jetstream_max_file"}, updated.GetStaticApplied())

	// Rotate 声明式轮换（在服前提）。
	rotated, err := pubsubs.RotatePubSubPassword(ctx, &structurev1.RotatePubSubPasswordRequest{PubsubId: row.GetId()})
	require.NoError(t, err)
	assert.Equal(t, "pubsub:events", rotated.GetPubsub().GetCredentialsRef(), "credential ref is stable; the value was replaced under it")

	// 删除收口：载体拆除 + tombstone；卷与凭证 Secret 残留。
	_, err = pubsubs.DeletePubSub(ctx, &structurev1.DeletePubSubRequest{Id: row.GetId()})
	require.NoError(t, err)
	assert.Contains(t, h.Runtime.Removed(), capability.NamespaceRef{
		Team: "default", Project: projectID, PubSub: row.GetId(), PubSubName: "events",
	})
	_, err = pubsubs.GetPubSub(ctx, &structurev1.GetPubSubRequest{Id: row.GetId()})
	assert.Equal(t, codes.NotFound, status.Code(err))
	_, err = pubsubs.DeletePubSub(ctx, &structurev1.DeletePubSubRequest{Id: row.GetId()})
	assert.Equal(t, codes.NotFound, status.Code(err), "再删 = 不存在（幂等口径）")
}
