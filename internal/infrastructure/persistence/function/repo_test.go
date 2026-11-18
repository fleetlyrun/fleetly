package function_test

// Function 聚合带测试（ADR-0102 任务②）：聚合根行写读 + 冻结体子表
//（含 ADR-0101 保留窗 Prune 的 Function 载体）。

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/function"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	functionrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/function"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

func newFunctionRepos(t *testing.T) (function.Repo, function.RevisionRepo, state.DB) {
	t.Helper()
	db, clock := statetest.New(t)
	return functionrepo.New(clock, db.Runner()), functionrepo.NewRevisions(clock, db.Runner()), db
}

func TestFunctionCreateAndRead(t *testing.T) {
	repos, _, db := newFunctionRepos(t)
	ctx := context.Background()
	statetest.SeedProject(t, db, "01JD0PROJ00000000000000000", "default")

	f := &function.Function{ProjectID: "01JD0PROJ00000000000000000", Name: "resize-img"}
	require.NoError(t, repos.Create(ctx, f))
	assert.NotEmpty(t, f.ID, "id is minted when empty (ADR-0092 ULID)")
	assert.NotZero(t, f.Seq)

	got, err := repos.Get(ctx, f.ID)
	require.NoError(t, err)
	assert.Equal(t, "resize-img", got.Name)
	assert.Equal(t, "private", got.Exposure, "exposure defaults are the caller's normalization; row round-trips what was written")
	assert.Equal(t, int64(1), got.Replicas)

	byName, err := repos.GetByName(ctx, f.ProjectID, "resize-img")
	require.NoError(t, err)
	assert.Equal(t, f.ID, byName.ID)

	// 同项目活跃同名 → ErrAlreadyExists（部分唯一索引）。
	err = repos.Create(ctx, &function.Function{ProjectID: f.ProjectID, Name: "resize-img"})
	require.ErrorIs(t, err, state.ErrAlreadyExists)

	// tombstone 后同名重建立即可用（活跃行唯一性）。
	require.NoError(t, repos.SoftDelete(ctx, f.ID))
	_, err = repos.Get(ctx, f.ID)
	require.ErrorIs(t, err, state.ErrNotFound, "tombstone rows are invisible on the active face")
	f2 := &function.Function{ProjectID: f.ProjectID, Name: "resize-img"}
	require.NoError(t, repos.Create(ctx, f2), "name is reusable after tombstone")
	assert.NotEqual(t, f.ID, f2.ID)
}

func TestFunctionRowVerbs(t *testing.T) {
	repos, _, db := newFunctionRepos(t)
	ctx := context.Background()
	const projectID = "01JD0PROJ00000000000000000"
	statetest.SeedProject(t, db, projectID, "default")
	f := &function.Function{ProjectID: projectID, Name: "webhook-relay"}
	require.NoError(t, repos.Create(ctx, f))

	require.NoError(t, repos.SetCapacity(ctx, f.ID, 3))
	require.NoError(t, repos.SetExposure(ctx, f.ID, "public", "relay.example.test"))
	require.NoError(t, repos.SwapCurrent(ctx, f.ID, f.ID+"-r1", "sha256:abc"))
	require.NoError(t, repos.AdvanceStatus(ctx, f.ID, "running"))

	// 指纹 CAS：未变返现行零推进、变更 +1 同拍落指纹。
	g1, err := repos.EnsureGeneration(ctx, f.ID, "fp-a")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), g1, "generation starts at 1 on first fingerprint")
	g2, err := repos.EnsureGeneration(ctx, f.ID, "fp-a")
	require.NoError(t, err)
	assert.Equal(t, uint64(1), g2, "unchanged fingerprint keeps the current generation")
	g3, err := repos.EnsureGeneration(ctx, f.ID, "fp-b")
	require.NoError(t, err)
	assert.Equal(t, uint64(2), g3, "changed fingerprint advances the generation")
	gotFP, err := repos.Get(ctx, f.ID)
	require.NoError(t, err)
	assert.Equal(t, "fp-b", gotFP.SpecFingerprint)

	got, err := repos.Get(ctx, f.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(3), got.Replicas)
	assert.Equal(t, "public", got.Exposure)
	assert.Equal(t, "relay.example.test", got.PublicHost)
	assert.Equal(t, f.ID+"-r1", got.CurrentRevisionID)
	assert.Equal(t, "running", got.Status)

	// 转 private 清 host 位（设计 v3 §6：host 仅 public 消费）。
	require.NoError(t, repos.SetExposure(ctx, f.ID, "private", ""))
	got, err = repos.Get(ctx, f.ID)
	require.NoError(t, err)
	assert.Empty(t, got.PublicHost)

	// 行写打在 tombstone 行 = NotFound（活跃档口径）。
	require.NoError(t, repos.SoftDelete(ctx, f.ID))
	require.ErrorIs(t, repos.SetCapacity(ctx, f.ID, 2), state.ErrNotFound)
}

