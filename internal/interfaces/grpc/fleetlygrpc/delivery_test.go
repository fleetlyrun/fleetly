package fleetlygrpc

// deployVarLookup 纯单测（P0-7.1）：装载失败必须上抛——两次 ListActive 任一
// 失败都不得静默吞成空环境（DB 瞬断会把内部故障伪装成"compose 变量不可
// 消解"的 E_INVALID_ARGUMENT）；正向断言分层合成（App 层胜出 Project 层）。
// 服务面级的受理链回归在 internal/apitest。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/appvariable"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/sharedvariable"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statertest"
)

func testVarServices(t *testing.T) *Services {
	t.Helper()
	db, _ := statertest.New(t)
	return &Services{
		DB:              db,
		SharedVariables: sharedvariable.New(db.Clock()),
		AppVariables:    appvariable.New(db.Clock()),
	}
}

func TestDeployVarLookupLayering(t *testing.T) {
	s := testVarServices(t)
	ctx := context.Background()
	require.NoError(t, s.SharedVariables.Upsert(ctx, s.DB.Runner(), &sharedvariable.SharedVariable{ProjectID: "p-1", Name: "db_url", Value: "proj"}))
	require.NoError(t, s.SharedVariables.Upsert(ctx, s.DB.Runner(), &sharedvariable.SharedVariable{ProjectID: "p-1", Name: "only_proj", Value: "v"}))
	require.NoError(t, s.AppVariables.Upsert(ctx, s.DB.Runner(), &appvariable.AppVariable{AppID: "a-1", Name: "db_url", Value: "app"}))

	lookup, err := deployVarLookup(ctx, s, &app.App{ID: "a-1", ProjectID: "p-1"})
	require.NoError(t, err)
	if v, ok := lookup("db_url"); !ok || v != "app" {
		t.Fatalf("app layer must win: got %q %v", v, ok)
	}
	if v, ok := lookup("only_proj"); !ok || v != "v" {
		t.Fatalf("project layer must surface when app layer is absent: got %q %v", v, ok)
	}
	if _, ok := lookup("missing"); ok {
		t.Fatal("absent name must report not-ok (fail-closed 消解律的查表面)")
	}
}

// TestDeployVarLookupPropagatesLoadErrors：DROP TABLE 制造装载失败——
// 两层装载任一失败都必须原样上抛（修复前 err == nil 分支静默吞错）。
func TestDeployVarLookupPropagatesLoadErrors(t *testing.T) {
	ctx := context.Background()
	appRow := &app.App{ID: "a-1", ProjectID: "p-1"}

	// 第一层（SharedVariables）失败。
	s := testVarServices(t)
	if _, err := s.DB.Runner().ExecContext(ctx, "DROP TABLE shared_variables"); err != nil {
		t.Fatalf("drop shared_variables: %v", err)
	}
	_, err := deployVarLookup(ctx, s, appRow)
	require.Error(t, err, "shared variable load failure must propagate, not be swallowed")

	// 第二层（AppVariables）失败。
	s2 := testVarServices(t)
	if _, err := s2.DB.Runner().ExecContext(ctx, "DROP TABLE app_variables"); err != nil {
		t.Fatalf("drop app_variables: %v", err)
	}
	_, err = deployVarLookup(ctx, s2, appRow)
	require.Error(t, err, "app variable load failure must propagate, not be swallowed")

	// 上抛的错经 mapStateError 落 E_INTERNAL（不落受理类码）。
	mapped := mapStateError(err, "variables")
	env, ok := mapped.(interface{ Code() string })
	require.True(t, ok, "mapped error must be an apperr envelope")
	assert.Equal(t, "E_INTERNAL", env.Code())
}
