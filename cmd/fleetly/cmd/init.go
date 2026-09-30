package cmd

// fleetly init（F0.2 后半）：Bootstrap Token 消费流——校验（WhoAmI）→ 建
// admin 用户 → 铸 CLI token → 写凭据文件 → 默认吊销 bootstrap
//（--keep-bootstrap 保留）。无浏览器环境可完成全部初始化。

import (
	"context"
	"flag"
	"fmt"

	"github.com/lynx-go/commands"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// initReport 是 --json 形态（secret 仅此一次出现）。
type initReport struct {
	User             string `json:"user"`
	UserID           string `json:"user_id"`
	TokenName        string `json:"token_name"`
	TokenID          string `json:"token_id"`
	Secret           string `json:"secret"`
	Addr             string `json:"addr"`
	CredentialsPath  string `json:"credentials_path"`
	BootstrapRevoked bool   `json:"bootstrap_revoked"`
}

func newInitVerb() commands.Command {
	const name = "init"
	var token, addr string
	var keepBootstrap bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Consume the bootstrap token: create the admin user, mint a CLI token, revoke bootstrap",
		usage:    "init --token BOOTSTRAP_TOKEN [--addr ADDR] [--keep-bootstrap] NAME",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&token, "token", "", "bootstrap token from the server data root (required)")
			fs.StringVar(&addr, "addr", "", fmt.Sprintf("fleetlyd gRPC address (default %q; env %s)", defaultAddr, envAddr))
			fs.BoolVar(&keepBootstrap, "keep-bootstrap", false, "keep the bootstrap token alive (default: revoke it)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument (the admin user name)")
			}
			userName := args[0]
			if token == "" {
				return usageErr(name, "--token is required (the bootstrap token printed by the installer / the server data root)")
			}
			if identity.TokenKind(token) != "platform" {
				return fmt.Errorf("init: token must be a platform token (%s prefix)", identity.TokenPrefix)
			}
			if addr == "" {
				addr = envOr(envAddr, defaultAddr)
			}
			c, err := dialClient(addr)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			auth := fleetly.WithToken(ctx, token)

			// 1. 校验：token 必须是活的 bootstrap（已初始化的服务器这里即
			// 401——提示走 login 路径）。
			who, err := c.Users.WhoAmI(auth, &identityv1.WhoAmIRequest{})
			if err != nil {
				return fmt.Errorf("init: bootstrap token rejected: %w\n"+
					"if this server is already initialized, use 'fleetly login --token ...' with an existing token", err)
			}
			if who.GetTokenName() != identity.BootstrapTokenName {
				return fmt.Errorf("init: this flow consumes the bootstrap token (got %q); mint more tokens with 'fleetly tokens create' instead", who.GetTokenName())
			}

			// 2. 建 admin 用户；3. 铸名下 CLI token。
			user, err := c.Users.CreateUser(auth, &identityv1.CreateUserRequest{
				Name: userName, RoleId: identity.RoleAdminID,
			})
			if err != nil {
				return fmt.Errorf("init: create admin user: %w", err)
			}
			tok, err := c.Tokens.CreateToken(auth, &identityv1.CreateTokenRequest{
				Name: userName + "-cli", RoleId: identity.RoleAdminID, UserId: user.GetUser().GetId(),
			})
			if err != nil {
				return fmt.Errorf("init: mint cli token: %w", err)
			}

			// 4. 写凭据文件（0600）。
			if err := writeCredentials(addr, tok.GetSecret()); err != nil {
				return err
			}
			credPath, _ := credentialsPath()

			// 5. 默认吊销 bootstrap。
			revoked := false
			if !keepBootstrap {
				if _, err := c.Tokens.RevokeToken(auth, &identityv1.RevokeTokenRequest{Id: who.GetTokenId()}); err != nil {
					return fmt.Errorf("init: revoke bootstrap token: %w (credentials are saved; revoke it manually later)", err)
				}
				revoked = true
			}

			report := initReport{
				User: userName, UserID: user.GetUser().GetId(),
				TokenName: tok.GetToken().GetName(), TokenID: tok.GetToken().GetId(),
				Secret: tok.GetSecret(), Addr: addr,
				CredentialsPath:  credPath,
				BootstrapRevoked: revoked,
			}
			if jsonOut {
				return writeJSON(env.Stdout, report)
			}
			_, err = fmt.Fprintf(env.Stdout,
				"initialized fleetly at %s\nadmin user %s (id %s)\ncli token %s (id %s)\n"+
					"secret (shown once): %s\ncredentials saved: %s\n",
				addr, userName, report.UserID, report.TokenName, report.TokenID, report.Secret, credPath)
			if err != nil {
				return err
			}
			if revoked {
				_, _ = fmt.Fprintln(env.Stdout, "bootstrap token revoked")
			} else {
				_, _ = fmt.Fprintln(env.Stdout, "bootstrap token kept alive (revoke it with 'fleetly tokens revoke <id>')")
			}
			next := "next: fleetly whoami"
			if addr != defaultAddr {
				next = "next: set FLEETLY_ADDR=" + addr + " (or pass --addr), then fleetly whoami"
			}
			_, _ = fmt.Fprintln(env.Stdout, next)
			return nil
		},
	}
}