func TestFunctionListByProject(t *testing.T) {
	repos, _, db := newFunctionRepos(t)
	ctx := context.Background()
	const projectID = "01JD0PROJ00000000000000000"
	statetest.SeedProject(t, db, projectID, "default")
	for i := 1; i <= 3; i++ {
		f := &function.Function{ProjectID: projectID, Name: fmt.Sprintf("fn-%d", i), Exposure: "private"}
		if i == 2 {
			f.Exposure = "public"
		}
		require.NoError(t, repos.Create(ctx, f))
	}

	all, err := repos.ListByProject(ctx, projectID, 0, 50, "", "")
	require.NoError(t, err)
	require.Len(t, all, 3)

	pub, err := repos.ListByProject(ctx, projectID, 0, 50, "public", "")
	require.NoError(t, err)
	require.Len(t, pub, 1)
	assert.Equal(t, "fn-2", pub[0].Name)

	page, err := repos.ListByProject(ctx, projectID, all[0].Seq, 2, "", "")
	require.NoError(t, err)
	assert.Len(t, page, 2, "after cursor skips the first row")
}

func TestFunctionRevisionLifecycle(t *testing.T) {
	_, revs, db := newFunctionRepos(t)
	ctx := context.Background()
	const (
		projectID = "01JD0PROJ00000000000000000"
		fnID      = "01JD0FN000000000000000000"
	)
	statetest.SeedFunction(t, db, fnID, projectID, "resize-img")

	seed := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			seq, err := revs.NextSeq(ctx, fnID)
			require.NoError(t, err)
			require.NoError(t, revs.Create(ctx, &function.Revision{
				FunctionID: fnID, Seq: seq, Spec: []byte(fmt.Sprintf(`{"v":%d}`, seq)),
			}))
		}
	}

	// 派生 id 形态 <fn>-r<n> + 内容寻址复用。
	seq, err := revs.NextSeq(ctx, fnID)
	require.NoError(t, err)
	rev := &function.Revision{FunctionID: fnID, Seq: seq, Spec: []byte(`{"a":1}`)}
	require.NoError(t, revs.Create(ctx, rev))
	assert.Equal(t, fmt.Sprintf("%s-r%d", fnID, seq), rev.ID)
	assert.NotEmpty(t, rev.Digest)
	dup, err := revs.FindByDigest(ctx, fnID, rev.Digest)
	require.NoError(t, err)
	assert.Equal(t, rev.ID, dup.ID)

	// 保留窗（ADR-0101 同律）：再冻 105（总 106）→ 恰清最旧 6（含首条
	// 占位 rev）；protect 窗外存活；幂等零删。
	seed(105)
	n, err := revs.Prune(ctx, fnID, nil, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 6, n)
	rows, err := revs.ListByFunction(ctx, fnID, 0, 200)
	require.NoError(t, err)
	require.Len(t, rows, 100)
	assert.Equal(t, int64(7), rows[0].Seq, "oldest non-active rows are pruned first")

	oldest := rows[0].ID
	seed(6)
	n, err = revs.Prune(ctx, fnID, []string{oldest}, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 5, n)
	rows, err = revs.ListByFunction(ctx, fnID, 0, 200)
	require.NoError(t, err)
	require.Len(t, rows, 101, "window plus the protected row beyond it")
	assert.Equal(t, int64(7), rows[0].Seq, "protected row survives beyond the window")

	n, err = revs.Prune(ctx, fnID, []string{oldest}, 100)
	require.NoError(t, err)
	assert.EqualValues(t, 0, n)

	// 聚合终局全清（ADR-0101 决策 4）。
	require.NoError(t, revs.DeleteAllByFunction(ctx, fnID))
	rows, err = revs.ListByFunction(ctx, fnID, 0, 200)
	require.NoError(t, err)
	assert.Empty(t, rows)
	_, err = revs.GetBySeq(ctx, fnID, 1)
	require.ErrorIs(t, err, state.ErrNotFound, "terminal cleanup leaves no resolvable revision")
}
