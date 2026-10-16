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
)

func TestVolumeDeleteCovered(t *testing.T) {
	h := New(t)
	ctx := h.Ctx()
	volumes := structurev1.NewVolumesServiceClient(h.Conn)
	networks := structurev1.NewNetworksServiceClient(h.Conn)
	databases := structurev1.NewDatabasesServiceClient(h.Conn)

	projectID := h.CreateProject(t, "volshift").GetId()

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
	// Database 的数据卷行由 reconcile 环异步物化——环先到时同名行已在
	//（CI 负载窗实证 2026-10-16，本地竞速恒赢掩蔽）。引用拒绝执法按名
	// 公式（数据库名）不看行属主，两种归属下断言等价，环先到即取既有行。
	data, err := volumes.CreateVolume(ctx, &structurev1.CreateVolumeRequest{ProjectId: projectID, Name: "main"})
	if status.Code(err) == codes.AlreadyExists {
		data, err = existingVolumeByName(t, volumes, ctx, projectID, "main")
	}
	require.NoError(t, err)
	require.NotNil(t, data, "volume row named main must exist either way")
	_, err = volumes.DeleteVolume(ctx, &structurev1.DeleteVolumeRequest{Id: data.GetVolume().GetId()})
	require.Equal(t, codes.FailedPrecondition, status.Code(err), "referenced volume must be refused (E_CONFLICT)")
	require.Contains(t, status.Convert(err).Message(), "still referenced")
	require.Contains(t, status.Convert(err).Message(), "database main")
}

// existingVolumeByName 按名取项目内既有卷行（环先到归属场景的兜底取行）；
// 找不到即测试失败（断言前提：卷行必在）。
func existingVolumeByName(t *testing.T, volumes structurev1.VolumesServiceClient, ctx context.Context, projectID, name string) (*structurev1.CreateVolumeResponse, error) {
	t.Helper()
	list, err := volumes.ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: projectID})
	if err != nil {
		return nil, err
	}
	for _, v := range list.GetVolumes() {
		if v.GetName() == name {
			return &structurev1.CreateVolumeResponse{Volume: v}, nil
		}
	}
	return nil, status.Errorf(codes.NotFound, "volume %q not found in project %s", name, projectID)
}
