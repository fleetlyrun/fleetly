// Package authn 是身份执法的应用层（assembly 与 apitest 共用）：内置种子、
// Bootstrap Token 首启流、（随拦截器批）Bearer 解析与 scope 执法。域内核
// 在 internal/identity（不 import 任何 internal 包）；本包组合域内核与
// state 聚合 repo。
package authn

import (
	"context"
	"database/sql"
	"errors"

	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/role"
	"github.com/fleetlyrun/fleetly/internal/state/team"
)

// EnsureSeed 幂等落 default Team 与三条内置角色（scopes 由代码单一源刷新
// ——词表演进时启动即同步）。启动链与测试夹具共用。
func EnsureSeed(ctx context.Context, db *state.DB, resources []string) error {
	teams, roles := team.New(db.Clock()), role.New(db.Clock())
	return db.Tx(ctx, func(tx *sql.Tx) error {
		if err := teams.Create(ctx, tx, &team.Team{
			ID: identity.DefaultTeamID, Name: identity.DefaultTeamID,
		}); err != nil {
			// default Team 已存在（幂等重启）时 repo 返回 ErrConflict 链：
			// 吞掉并继续刷内置角色，其余错误上抛。
			if errors.Is(err, state.ErrConflict) {
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
