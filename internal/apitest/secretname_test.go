package apitest_test

// N1 收尾批 A3：Secret 名字符集受理面 + A7 normalizeDockerfile 入口校验
//（hooks 面）。路径逃逸形态在受理位拒绝（/run/secrets/<名> 文件目标）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/apitest"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func TestPutSecretNameCharset(t *testing.T) {
	h := apitest.New(t)
	ctx := sdk.WithToken(context.Background(), h.Token)
	projects := structurev1.NewProjectsServiceClient(h.Conn)
	secrets := structurev1.NewSecretsServiceClient(h.Conn)

	proj, err := projects.CreateProject(ctx, &structurev1.CreateProjectRequest{Name: "charset"})
	require.NoError(t, err)
	pid := proj.GetProject().GetId()

	ok, err := secrets.PutSecret(ctx, &structurev1.PutSecretRequest{ProjectId: pid, Name: "api.key-1", Value: "v"})
	require.NoError(t, err, "charset-compliant names stay accepted")
	assert.NotEmpty(t, ok.GetSecret().GetId())

	for _, bad := range []string{"../escape", "a/b", `a\b`, "sp ace", "..", "a..b", ".hidden"} {
		_, err := secrets.PutSecret(ctx, &structurev1.PutSecretRequest{ProjectId: pid, Name: bad, Value: "v"})
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "name %q must be rejected at intake", bad)
	}

	// 保留前缀的错误形态保持原样（reserved 拒先于字符集拒——既定契约）。
	_, err = secrets.PutSecret(ctx, &structurev1.PutSecretRequest{ProjectId: pid, Name: "database:pg", Value: "postgres://x"})
	assert.Equal(t, codes.FailedPrecondition, status.Code(err), "reserved database credential prefix keeps its conflict shape")
}
