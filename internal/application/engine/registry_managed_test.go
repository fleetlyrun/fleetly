package engine

// 受管 zot 面测试（F1.11，ADR-0019 附录 B）：无 Registry 的诚实拒绝。
// managed host 平台凭证注入（materialsFor 面）已随 P5 wave-5 拆包迁入
// intake 卫星包白盒（intake/materials_test.go，通则⑥——断言面零漂移）；
// reconcileManaged 双 Provider 面已随 P5 wave-4 迁 manageds 包白盒。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	apprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// 附录 B.5①：无 Registry Provider 时 build 源部署在 prepare 精确失败
// （不做本机导入退化形态）。
func TestBuildSourceWithoutRegistryFailsAtPrepare(t *testing.T) {
	fb := &fakeBuilder{digest: "sha256:built"}
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: rt,
		Builders: map[string]capability.Builder{specir.BuilderDockerfile: fb},
		Logger:   discardLogger()}, Options{Data: DataOptions{DataRoot: t.TempDir()}})
	ctx := context.Background()
	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}))
	require.NoError(t, apprepo.New(clock, db.Runner()).Create(ctx, &app.App{ID: tAppID, ProjectID: tProjectID, Name: "web"}))

	revID, _, _ := freezeGitBuildSpec(t, e, "01JD0REV0000000000000000C1")
	d, _, err := e.Submit(ctx, SubmitRequest{AppID: tAppID, RevisionID: revID})
	require.NoError(t, err)

	e.step(ctx) // preparing → 前置门精确失败（不进 building）
	d = getDeployment(t, e, d.ID)
	assert.Equal(t, deployment.StateFailed, d.State)
	assert.Contains(t, d.Error, "no registry provider wired")
	assert.Empty(t, fb.snapshot(), "the builder must never be engaged without a push target")
}
