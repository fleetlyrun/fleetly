package fleetlygrpc

// factOf 归属解析 fail-closed（R08-M8，S14-c7）：资源轴事实的 Team 解析
// 失败必须返回 error（整单拒绝），不得吞成 TeamID=""（空 Team 行对所有
// Team 可见 = 跨队事件泄漏）。平台级事实由 platformFact 显式构造。

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// failingProjects 只覆写 Get（注入归属解析故障；其余方法走嵌入接口）。
type failingProjects struct {
	project.Repo
	err error
}

func (f failingProjects) Get(context.Context, string) (*project.Project, error) { return nil, f.err }

// wantInternal 断言错误是 E_INTERNAL 信封（fail-closed 收口形态）。
func wantInternal(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	ae, ok := err.(*apperr.Error)
	require.True(t, ok, "want an apperr envelope, got %T", err)
	assert.Equal(t, "E_INTERNAL", ae.Code())
}

func TestFactOfFailsClosedOnUnresolvableOwnership(t *testing.T) {
	db, clock := statetest.New(t)
	ctx := context.Background()
	projects := projectrepo.New(clock, db.Runner())
	const pid = "01JD0FACT00000000000000000"
	require.NoError(t, projects.Create(ctx, &project.Project{ID: pid, TeamID: "team-a", Name: "alpha"}))
	s := &Services{Projects: projects}
	row := func(team string) writes.FactRow { return writes.FactRow{TeamID: team} }

	// 正例：解析成功 → 闭包铸入归属 Team。
	fact, err := s.factOf(ctx, pid, row)
	require.NoError(t, err)
	assert.Equal(t, "team-a", fact().TeamID)

	// 空 project_id：拒绝（不得落平台级可见行）。
	_, err = s.factOf(ctx, "", row)
	wantInternal(t, err)

	// 行缺失（Project 被删/不存在）：拒绝。
	_, err = s.factOf(ctx, "01JD0MISSING00000000000000", row)
	wantInternal(t, err)

	// 存储故障：拒绝（不是"放行为平台级"）。
	s.Projects = failingProjects{Repo: projects, err: errors.New("db busy")}
	_, err = s.factOf(ctx, pid, row)
	wantInternal(t, err)
}

// TestPlatformFactIsExplicitConstructor：平台级事实（TeamID=""）走显式
// helper——语义上是"无 Team 轴"的声明口，不是资源轴解析的失败兜底。
func TestPlatformFactIsExplicitConstructor(t *testing.T) {
	fact := platformFact(func() writes.FactRow { return writes.FactRow{Name: "template.refreshed"} })
	assert.Equal(t, "", fact().TeamID)
	assert.Equal(t, "template.refreshed", fact().Name)
}
