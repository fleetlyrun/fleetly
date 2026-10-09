package cmd

// Identity 上下文动词（F0.5/F0.6/F0.7 CLI 面）：tokens/users/roles/teams
// 组与 audit 顶层动词。明文凭证只在 create/invite 输出出现一次。

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/lynx-go/commands"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
)

// ---- tokens ----

func newTokensCreateVerb() commands.Command {
	const name = "create"
	var team, role, user string
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Mint a token (secret shown once; scopes come from the team role)",
		usage:    "tokens create NAME --role ROLE_ID [--team TEAM_ID] [--user USER_ID]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&team, "team", "", "team id (default \"default\")")
			fs.StringVar(&role, "role", "", "role id granting the scope set (required)")
			fs.StringVar(&user, "user", "", "optional owner user id")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if role == "" {
				return usageErr(name, "--role is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Tokens.CreateToken(ctx, &identityv1.CreateTokenRequest{
				Name: args[0], TeamId: team, RoleId: role, UserId: user,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintf(env.Stdout, "created token %s (id %s)\nsecret (shown once): %s\n",
					resp.GetToken().GetName(), resp.GetToken().GetId(), resp.GetSecret())
			})
		},
	}
}

func newTokensListVerb() commands.Command {
	const name = "list"
	return &flaggedVerb{
		name:     name,
		synopsis: "List tokens (prefixes only; secrets never return)",
		usage:    "tokens list",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Tokens.ListTokens(ctx, &identityv1.ListTokensRequest{})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tNAME\tPREFIX\tROLE\tREVOKED\tLAST_USED\tCREATED")
				for _, t := range resp.GetTokens() {
					revoked := "no"
					if t.GetRevoked() {
						revoked = "yes"
					}
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						t.GetId(), t.GetName(), t.GetPrefix(), t.GetRoleId(), revoked, t.GetLastUsedAt(), t.GetCreatedAt())
				}
			})
		},
	}
}

func newTokensRevokeVerb() commands.Command {
	const name = "revoke"
	return &flaggedVerb{
		name:     name,
		synopsis: "Revoke a token (its next call is rejected)",
		usage:    "tokens revoke ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Tokens.RevokeToken(ctx, &identityv1.RevokeTokenRequest{Id: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetToken(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "revoked token %s (id %s)\n", resp.GetToken().GetName(), resp.GetToken().GetId())
			})
		},
	}
}

// ---- users ----

func newUsersCreateVerb() commands.Command {
	const name = "create"
	var team, role, password string
	var passwordStdin bool
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Create a user granted a role in a team",
		usage:    "users create NAME --role ROLE_ID [--team TEAM_ID] [--password PASSWORD]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&team, "team", "", "team id (default \"default\")")
			fs.StringVar(&role, "role", "", "role id (required)")
			fs.StringVar(&password, "password", "", "initial password (empty = no password login; read stdin when omitted and --password-stdin is set)")
			fs.BoolVar(&passwordStdin, "password-stdin", false, "read the initial password from stdin")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if role == "" {
				return usageErr(name, "--role is required")
			}
			pw := ""
			if passwordStdin || password != "" {
				v, err := valueOrStdin(password, env)
				if err != nil {
					return err
				}
				pw = strings.TrimSpace(v)
				if pw == "" {
					return usageErr(name, "--password is required (or pipe it via stdin)")
				}
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Users.CreateUser(ctx, &identityv1.CreateUserRequest{
				Name: args[0], TeamId: team, RoleId: role, Password: pw,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetUser(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "created user %s (id %s)\n", resp.GetUser().GetName(), resp.GetUser().GetId())
			})
		},
	}
}

// users set-password：设置/重置密码（admin 面；C6 密码会话第一期的
// 密码物主面——自助改密随 SSO 批裁决）。密码 stdin 兜底（valueOrStdin）。
func newUsersSetPasswordVerb() commands.Command {
	const name = "set-password"
	var password string
	return &flaggedVerb{
		name:     name,
		synopsis: "Set or reset a user's password (admin; the credential for password login)",
		usage:    "users set-password [--password PASSWORD] USER_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&password, "password", "", "new password (empty = read stdin)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one USER_ID argument")
			}
			pw, err := valueOrStdin(password, env)
			if err != nil {
				return err
			}
			pw = strings.TrimSpace(pw)
			if pw == "" {
				return usageErr(name, "--password is required (or pipe it via stdin)")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			_, err = c.Users.SetUserPassword(ctx, &identityv1.SetUserPasswordRequest{UserId: args[0], Password: pw})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, &identityv1.SetUserPasswordResponse{}, func() {
				_, _ = fmt.Fprintf(env.Stdout, "password set for user %s\n", args[0])
			})
		},
	}
}

func newUsersListVerb() commands.Command {
	const name = "list"
	return &flaggedVerb{
		name:     name,
		synopsis: "List users",
		usage:    "users list",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Users.ListUsers(ctx, &identityv1.ListUsersRequest{})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tNAME\tCREATED")
				for _, u := range resp.GetUsers() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", u.GetId(), u.GetName(), u.GetCreatedAt())
				}
			})
		},
	}
}

