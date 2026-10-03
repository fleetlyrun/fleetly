package anchor_test

// anchor 解析图单测（架构评审第二轮候选 3）：链形状（直通/单跳/二跳/
// Any 档）、逐跳 NotFoundError 资源名、KindTeam 直通。行为级证明由
// freeze（governance）与行级授权（apitest 跨 Team 矩阵，ADR-0035 验收
// 锚）的既有测试承载——两消费面策略不变。

import (
	"context"
	"errors"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/anchor"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
	"github.com/fleetlyrun/fleetly/internal/state/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	tProject = "01JD0PROJ0000000000000000A"
	tTeam    = "team-a"
)

func TestAnchorTeamOfChains(t *testing.T) {
	db, clock := statertest.New(t)
	a := anchor.New(clock)
	ctx := context.Background()
	run := db.Runner()

	require.NoError(t, project.New(clock).Create(ctx, run, &project.Project{
		ID: tProject, Name: "shop", TeamID: tTeam,
	}))
	require.NoError(t, task.New(clock).Create(ctx, run, &task.Task{
		ID: "01JD0TASK0000000000000000A", ProjectID: tProject, Form: "one-shot",
		State: "active", Spec: []byte("{}"), DNSName: "t",
	}))
	require.NoError(t, app.New(clock).Create(ctx, run, &app.App{
		ID: "01JD0APP00000000000000000A", ProjectID: tProject, Name: "web",
	}))
	require.NoError(t, networkrepo.New(clock).Create(ctx, run, &networkrepo.Network{
		ID: "01JD0NET00000000000000000A", ProjectID: tProject, Name: "default",
	}))
	require.NoError(t, networkpeer.New(clock).Create(ctx, run, &networkpeer.Peer{
		ID: "01JD0PEER0000000000000000A", NetworkID: "01JD0NET00000000000000000A",
	}))

	// KindTeam 直通（零跳）。
	team, err := a.TeamOf(ctx, run, anchor.KindTeam, tTeam)
	require.NoError(t, err)
	assert.Equal(t, tTeam, team)

	// 单跳与二跳链。
	team, err = a.TeamOf(ctx, run, anchor.KindTask, "01JD0TASK0000000000000000A")
	require.NoError(t, err)
	assert.Equal(t, tTeam, team)

	team, err = a.TeamOf(ctx, run, anchor.KindPeer, "01JD0PEER0000000000000000A")
	require.NoError(t, err)
	assert.Equal(t, tTeam, team)

	team, err = a.TeamOfProjectID(ctx, run, tProject)
	require.NoError(t, err)
	assert.Equal(t, tTeam, team)
}

func TestAnchorNotFoundCarriesLegResource(t *testing.T) {
	db, clock := statertest.New(t)
	a := anchor.New(clock)
	ctx := context.Background()
	run := db.Runner()

	// 终点腿在场，首跳缺失 → 报首跳资源名。
	_, err := a.TeamOf(ctx, run, anchor.KindTask, "01JD0MISSING0000000000000T")
	var nf *anchor.NotFoundError
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, "task", nf.Resource)
	assert.True(t, errors.Is(err, state.ErrNotFound), "freeze 面据此放行")

	// 二跳链：peer 在场、network 缺失 → 报 network。
	require.NoError(t, networkpeer.New(clock).Create(ctx, run, &networkpeer.Peer{
		ID: "01JD0PEER0000000000000000B", NetworkID: "01JD0MISSING0000000000000N",
	}))
	_, err = a.TeamOf(ctx, run, anchor.KindPeer, "01JD0PEER0000000000000000B")
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, "network", nf.Resource)

	// 首跳在场、终点腿缺失 → 报 project。
	require.NoError(t, task.New(clock).Create(ctx, run, &task.Task{
		ID: "01JD0TASK0000000000000000B", ProjectID: "01JD0MISSING0000000000000P",
		Form: "one-shot", State: "active", Spec: []byte("{}"), DNSName: "t",
	}))
	_, err = a.TeamOf(ctx, run, anchor.KindTask, "01JD0TASK0000000000000000B")
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, "project", nf.Resource)
}

func TestAnchorAppTombstoneTiers(t *testing.T) {
	db, clock := statertest.New(t)
	a := anchor.New(clock)
	ctx := context.Background()
	run := db.Runner()

	require.NoError(t, project.New(clock).Create(ctx, run, &project.Project{
		ID: tProject, Name: "shop", TeamID: tTeam,
	}))
	gone := "01JD0APP00000000000000000B"
	require.NoError(t, app.New(clock).Create(ctx, run, &app.App{
		ID: gone, ProjectID: tProject, Name: "gone",
	}))
	require.NoError(t, app.New(clock).SoftDelete(ctx, run, gone))

	// 活跃档（生命周期动词口径）：tombstone 行 = 缺失。
	_, err := a.TeamOf(ctx, run, anchor.KindApp, gone)
	var nf *anchor.NotFoundError
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, "app", nf.Resource)

	// Any 档（审计型读面口径，ADR-0035）：tombstone 行归属仍是事实。
	team, err := a.TeamOf(ctx, run, anchor.KindAnyApp, gone)
	require.NoError(t, err)
	assert.Equal(t, tTeam, team)
}
