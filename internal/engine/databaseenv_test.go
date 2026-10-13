package engine

// 托管库引用注入单测：database_refs → FLEETLY_DB_<NAME>_* env（值取凭证
// 单源连接串 + DatabaseDNSName 铸名；缺库精确失败带创建提示）。

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/spec"
)

func TestInjectDatabaseEnv(t *testing.T) {
	const (
		connectURL = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
		dbName     = "shop"                                                                    // 夹具常量（database_test 同源）
		dbID       = "01jd0db000000000000000000"
	)
	e, _, _, _, _ := newBackupFixture(t, "postgres", connectURL)
	ctx := context.Background()

	spec := &specv1.AppSpec{
		SchemaVersion: spec.SchemaVersion,
		App:           &specv1.AppRef{Id: "app-1", Project: tProjectID},
		Processes: []*specv1.ProcessSpec{
			{Name: "web", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"}, DatabaseRefs: []string{dbName}},
			{Name: "sidecar", ImageOrigin: &specv1.ProcessSpec_Image{Image: "busybox:1.36"}},
		},
	}
	ws := []capability.Workload{
		{ID: "w-web", Process: "web"},
		{ID: "w-sidecar", Process: "sidecar"},
	}

	require.NoError(t, e.injectDatabaseEnv(ctx, spec, ws, tProjectID))

	env := ws[0].Env
	require.NotNil(t, env)
	assert.Equal(t, "FLEETLY_DB_SHOP", "FLEETLY_DB_"+dbEnvToken(dbName))
	// 连接串单源回读：URL 可解析且密码/库与夹具一致。
	parsed, err := url.Parse(env["FLEETLY_DB_SHOP_URL"])
	require.NoError(t, err)
	assert.Equal(t, "db-01jd0db000000000000000000", parsed.Hostname())
	assert.Equal(t, "5432", parsed.Port())
	assert.Equal(t, "fleetly", parsed.User.Username())
	assert.Equal(t, "secretpw", env["FLEETLY_DB_SHOP_PASSWORD"])
	assert.True(t, strings.HasSuffix(env["FLEETLY_DB_SHOP_DATABASE"], "fleetly"))
	assert.Equal(t, "db-01jd0db000000000000000000", env["FLEETLY_DB_SHOP_HOST"])
	assert.Equal(t, "5432", env["FLEETLY_DB_SHOP_PORT"])
	assert.Equal(t, "fleetly", env["FLEETLY_DB_SHOP_USER"])
	// 未引用的进程零注入。
	assert.Empty(t, ws[1].Env)

	// 引用在册性：缺库精确失败并带创建提示。
	bad := &specv1.AppSpec{
		SchemaVersion: spec.SchemaVersion,
		App:           &specv1.AppRef{Id: "app-1", Project: tProjectID},
		Processes: []*specv1.ProcessSpec{
			{Name: "web", ImageOrigin: &specv1.ProcessSpec_Image{Image: "nginx:1.27"}, DatabaseRefs: []string{"nope"}},
		},
	}
	wsBad := []capability.Workload{{ID: "w-web", Process: "web"}}
	err = e.injectDatabaseEnv(ctx, bad, wsBad, tProjectID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "create the managed database first")

	// 密码不落 Spec（值只进 Workload Env——下发面），引用本身在册。
	assert.Empty(t, spec.GetProcesses()[0].GetEnv())
}
