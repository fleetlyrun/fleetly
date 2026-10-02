package apitest_test

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	"github.com/fleetlyrun/fleetly/internal/upload"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// uploadTar 经 client-streaming 上传一段字节（F1.10，ADR-0019 附录 A）。
// 服务端中途拒绝（如冻结首帧执法）会让后续 Send 失败——仍走 CloseAndRecv
// 取回真实 RPC 状态，错误不被客户端 EOF 掩蔽。
func uploadTar(ctx context.Context, c deliveryv1.BuildsServiceClient, projectID string, body []byte, chunkSize int) (*deliveryv1.UploadSourceResponse, error) {
	stream, err := c.UploadSource(ctx)
	if err != nil {
		return nil, err
	}
	if err := stream.Send(&deliveryv1.UploadSourceRequest{
		Part: &deliveryv1.UploadSourceRequest_Meta{Meta: &deliveryv1.UploadSourceMeta{ProjectId: projectID}},
	}); err != nil {
		resp, cerr := stream.CloseAndRecv()
		if cerr != nil {
			return nil, cerr
		}
		return resp, err
	}
	var sendErr error
	for i := 0; i < len(body); i += chunkSize {
		end := i + chunkSize
		if end > len(body) {
			end = len(body)
		}
		if serr := stream.Send(&deliveryv1.UploadSourceRequest{
			Part: &deliveryv1.UploadSourceRequest_Chunk{Chunk: body[i:end]},
		}); serr != nil {
			sendErr = serr
			break
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return nil, err
	}
	if sendErr != nil {
		return nil, sendErr
	}
	return resp, nil
}

// buildFixtureTar 构造最小构建上下文 tar（Dockerfile + app 文件）。
func buildFixtureTar() []byte {
	// 上传面接受任意合法 tar 流——确定性归一是 CLI 去重的客户端前提，
	// 服务端只认内容寻址。
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: "Dockerfile", Mode: 0o644, Size: int64(len(fixtureDockerfile)), ModTime: time.Unix(0, 0).UTC()})
	_, _ = tw.Write([]byte(fixtureDockerfile))
	_ = tw.WriteHeader(&tar.Header{Name: "app.txt", Mode: 0o644, Size: int64(len(fixtureApp)), ModTime: time.Unix(0, 0).UTC()})
	_, _ = tw.Write([]byte(fixtureApp))
	_ = tw.Close()
	return buf.Bytes()
}

const (
	fixtureDockerfile = "FROM alpine:3.20\nCMD [\"/bin/true\"]\n"
	fixtureApp        = "hello upload\n"
)

// TestUploadSourceLifecycle：上传 → 重传去重（同 digest 同 id）→ list →
// deploy upload 形态全链（构建输入=解包后目录）→ 跨项目引用拒绝。
func TestUploadSourceLifecycle(t *testing.T) {
	h := apitest.NewManual(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	builds := deliveryv1.NewBuildsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "upload-main"})
	require.NoError(t, err)
	projID := proj.GetProject().GetId()
	app, err := apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projID, Name: "web"})
	require.NoError(t, err)
	appID := app.GetApp().GetId()

	body := buildFixtureTar()
	resp, err := uploadTar(owner, builds, projID, body, 4)
	require.NoError(t, err)
	assert.NotEmpty(t, resp.GetId())
	assert.NotEmpty(t, resp.GetDigest())
	assert.EqualValues(t, len(body), resp.GetSizeBytes())
	assert.False(t, resp.GetDeduplicated())

	// 同字节重传：同 digest 同 id，deduplicated=true（内容寻址天然幂等——
	// ADR-0019 附录 A.2 的幂等豁免依据）。
	again, err := uploadTar(owner, builds, projID, body, 16)
	require.NoError(t, err)
	assert.Equal(t, resp.GetId(), again.GetId())
	assert.True(t, again.GetDeduplicated())

	// List 读面。
	list, err := builds.ListUploads(owner, &deliveryv1.ListUploadsRequest{ProjectId: projID})
	require.NoError(t, err)
	require.Len(t, list.GetUploads(), 1)
	assert.Equal(t, resp.GetId(), list.GetUploads()[0].GetId())

	// deploy upload 形态：Revision 冻结 upload 引用 → building 驱动解包 →
	// FakeBuilder 构建输入 = 解包后的上下文目录 → releasing/observing 手动
	// 推进至 succeeded（FakeRuntime 观测注入，promoteToSucceeded 同款）。
	dep, err := deployments.Deploy(owner, &deliveryv1.DeployRequest{AppId: appID, UploadId: resp.GetId()})
	require.NoError(t, err)
	depID := dep.GetDeployment().GetId()

	succeeded := false
	for i := 0; i < 30 && !succeeded; i++ {
		h.Drive(owner)
		h.Runtime.ReportRunning(appID+"-web", 1)
		h.Clock.Advance(120 * time.Second)
		h.Drive(owner)
		got, gerr := deployments.GetDeployment(owner, &deliveryv1.GetDeploymentRequest{Id: depID})
		if gerr == nil && got.GetDeployment().GetState() == "succeeded" {
			succeeded = true
		}
	}
	require.True(t, succeeded, "upload deploy must converge to succeeded")

	calls := h.Builder.Calls()
	require.NotEmpty(t, calls)
	ctxDir := calls[len(calls)-1].ContextDir
	dockerfile, rerr := os.ReadFile(filepath.Join(ctxDir, "Dockerfile")) //nolint:gosec // 测试断言构建输入
	require.NoError(t, rerr)
	assert.Equal(t, fixtureDockerfile, string(dockerfile))
	appBody, rerr := os.ReadFile(filepath.Join(ctxDir, "app.txt")) //nolint:gosec // 测试断言构建输入
	require.NoError(t, rerr)
	assert.Equal(t, fixtureApp, string(appBody))
	assert.Equal(t, "Dockerfile", calls[len(calls)-1].Dockerfile)
}

