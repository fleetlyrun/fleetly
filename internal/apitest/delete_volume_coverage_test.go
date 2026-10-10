package apitest

// DeleteVolume 覆盖（IA v3 二期⑤b，audit §10.2 guard C）：显式删除生效 +
// 活跃下级拒绝（Database 挂靠卷名命中——受理位引用预检的真形态）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestVolumeDeleteCovered(t *testing.T) {
	h := New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	volumes := structurev1.NewVolumesServiceClient(h.Conn)
	networks := structurev1.NewNetworksServiceClient(h.Conn)
	databases := structurev1.NewDatabasesServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "volshift"})
	require.NoError(t, err)
	projectID := proj.GetProject().GetId()

	// ---- 显式删除生效：create ×2 → delete → 列表只剩一个。----
	first, err := volumes.CreateVolume(ctx, &structurev1.CreateVolumeRequest{ProjectId: projectID, Name: "scratch"})
	require.NoError(t, err)
	_, err = volumes.CreateVolume(ctx, &structurev1.CreateVolumeRequest{ProjectId: projectID, Name: "keep"})
	require.NoError(t, err)
	_, err = volumes.DeleteVolume(ctx, &structurev1.DeleteVolumeRequest{Id: first.GetVolume().GetId()})
	require.NoError(t, err)
	list, err := volumes.ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: projectID})
	require.NoError(t, err)
	require.Len(t, list.GetVolumes(), 1, "deleted volume must leave the list")
	require.Equal(t, "keep", list.GetVolumes()[0].GetName())

	// tombstone 后同 ID 再删 = not found（活跃行口径）。
	_, err = volumes.DeleteVolume(ctx, &structurev1.DeleteVolumeRequest{Id: first.GetVolume().GetId()})
	require.Equal(t, codes.NotFound, status.Code(err), "deleting a tombstoned volume must 404")

	// ---- 活跃下级拒绝：Database 挂靠卷（名公式 = 数据库名）命中。----
	// （harness 建项目即带 default 网络——已存在即复用。）
	_, err = networks.CreateNetwork(ctx, &structurev1.CreateNetworkRequest{ProjectId: projectID, Name: "default"})
	if status.Code(err) != codes.AlreadyExists {
		require.NoError(t, err)
	}
	_, err = databases.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{ProjectId: projectID, Name: "main", Engine: "postgres"})
	require.NoError(t, err)
	data, err := volumes.CreateVolume(ctx, &structurev1.CreateVolumeRequest{ProjectId: projectID, Name: "main"})
	require.NoError(t, err)
	_, err = volumes.DeleteVolume(ctx, &structurev1.DeleteVolumeRequest{Id: data.GetVolume().GetId()})
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "referenced volume must be refused (E_CONFLICT)")
	require.Contains(t, status.Convert(err).Message(), "still referenced")
	require.Contains(t, status.Convert(err).Message(), "database main")
}
