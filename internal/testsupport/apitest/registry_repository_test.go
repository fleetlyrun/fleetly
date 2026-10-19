package apitest_test

// Registry tags 仓名归属受理面（R03-M13）：repository 收敛为平台仓名
// 双段小写白名单——路径段注入（`../`、`%2e%2e`、`?`、`#`、空段）在
// 受理位即 E_INVALID_ARGUMENT（400），不是运行期 E_INTERNAL 500；越纲
// 形态仍按 not found（404，不泄漏跨项目存在性）。engine 层的"不透传到
// 上游"断言由 registrycontent_test.go 承载（本夹具假仓无 content 子面）。

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

func TestRegistryRepositoryRejectsPathInjection(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	projectID := h.CreateProject(t, "registry-injection").GetId()
	prefix := strings.ToLower(projectID)

	for _, repo := range []string{
		prefix + "/../other-project/app",
		prefix + "/%2e%2e",
		prefix + "/..",
		prefix + "/?x=1",
		prefix + "/#frag",
		prefix + "/",
		prefix + "//web",
	} {
		_, err := deliveryv1.NewRegistryServiceClient(h.Conn).ListRegistryTags(ctx,
			&deliveryv1.ListRegistryTagsRequest{ProjectId: projectID, Repository: repo})
		require.Error(t, err, "repository %q must be rejected at admission", repo)
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "repository %q", repo)
		assert.Equal(t, "E_INVALID_ARGUMENT", appErrCode(t, err), "repository %q", repo)
	}

	// 形态合法但越纲：仍按 not found（不泄漏他项目仓是否存在）。
	_, err := deliveryv1.NewRegistryServiceClient(h.Conn).ListRegistryTags(ctx,
		&deliveryv1.ListRegistryTagsRequest{ProjectId: projectID, Repository: "01jd0other0000000000000000/web"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err))
	assert.Equal(t, "E_NOT_FOUND", appErrCode(t, err))
}
