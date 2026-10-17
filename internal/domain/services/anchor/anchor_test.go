package anchor_test

// anchor 解析图单测（架构评审第二轮候选 3）：链形状（直通/单跳/二跳/
// Any 档）、逐跳 NotFoundError 资源名、KindTeam 直通。行为级证明由
// freeze（governance）与行级授权（apitest 跨 Team 矩阵，ADR-0035 验收
// 锚）的既有测试承载——两消费面策略不变。

import (
	"context"
	"errors"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
	"github.com/fleetlyrun/fleetly/internal/domain/services/anchor"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/networkpeer"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	apprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
	buildrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/build"
	dbrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/database"
	deploymentrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/deployment"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/githubapp"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/hook"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/network"
	networkpeerrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/networkpeer"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	routerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/route"
	runrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/run"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/schedule"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	taskrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/task"
	volumerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/volume"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	tProject = "01JD0PROJ0000000000000000A"
	tTeam    = "team-a"
)

// newAnchor 构造注入完整 repo 集的解析器（anchor 零持久化依赖，测试侧
// 充当组合根）。
func newAnchor(clock state.Clock, run state.Runner) *anchor.Anchor {
	return anchor.New(anchor.Repos{
		Projects:    projectrepo.New(clock, run),
		Apps:        apprepo.New(clock, run),
		Tasks:       taskrepo.New(clock, run),
		Schedules:   schedule.New(clock, run),
		Networks:    networkrepo.New(clock, run),
		Peers:       networkpeerrepo.New(clock, run),
		Hooks:       hook.New(clock, run),
		Routes:      routerepo.New(clock, run),
		Databases:   dbrepo.New(clock, run),
		Volumes:     volumerepo.New(clock, run),
		Runs:        runrepo.New(clock, run),
		Deployments: deploymentrepo.New(clock, run),
		Builds:      buildrepo.New(clock, run),
		GitHubApps:  githubapp.New(clock, run),
	})
}

func TestAnchorTeamOfChains(t *testing.T) {
	db, clock := statetest.New(t)
	a := newAnchor(clock, db.Runner())
	ctx := context.Background()
	run := db.Runner()

	require.NoError(t, projectrepo.New(clock, run).Create(ctx, &project.Project{
		ID: tProject, Name: "shop", TeamID: tTeam,
	}))
	require.NoError(t, taskrepo.New(clock, run).Create(ctx, &task.Task{
		ID: "01JD0TASK0000000000000000A", ProjectID: tProject, Form: "one-shot",
		State: "active", Spec: []byte("{}"), DNSName: "t",
	}))
	require.NoError(t, apprepo.New(clock, run).Create(ctx, &app.App{
		ID: "01JD0APP00000000000000000A", ProjectID: tProject, Name: "web",
	}))
	require.NoError(t, networkrepo.New(clock, run).Create(ctx, &network.Network{
		ID: "01JD0NET00000000000000000A", ProjectID: tProject, Name: "default",
	}))
	require.NoError(t, networkpeerrepo.New(clock, run).Create(ctx, &networkpeer.Peer{
		ID: "01JD0PEER0000000000000000A", NetworkID: "01JD0NET00000000000000000A",
	}))

	// KindTeam 直通（零跳）。
	team, err := a.TeamOf(ctx, anchor.KindTeam, tTeam)
	require.NoError(t, err)
	assert.Equal(t, tTeam, team)

	// 单跳与二跳链。
	team, err = a.TeamOf(ctx, anchor.KindTask, "01JD0TASK0000000000000000A")
	require.NoError(t, err)
	assert.Equal(t, tTeam, team)

	team, err = a.TeamOf(ctx, anchor.KindPeer, "01JD0PEER0000000000000000A")
	require.NoError(t, err)
	assert.Equal(t, tTeam, team)

	team, err = a.TeamOfProjectID(ctx, tProject)
	require.NoError(t, err)
	assert.Equal(t, tTeam, team)
}

func TestAnchorNotFoundCarriesLegResource(t *testing.T) {
	db, clock := statetest.New(t)
	a := newAnchor(clock, db.Runner())
	ctx := context.Background()
	run := db.Runner()

	// 终点腿在场，首跳缺失 → 报首跳资源名。
	_, err := a.TeamOf(ctx, anchor.KindTask, "01JD0MISSING0000000000000T")
	var nf *anchor.NotFoundError
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, "task", nf.Resource)
	assert.True(t, errors.Is(err, state.ErrNotFound), "freeze 面据此放行")

	// 二跳链：peer 在场、network 缺失 → 报 network。
	require.NoError(t, networkpeerrepo.New(clock, run).Create(ctx, &networkpeer.Peer{
		ID: "01JD0PEER0000000000000000B", NetworkID: "01JD0MISSING0000000000000N",
	}))
	_, err = a.TeamOf(ctx, anchor.KindPeer, "01JD0PEER0000000000000000B")
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, "network", nf.Resource)

	// 首跳在场、终点腿缺失 → 报 project。
	require.NoError(t, taskrepo.New(clock, run).Create(ctx, &task.Task{
		ID: "01JD0TASK0000000000000000B", ProjectID: "01JD0MISSING0000000000000P",
		Form: "one-shot", State: "active", Spec: []byte("{}"), DNSName: "t",
	}))
	_, err = a.TeamOf(ctx, anchor.KindTask, "01JD0TASK0000000000000000B")
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, "project", nf.Resource)
}

func TestAnchorAppTombstoneTiers(t *testing.T) {
	db, clock := statetest.New(t)
	a := newAnchor(clock, db.Runner())
	ctx := context.Background()
	run := db.Runner()

	require.NoError(t, projectrepo.New(clock, run).Create(ctx, &project.Project{
		ID: tProject, Name: "shop", TeamID: tTeam,
	}))
	gone := "01JD0APP00000000000000000B"
	require.NoError(t, apprepo.New(clock, run).Create(ctx, &app.App{
		ID: gone, ProjectID: tProject, Name: "gone",
	}))
	require.NoError(t, apprepo.New(clock, run).SoftDelete(ctx, gone))

	// 活跃档（生命周期动词口径）：tombstone 行 = 缺失。
	_, err := a.TeamOf(ctx, anchor.KindApp, gone)
	var nf *anchor.NotFoundError
	require.ErrorAs(t, err, &nf)
	assert.Equal(t, "app", nf.Resource)

	// Any 档（审计型读面口径，ADR-0035）：tombstone 行归属仍是事实。
	team, err := a.TeamOf(ctx, anchor.KindAnyApp, gone)
	require.NoError(t, err)
	assert.Equal(t, tTeam, team)
}
