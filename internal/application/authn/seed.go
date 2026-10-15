package authn

// Package authn 是身份执法的应用层（assembly 与 apitest 共用）：内置种子、
// Bootstrap Token 首启流、（随拦截器批）Bearer 解析与 scope 执法。域内核
// 在 internal/domain/identity（不 import 任何 internal 包）；本包组合域
// 内核与 state 契约面（DB 接口 + 聚合 repo 注入，P1b C 批构造上提——零
// 持久化构造）。

import (
	"context"
	"database/sql"
	"errors"

	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/role"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/team"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/token"
	"github.com/fleetlyrun/fleetly/internal/domain/identity/user"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// SeedRepos 是种子与 Bootstrap Token 首启面的 repo 集（组合根构造注入）。
type SeedRepos struct {
	Teams  team.Repo
	Roles  role.Repo
	Tokens token.Repo
	Users  user.Repo
}

// EnsureSeed 幂等落 default Team 与三条内置角色（scopes 由代码单一源刷新
// ——词表演进时启动即同步）。启动链与测试夹具共用。
func EnsureSeed(ctx context.Context, db state.DB, repos SeedRepos, resources []string) error {
	teams, roles := repos.Teams, repos.Roles
	return db.Tx(ctx, func(tx *sql.Tx) error {
		if err := teams.Create(ctx, tx, &team.Team{
			ID: identity.DefaultTeamID, Name: identity.DefaultTeamID,
		}); err != nil {
			// default Team 已存在（幂等重启）时 repo 返回 ErrAlreadyExists
			// 链：吞掉并继续刷内置角色，其余错误上抛。
			if errors.Is(err, state.ErrAlreadyExists) {
				// 已存在的场景仍要刷新内置角色，落到下方循环。
			} else {
				return err
			}
		}
		for _, b := range identity.BuiltinRoles(resources) {
			if err := roles.UpsertBuiltin(ctx, tx, &role.Role{
				ID: b.ID, Name: b.Name, Builtin: true,
				Scopes: identity.ScopeStrings(b.Scopes),
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
