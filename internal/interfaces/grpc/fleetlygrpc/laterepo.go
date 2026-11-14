package fleetlygrpc

// laterepo.go 是拆分 handler 包的测试 seam：apitest/CLI golden 在构造
// Services 之后覆写 repo 字段（故障注入），拆分包的 Deps 若直接捕获字段
// 值会绕过覆写。经一个逐方法委托的窄包装保持晚绑定读面。

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
)

// lateProjectRepo 把每次调用转发到 Services.Projects 的当前值。
type lateProjectRepo struct{ s *Services }

var _ project.Repo = lateProjectRepo{}

func (r lateProjectRepo) Create(ctx context.Context, p *project.Project) error {
	return r.s.Structure.Projects.Create(ctx, p)
}
func (r lateProjectRepo) Get(ctx context.Context, id string) (*project.Project, error) {
	return r.s.Structure.Projects.Get(ctx, id)
}
func (r lateProjectRepo) GetByNameInTeam(ctx context.Context, teamID, name string) (*project.Project, error) {
	return r.s.Structure.Projects.GetByNameInTeam(ctx, teamID, name)
}
func (r lateProjectRepo) List(ctx context.Context) ([]project.Project, error) {
	return r.s.Structure.Projects.List(ctx)
}
func (r lateProjectRepo) ListPage(ctx context.Context, afterSeq int64, limit int) ([]project.Project, error) {
	return r.s.Structure.Projects.ListPage(ctx, afterSeq, limit)
}
func (r lateProjectRepo) ListByTeam(ctx context.Context, teamID string) ([]project.Project, error) {
	return r.s.Structure.Projects.ListByTeam(ctx, teamID)
}
func (r lateProjectRepo) ListByTeamPage(ctx context.Context, teamID string, afterSeq int64, limit int) ([]project.Project, error) {
	return r.s.Structure.Projects.ListByTeamPage(ctx, teamID, afterSeq, limit)
}
func (r lateProjectRepo) SoftDelete(ctx context.Context, id string) error {
	return r.s.Structure.Projects.SoftDelete(ctx, id)
}
