package configrepo

import (
	"context"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// Repo 是 Config 聚合存取。方法集机械平移自既有 repo 导出面；Runner 一律
// 由调用方注入（*sql.DB 或 *sql.Tx 均满足，可独立执行也可组合进四件一拍
// 事务，ADR-0005）。
type Repo interface {
	// Create 落新版本（同 project+name 下一版本号单调递增；配额执法在 API
	// 层——repo 只承载版本化事实）。
	Create(ctx context.Context, run state.Runner, c *Config) error
	// NextVersion 返回下一版本号（首版 1）。
	NextVersion(ctx context.Context, run state.Runner, projectID, name string) (int64, error)
	// Latest 返回最新版本（无 → state.ErrNotFound）。
	Latest(ctx context.Context, run state.Runner, projectID, name string) (*Config, error)
	// GetVersion 返回指定版本。
	GetVersion(ctx context.Context, run state.Runner, projectID, name string, version int64) (*Config, error)
	// LatestByProject 返回 Project 内每个 name 的最新版本（全量列表面——
	// 配额执法与引擎装配面消费；API 分页读面走 LatestByProjectPage）。
	LatestByProject(ctx context.Context, run state.Runner, projectID string) ([]Config, error)
	// LatestByProjectPage 是 List 的分页读面（ADR-0026 after_* + limit；游标
	// 轴 = 既有排序轴 name 字典序升序）。每行仍是该 name 的最新版本（分页只
	// 动行集不改每行形态）。limit<=0 或 >200 回落/钳制缺省 50。
	LatestByProjectPage(ctx context.Context, run state.Runner, projectID string, afterName string, limit int) ([]Config, error)
	// ListVersions 返回全部版本（旧→新）。
	ListVersions(ctx context.Context, run state.Runner, projectID, name string) ([]Config, error)
}
