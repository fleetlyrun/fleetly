package cmd

// login / whoami 与本地凭据文件（F0.22）：`login` 经 WhoAmI 验证后把
// addr+token 存入用户配置目录（0600）；解析序 显式旗标 > FLEETLY_* env >
// 凭据文件 > 内建默认。凭据文件只从 os.UserConfigDir 解析——绝不读 cwd
//（与不自动加载 .env 同一威胁模型：不可信目录不得重定向 endpoint 窃取
// Token）。

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lynx-go/commands"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	"github.com/fleetlyrun/fleetly/internal/identity"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// envCredentials 覆盖凭据文件路径（测试接缝；生产空串走 UserConfigDir）。
const envCredentials = "FLEETLY_CREDENTIALS" //nolint:gosec // 环境变量名，非硬编码凭据

const credentialsDir = "fleetly"

// credentialsPath 返回凭据文件路径（env 覆盖优先）。
func credentialsPath() (string, error) {
	if p := os.Getenv(envCredentials); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config dir: %w", err)
	}
	return filepath.Join(dir, credentialsDir, "credentials"), nil
}

// readCredentials 解析凭据文件（缺失返回零值；行形态 "key = value"）。
func readCredentials() (addr, token string, err error) {
	path, err := credentialsPath()
	if err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(path) //nolint:gosec // 凭据文件自有路径
	if err != nil {
		if os.IsNotExist(err) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("read credentials: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "addr":
			addr = v
		case "token":
			token = v
		}
	}
	return addr, token, nil
}

// writeCredentials 落凭据文件（目录 0700、文件 0600——Windows 权限是
// 咨询性提示，POSIX 面强制）。
func writeCredentials(addr, token string) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	body := "addr = " + addr + "\ntoken = " + token + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write credentials: %w", err)
	}
	return nil
}

func newLoginVerb() commands.Command {
	const name = "login"
	var token, addr, loginName, password string
	return &flaggedVerb{
		name:     name,
		synopsis: "Validate a token, or log in with name+password (a platform token is minted server-side), and save it locally (addr+token, file mode 0600)",
		usage:    "login (--token TOKEN | --name NAME [--password PASSWORD]) [--addr ADDR]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&token, "token", "", "token to validate and store (exclusive with --name)")
			fs.StringVar(&loginName, "name", "", "user name for password login (exclusive with --token)")
			fs.StringVar(&password, "password", "", "password for --name (empty = read stdin; avoid passing secrets in argv)")
			fs.StringVar(&addr, "addr", "", fmt.Sprintf("fleetlyd address (default %q; env %s)", defaultAddr, envAddr))
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) > 0 {
				return usageErr(name, "unexpected argument(s)")
			}
			if token == "" && loginName == "" {
				return usageErr(name, "--token or --name is required")
			}
			if token != "" && loginName != "" {
				return usageErr(name, "--token and --name are exclusive")
			}
			if addr == "" {
				addr = envOr(envAddr, defaultAddr)
			}
			c, err := dialClient(addr)
			if err != nil {
				return err
			}
			defer c.Close() //nolint:errcheck // 进程退出路径
			if token == "" {
				// 密码形态（C6 第一期）：服务端校验并铸 Token，本地只落
				// 返回的 secret（与 --token 形态同一 credentials 通道）。
				// 密码 stdin 兜底（valueOrStdin 同款）。
				pw, err := valueOrStdin(password, env)
				if err != nil {
					return err
				}
				pw = strings.TrimSpace(pw)
				if pw == "" {
					return usageErr(name, "--password is required (or pipe it via stdin)")
				}
				resp, err := c.Users.Login(ctx, &identityv1.LoginRequest{Name: loginName, Password: pw})
				if err != nil {
					return err
				}
				token = resp.GetSecret()
			}
			if identity.TokenKind(token) != "platform" {
				return fmt.Errorf("login: token must be a platform token (%s prefix)", identity.TokenPrefix)
			}
			// login 的凭证只在本动词生效：显式 WithToken，不读环境/文件。
			who, err := c.Users.WhoAmI(fleetly.WithToken(ctx, token), &identityv1.WhoAmIRequest{})
			if err != nil {
				return err
			}
			if err := writeCredentials(addr, token); err != nil {
				return err
			}
			return renderOut(env, jsonOut, who, func() {
				actor := who.GetUserName()
				if actor == "" {
					actor = "token:" + who.GetTokenName()
				}
				_, _ = fmt.Fprintf(env.Stdout, "logged in as %s (role %s, team %s)\ncredentials saved\n",
					actor, who.GetRoleName(), who.GetTeamId())
			})
		},
	}
}

func newWhoamiVerb() commands.Command {
	return &flaggedVerb{
		name:     "whoami",
		synopsis: "Show the identity behind the current token",
		usage:    "whoami",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) > 0 {
				return usageErr("whoami", "unexpected argument(s)")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Users.WhoAmI(ctx, &identityv1.WhoAmIRequest{})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				who := resp.GetUserName()
				if who == "" {
					who = "(no user attached)"
				}
				_, _ = fmt.Fprintf(env.Stdout, "token %s (id %s)\nuser %s\nrole %s\nteam %s\nscopes %v\n",
					resp.GetTokenName(), resp.GetTokenId(), who, resp.GetRoleName(), resp.GetTeamId(), resp.GetScopes())
			})
		},
	}
}
