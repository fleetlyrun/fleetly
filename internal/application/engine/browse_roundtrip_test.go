package engine

// 五引擎 browse 方言往返表（R01-H1†/R01-M1，S16-c6）：dbtemplate.ConnURL
// （连接串单真源）→ browseConnFromURL（引擎解出 Conn）→ dbbrowser.Render
// （方言渲染）全链必须成功——这是创建 API 受理成功后的真实链路，且免
// Docker。redis 的空用户名与 mongoku 的库段/authSource 都在本表内钉死。

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dbbrowser"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
)

func TestBrowseDialectRoundTripAllEngines(t *testing.T) {
	const (
		host     = "db-01jd0db000000000000000000"
		password = "s3cretPW123"
	)
	// 方言是否消费用户名（redis 无用户名概念——空 user 是冻结形态而非缺陷）。
	consumesUser := map[string]bool{
		"postgres": true,
		"pgvector": true,
		"redis":    false,
		"mysql":    true,
		"mongo":    true,
	}
	// 值域 totality：两注册表都覆盖 dbtemplate.Engines()，测试自身不失明。
	require.Len(t, consumesUser, len(dbtemplate.Engines()))

	for _, engine := range dbtemplate.Engines() {
		t.Run(engine, func(t *testing.T) {
			tpl, ok := dbtemplate.For(engine)
			require.True(t, ok)
			browser, ok := dbbrowser.For(engine)
			require.True(t, ok)

			connURL := tpl.ConnURL(host, password)
			conn, err := browseConnFromURL(connURL)
			require.NoError(t, err, "engine ConnURL must round-trip through browseConnFromURL: %s", connURL)
			assert.Equal(t, host, conn.Host)
			assert.Equal(t, tpl.Meta().Port, conn.Port, "ConnURL port must equal the template port")
			assert.Equal(t, password, conn.Password)
			assert.Equal(t, consumesUser[engine], conn.User != "",
				"dialect user expectation for %s (redis mints an empty user by design)", engine)

			// EnforcementNone 的方言（adminer）对 readonly 渲染 fail-closed——
			// 按方言层级选择受理面会传的形态。
			readonly := browser.ReadOnlyEnforcement() != dbbrowser.EnforcementNone
			r, err := browser.Render(conn, readonly)
			require.NoError(t, err,
				"browse render must accept the round-tripped Conn (create-then-browse chain) for engine %q", engine)
			assert.True(t, len(r.Env) > 0 || len(r.Files) > 0 || len(r.Command) > 0,
				"rendering must carry a dialect surface (env/files/command)")

			switch engine {
			case "redis":
				// 空 user 形态的 env 方言：host/port/dbIndex/password 逐字段。
				assert.Equal(t, "db:"+host+":6379:0:"+password, r.Env["REDIS_HOSTS"])
			case "mongo":
				// 库段 = authSource 缺省源（用户在业务库内铸造）。
				assert.Equal(t, "mongodb://fleetly:"+password+"@"+host+":27017/fleetly",
					r.Env["MONGOKU_DEFAULT_HOST"])
			}
		})
	}
}