// TestUploadSourceLimits：单上传上限（诚实 E_UPLOAD_TOO_LARGE 而非不透明
// RESOURCE_EXHAUSTED）与项目存量配额（E_QUOTA_EXCEEDED）。
func TestUploadSourceLimits(t *testing.T) {
	h := apitest.NewManual(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	builds := deliveryv1.NewBuildsServiceClient(h.Conn)

	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "upload-limits"})
	require.NoError(t, err)
	projID := proj.GetProject().GetId()

	// 测试注入小限额（生产缺省 512MiB/4GiB；字段替换即生效——服务持有同
	// 一 Services 指针）。
	h.Services.UploadStore = upload.NewStore(h.DataRoot, 64, 96)

	_, err = uploadTar(owner, builds, projID, bytes.Repeat([]byte("x"), 65), 16)
	require.Error(t, err)
	assert.Equal(t, codes.ResourceExhausted, status.Code(err))
	assert.Contains(t, err.Error(), "E_UPLOAD_TOO_LARGE")
	assert.Contains(t, err.Error(), "limit")

	// 存量配额：64 字节后，再传 33 字节（和 > 96）拒。
	_, err = uploadTar(owner, builds, projID, bytes.Repeat([]byte("y"), 64), 16)
	require.NoError(t, err)
	_, err = uploadTar(owner, builds, projID, bytes.Repeat([]byte("z"), 33), 16)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_QUOTA_EXCEEDED")
	// 同 digest 重传不占新配额（幂等路径先于配额检查）。
	_, err = uploadTar(owner, builds, projID, bytes.Repeat([]byte("y"), 64), 16)
	require.NoError(t, err)
}

// TestUploadSourceFreeze：冻结期间上传被拒（首帧执法——流式链 authn→freeze）。
func TestUploadSourceFreeze(t *testing.T) {
	h := apitest.NewManual(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	teams := identityv1.NewTeamsServiceClient(h.Conn)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	builds := deliveryv1.NewBuildsServiceClient(h.Conn)
	gov := systemv1.NewGovernanceServiceClient(h.Conn)

	team, err := teams.CreateTeam(owner, &identityv1.CreateTeamRequest{Name: "upload-freeze"})
	require.NoError(t, err)
	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "frozen-src", TeamId: team.GetTeam().GetId()})
	require.NoError(t, err)

	_, err = gov.SetChangeFreeze(owner, &systemv1.SetChangeFreezeRequest{TeamId: team.GetTeam().GetId(), Reason: "upload freeze window"})
	require.NoError(t, err)

	_, err = uploadTar(owner, builds, proj.GetProject().GetId(), buildFixtureTar(), 8)
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))
	assert.Contains(t, err.Error(), "E_CHANGE_FROZEN")
}

// TestDeployUploadCrossProject：Upload 是 project 级材料——跨项目引用拒绝。
func TestDeployUploadCrossProject(t *testing.T) {
	h := apitest.NewManual(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	apps := structurev1.NewAppsServiceClient(h.Conn)
	builds := deliveryv1.NewBuildsServiceClient(h.Conn)
	deployments := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	projA, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "src-project"})
	require.NoError(t, err)
	projB, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "other-project"})
	require.NoError(t, err)
	appB, err := apps.CreateApp(owner, &structurev1.CreateAppRequest{ProjectId: projB.GetProject().GetId(), Name: "web"})
	require.NoError(t, err)

	up, err := uploadTar(owner, builds, projA.GetProject().GetId(), buildFixtureTar(), 8)
	require.NoError(t, err)

	_, err = deployments.Deploy(owner, &deliveryv1.DeployRequest{AppId: appB.GetApp().GetId(), UploadId: up.GetId()})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "belongs to project")
}

// TestDeployUploadBadFrames：契约执法——首帧必须是 meta；meta 只许一次；
// 空流拒绝。
func TestDeployUploadBadFrames(t *testing.T) {
	h := apitest.NewManual(t)
	owner := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	builds := deliveryv1.NewBuildsServiceClient(h.Conn)

	proj, err := projects.CreateProject(owner, &structurev1.CreateProjectRequest{Name: "upload-bad"})
	require.NoError(t, err)
	projID := proj.GetProject().GetId()

	// 首帧是 chunk → 拒。
	stream, err := builds.UploadSource(owner)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&deliveryv1.UploadSourceRequest{
		Part: &deliveryv1.UploadSourceRequest_Chunk{Chunk: []byte("x")},
	}))
	_, err = stream.CloseAndRecv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "E_INVALID_ARGUMENT")

	// meta 二次出现 → 拒。
	stream, err = builds.UploadSource(owner)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&deliveryv1.UploadSourceRequest{
		Part: &deliveryv1.UploadSourceRequest_Meta{Meta: &deliveryv1.UploadSourceMeta{ProjectId: projID}},
	}))
	require.NoError(t, stream.Send(&deliveryv1.UploadSourceRequest{
		Part: &deliveryv1.UploadSourceRequest_Meta{Meta: &deliveryv1.UploadSourceMeta{ProjectId: projID}},
	}))
	_, err = stream.CloseAndRecv()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata frame may only appear first")

	// 空 body（仅 meta）→ 拒。
	_, err = uploadTar(owner, builds, projID, nil, 8)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no bytes")
}
