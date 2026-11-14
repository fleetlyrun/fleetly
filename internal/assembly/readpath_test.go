package assembly

// 读径绑定的装配锚（SQLite 读写连接分离）：NewAPIDeps 的 API repo 集与
// Anchor 绑 db.Reader()（query_only 读池）——读可用、写大声失败；写缝
// （Deps.Tx / engine 写原语）仍在写柄上。误把写挂到读柄 repo 是 apitest
// 全套的回归对象，本测试是同一不变量在装配面的最小钉子。

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engine"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/services/anchor"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

func TestNewAPIDepsReadPathIsReadBound(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bind := NewBinder(db)
	eng := engine.New(engine.Deps{
		DB: db, Stores: bind(db.Runner()), Tx: transact.NewTx(db, bind), Logger: log,
	}, engine.Options{})
	deps := NewAPIDeps(db, bind, eng, nil, nil, nil, t.TempDir(), nil, log)

	// 生产写路径的替身：写柄落一行。
	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{
		ID: "01JD0PROJ0000000000000000", TeamID: "default", Name: "read-path",
	}))

	// 读：读柄 repo 正常。
	got, err := deps.Structure.Projects.Get(ctx, "01JD0PROJ0000000000000000")
	require.NoError(t, err)
	assert.Equal(t, "read-path", got.Name)

	// 写：读柄 repo 必须大声失败（query_only），不得静默落写。
	err = deps.Structure.Projects.Create(ctx, &project.Project{
		ID: "01JD0PROJ0000000000000001", TeamID: "default", Name: "must-fail",
	})
	require.Error(t, err, "a write through the read-bound API repo must fail loudly")
	assert.Contains(t, strings.ToLower(err.Error()), "readonly",
		"the failure must be the query_only/SQLITE_READONLY face, got: %v", err)
	_, err = projectrepo.New(clock, db.Runner()).Get(ctx, "01JD0PROJ0000000000000001")
	assert.ErrorIs(t, err, state.ErrNotFound, "the rejected write must not have landed")

	// Anchor 同属读柄投影：归属解析可用。
	team, err := deps.Core.Anchor.TeamOf(ctx, anchor.KindProject, "01JD0PROJ0000000000000000")
	require.NoError(t, err)
	assert.Equal(t, "default", team)

	// 写缝仍在写柄：Deps.Tx 的事务作用域 repo 可写。
	require.NoError(t, deps.Core.Tx.Do(ctx, func(st transact.Stores) error {
		return st.Projects.Create(ctx, &project.Project{
			ID: "01JD0PROJ0000000000000002", TeamID: "default", Name: "tx-write",
		})
	}))
	_, err = deps.Structure.Projects.Get(ctx, "01JD0PROJ0000000000000002")
	require.NoError(t, err, "the committed tx write is visible to the read path")
}