func newUsersInviteVerb() commands.Command {
	const name = "invite"
	var team, role, ttl string
	return &flaggedVerb{
		name:     name,
		synopsis: "Issue a one-time invitation (24h default, role-bound; secret shown once)",
		usage:    "users invite --role ROLE_ID [--team TEAM_ID] [--ttl DURATION]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&team, "team", "", "team id (default \"default\")")
			fs.StringVar(&role, "role", "", "role the invitee receives (required)")
			fs.StringVar(&ttl, "ttl", "", "validity window like \"24h\" (max 168h)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) > 0 {
				return usageErr(name, "unexpected argument(s)")
			}
			if role == "" {
				return usageErr(name, "--role is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Invitations.CreateInvitation(ctx, &identityv1.CreateInvitationRequest{
				TeamId: team, RoleId: role, Ttl: ttl,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintf(env.Stdout, "invitation %s expires %s\naccept with: fleetly users accept --token %s --name NAME\n",
					resp.GetInvitation().GetId(), resp.GetInvitation().GetExpiresAt(), resp.GetSecret())
			})
		},
	}
}

func newUsersAcceptVerb() commands.Command {
	const name = "accept"
	var token, userName string
	return &flaggedVerb{
		name:     name,
		synopsis: "Redeem an invitation token into a user (no platform token needed)",
		usage:    "users accept --token INVITATION --name NAME",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&token, "token", "", "invitation secret (required)")
			fs.StringVar(&userName, "name", "", "username to claim (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) > 0 {
				return usageErr(name, "unexpected argument(s)")
			}
			if token == "" || userName == "" {
				return usageErr(name, "--token and --name are required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Invitations.AcceptInvitation(ctx, &identityv1.AcceptInvitationRequest{
				Secret: token, UserName: userName,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetUser(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "welcome %s (id %s) — ask an admin to mint your first token\n",
					resp.GetUser().GetName(), resp.GetUser().GetId())
			})
		},
	}
}

// ---- roles ----

func newRolesCreateVerb() commands.Command {
	const name = "create"
	var team string
	var scopes multiFlag
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Create a custom role from resource:action scopes (write implies read)",
		usage:    "roles create NAME --scope RESOURCE:ACTION [--scope ...] [--team TEAM_ID]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&team, "team", "", "team id (default \"default\")")
			fs.Var(&scopes, "scope", "resource:action scope (repeatable)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if len(scopes) == 0 {
				return usageErr(name, "at least one --scope is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Roles.CreateRole(ctx, &identityv1.CreateRoleRequest{
				Name: args[0], TeamId: team, Scopes: scopes,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetRole(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "created role %s (id %s)\n", resp.GetRole().GetName(), resp.GetRole().GetId())
			})
		},
	}
}

func newRolesListVerb() commands.Command {
	const name = "list"
	return &flaggedVerb{
		name:     name,
		synopsis: "List roles (builtin owner/admin/member plus custom)",
		usage:    "roles list",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Roles.ListRoles(ctx, &identityv1.ListRolesRequest{})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tNAME\tTEAM\tBUILTIN\tSCOPES")
				for _, r := range resp.GetRoles() {
					builtin := "no"
					if r.GetBuiltin() {
						builtin = "yes"
					}
					team := r.GetTeamId()
					if team == "" {
						team = "-"
					}
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%v\n", r.GetId(), r.GetName(), team, builtin, r.GetScopes())
				}
			})
		},
	}
}

// ---- teams ----

func newTeamsCreateVerb() commands.Command {
	const name = "create"
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Create a team",
		usage:    "teams create NAME",
		setFlags: func(fs *flag.FlagSet) { idem.declare(fs) },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Teams.CreateTeam(ctx, &identityv1.CreateTeamRequest{Name: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetTeam(), func() {
				_, _ = fmt.Fprintf(env.Stdout, "created team %s (id %s)\n", resp.GetTeam().GetName(), resp.GetTeam().GetId())
			})
		},
	}
}

func newTeamsListVerb() commands.Command {
	const name = "list"
	return &flaggedVerb{
		name:     name,
		synopsis: "List teams",
		usage:    "teams list",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Teams.ListTeams(ctx, &identityv1.ListTeamsRequest{})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tNAME\tCREATED")
				for _, tm := range resp.GetTeams() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", tm.GetId(), tm.GetName(), tm.GetCreatedAt())
				}
			})
		},
	}
}

// ---- audit ----

func newAuditVerb() commands.Command {
	const name = "audit"
	var source, action, actor, resource string
	var limit int64
	return &flaggedVerb{
		name:     name,
		synopsis: "Read the audit trail (who changed what, from where)",
		usage:    "audit [--source api|cli|manual|webhook|schedule|system] [--action PREFIX] [--actor ACTOR] [--resource RES] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&source, "source", "", "filter by source enum")
			fs.StringVar(&action, "action", "", "filter by action prefix (e.g. \"token.\")")
			fs.StringVar(&actor, "actor", "", "filter by actor (user:NAME / token:NAME)")
			fs.StringVar(&resource, "resource", "", "filter by resource (e.g. project/ID)")
			fs.Int64Var(&limit, "limit", 50, "max entries (capped at 1000)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) > 0 {
				return usageErr(name, "unexpected argument(s)")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Audit.ListAudit(ctx, &identityv1.ListAuditRequest{
				Source: source, Action: action, Actor: actor, Resource: resource, Limit: int32(limit), //nolint:gosec // 限额在服务端钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tACTOR\tSOURCE\tACTION\tRESOURCE\tBEFORE\tAFTER\tCREATED")
				for _, e := range resp.GetEntries() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						e.GetId(), e.GetActor(), e.GetSource(), e.GetAction(), e.GetResource(),
						e.GetBeforeFp(), e.GetAfterFp(), e.GetCreatedAt())
				}
			})
		},
	}
}

// multiFlag 是可重复字符串旗标（--scope A --scope B）。
type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprint([]string(*m)) }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}
