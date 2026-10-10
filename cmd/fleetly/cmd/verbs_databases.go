package cmd

// Databases 上下文动词（F1.12，ADR-0029）。全部 --json 双形态（golden 钉
// 死）；凭证值永不回显——create 回显 Secret 名（database:<name>），App 经
// process secret_refs 引用后取完整连接 URL。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lynx-go/commands"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
)

func newDatabasesCreateVerb() commands.Command {
	const name = "create"
	var project, engine string
	var idem idemKeyFlag
	var restoreFrom string
	return &flaggedVerb{
		name: name, synopsis: "Create a database from a template (postgres / pgvector / redis / mysql / mongo)",
		usage: "databases create --project PROJECT_ID --engine ENGINE NAME [--restore-from-backup BACKUP_ID]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "owning project id (required)")
			fs.StringVar(&engine, "engine", "", "template engine: postgres | pgvector | redis | mysql | mongo (required)")
			fs.StringVar(&restoreFrom, "restore-from-backup", "",
				"restore this backup id into the new database (engine must match; ADR-0039)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if project == "" || engine == "" {
				return usageErr(name, "--project and --engine are required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Databases.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
				ProjectId: project, Name: args[0], Engine: engine, RestoreFromBackup: restoreFrom,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetDatabase(), func() {
				_, _ = fmt.Fprintf(env.Stdout,
					"created database %s (id %s)\nengine %s %s\ncredential secret %s (value never shown; reference it from app process secret_refs)\nconnect in-network at %s:%d\n",
					resp.GetDatabase().GetName(), resp.GetDatabase().GetId(),
					resp.GetDatabase().GetEngine(), resp.GetDatabase().GetVersion(),
					resp.GetDatabase().GetCredentialsRef(),
					resp.GetDatabase().GetHost(), resp.GetDatabase().GetPort())
				if restoreFrom != "" {
					_, _ = fmt.Fprintf(env.Stdout, "restore pending from backup %s (watch database.backup_* events)\n", restoreFrom)
				}
			})
		},
	}
}

func newDatabasesListVerb() commands.Command {
	const name = "list"
	var project, after string
	var limit int
	return &flaggedVerb{
		name: name, synopsis: "List databases in a project (newest first)",
		usage: "databases list --project PROJECT_ID [--after DATABASE_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.StringVar(&after, "after", "", "pagination cursor: the last database id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if project == "" {
				return usageErr(name, "--project is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Databases.ListDatabases(ctx, &structurev1.ListDatabasesRequest{
				ProjectId: project, AfterDatabaseId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tNAME\tENGINE\tVERSION\tSTATUS\tCREDENTIALS\tCREATED")
				for _, d := range resp.GetDatabases() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
						d.GetId(), d.GetName(), d.GetEngine(), d.GetVersion(), d.GetStatus(), d.GetCredentialsRef(), d.GetCreatedAt())
				}
			})
		},
	}
}

func newDatabasesGetVerb() commands.Command {
	const name = "get"
	return &flaggedVerb{
		name: name, synopsis: "Show one database (connection face without credentials)",
		usage: "databases get DATABASE_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Databases.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetDatabase(), func() {
				d := resp.GetDatabase()
				_, _ = fmt.Fprintf(env.Stdout,
					"id %s\nname %s\nengine %s %s\nstatus %s\ncredential secret %s (value never shown)\nin-network endpoint %s:%d\ncreated %s\n",
					d.GetId(), d.GetName(), d.GetEngine(), d.GetVersion(), d.GetStatus(),
					d.GetCredentialsRef(), d.GetHost(), d.GetPort(), d.GetCreatedAt())
			})
		},
	}
}

// databases delete（ADR-0029 决策 8）：收口删除——拆载体 + tombstone；
// 数据卷与凭证 Secret 残留（Project 级材料，备份保留义）。
func newDatabasesDeleteVerb() commands.Command {
	const name = "delete"
	return &flaggedVerb{
		name: name, synopsis: "Delete a database (tears down carriers; volume and credential secret are retained)",
		usage: "databases delete DATABASE_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Databases.DeleteDatabase(ctx, &structurev1.DeleteDatabaseRequest{Id: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintf(env.Stdout,
					"deleted database %s (carriers torn down; data volume and credential secret retained)\n", args[0])
			})
		},
	}
}

// databases backup（F2.2，ADR-0039）：手动触发——铸台账行即返，完成事实
// 由事件与 backups 列表观测（备份时长无上界承诺，同步等待面不成立）。
func newDatabasesBackupVerb() commands.Command {
	const name = "backup"
	return &flaggedVerb{
		name: name, synopsis: "Trigger a backup now (returns a pending ledger row; watch database.backup_* events)",
		usage: "databases backup DATABASE_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Databases.TriggerBackup(ctx, &structurev1.TriggerBackupRequest{DatabaseId: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetBackup(), func() {
				b := resp.GetBackup()
				_, _ = fmt.Fprintf(env.Stdout,
					"backup triggered (id %s) status %s\ncompletion lands in database.backup_succeeded / database.backup_failed events\n",
					b.GetId(), b.GetStatus())
			})
		},
	}
}

// databases backups（F2.2，ADR-0039）：台账列举，新→旧分页。
func newDatabasesBackupsVerb() commands.Command {
	const name = "backups"
	var after string
	var limit int
	return &flaggedVerb{
		name: name, synopsis: "List backups of a database (newest first)",
		usage: "databases backups DATABASE_ID [--after BACKUP_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&after, "after", "", "pagination cursor: the last backup id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Databases.ListBackups(ctx, &structurev1.ListBackupsRequest{
				DatabaseId: args[0], AfterBackupId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tSTATUS\tENGINE\tSIZE\tDIGEST\tOBJECT\tFINISHED")
				for _, b := range resp.GetBackups() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
						b.GetId(), b.GetStatus(), b.GetEngine(), b.GetSizeBytes(), b.GetDigest(), b.GetObjectKey(), b.GetFinishedAt())
				}
			})
		},
	}
}

// databases verify（F2.2，ADR-0039 决策 8）：重算对象摘要比对回执。
func newDatabasesVerifyVerb() commands.Command {
	const name = "verify"
	return &flaggedVerb{
		name: name, synopsis: "Verify a backup object (recomputes sha256 against the ledger receipt)",
		usage: "databases verify BACKUP_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BACKUP_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Databases.VerifyBackup(ctx, &structurev1.VerifyBackupRequest{BackupId: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				if resp.GetOk() {
					_, _ = fmt.Fprintf(env.Stdout, "backup %s verified (digest %s matches the ledger receipt)\n",
						args[0], resp.GetDigest())
					return
				}
				_, _ = fmt.Fprintf(env.Stdout, "backup %s FAILED verification: %s\n", args[0], resp.GetError())
			})
		},
	}
}

// databases browse（F3.6，ADR-0051）：铸造数据浏览器会话——打印入口 URL
// （含一次性 Launcher Ticket，120s 单用途；兑换即烧，过期重开）。不自动开
// 浏览器：CLI 面以脚本消费为主（URL 是唯一交付物）。
// databases download-backup（IA v3 二期⑤）：流式下载备份对象到本地文件
// （分块重组；--out 必填——不落 stdout，二进制不进管道文本面）。
func newDatabasesDownloadBackupVerb() commands.Command {
	const name = "download-backup"
	var database, out string
	return &flaggedVerb{
		name:     name,
		synopsis: "Download a succeeded backup object to a local file (streamed in chunks)",
		usage:    "databases download-backup --database DATABASE_ID BACKUP_ID --out PATH",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&database, "database", "", "owning database id (required)")
			fs.StringVar(&out, "out", "", "output file path (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BACKUP_ID argument")
			}
			if database == "" {
				return usageErr(name, "--database is required")
			}
			if out == "" {
				return usageErr(name, "--out is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			stream, err := c.Databases.DownloadBackup(ctx, &structurev1.DownloadBackupRequest{
				DatabaseId: database, BackupId: args[0],
			})
			if err != nil {
				return err
			}
			// 截断语义：先清目标文件（追加写只发生在本次下载窗口内）。
			if err := os.WriteFile(out, []byte{}, 0o600); err != nil {
				return err
			}
			var size int64
			for {
				frame, rerr := stream.Recv()
				if rerr == io.EOF {
					break
				}
				if rerr != nil {
					return rerr
				}
				if err := appendFile(out, frame.GetData()); err != nil {
					return err
				}
				size += int64(len(frame.GetData()))
			}
			if jsonOut {
				data, merr := json.Marshal(&downloadSummary{Database: database, Backup: args[0], Path: out, Bytes: size})
				if merr != nil {
					return merr
				}
				_, _ = fmt.Fprintln(env.Stdout, string(data))
				return nil
			}
			_, _ = fmt.Fprintf(env.Stdout, "downloaded %s (%d bytes) → %s\n", args[0], size, out)
			return nil
		},
	}
}

// downloadSummary 是 --json 形态的下载回执。
type downloadSummary struct {
	Database string `json:"database"`
	Backup   string `json:"backup"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
}

// databases rotate-password（IA v3 二期⑤b）：方言级凭证轮换——新连接串
// 只进凭证 Secret（值永不回显）；回执显式披露级联语义（引用库的 App 持
// 旧值直到重新部署）。位置参数沿 get/delete 惯例。
func newDatabasesRotatePasswordVerb() commands.Command {
	const name = "rotate-password"
	return &flaggedVerb{
		name:     name,
		synopsis: "Rotate the database credential (a new connection URL lands in the credential secret)",
		usage:    "databases rotate-password DATABASE_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Databases.RotateDatabasePassword(ctx, &structurev1.RotateDatabasePasswordRequest{DatabaseId: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetDatabase(), func() {
				d := resp.GetDatabase()
				_, _ = fmt.Fprintf(env.Stdout,
					"rotated credential for database %s (id %s)\ncredential secret %s now holds the new connection URL (value never shown)\ncascade: apps referencing this database keep the old credential until redeployed - redeploy them now\n",
					d.GetName(), d.GetId(), d.GetCredentialsRef())
			})
		},
	}
}

// appendFile 以追加语义落盘（先截断在首次写入前由调用方保证——此处
// 首帧前 O_TRUNC 由 downloadVerb 预清理实现）。
func appendFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304：path 是用户 --out 旗标（本地落盘即目的），非不可信包含
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = f.Write(data)
	return err
}

func newDatabasesBrowseVerb() commands.Command {
	const name = "browse"
	var write bool
	return &flaggedVerb{
		name: name, synopsis: "Open a data-browser session for a database (prints a one-time entry URL)",
		usage: "databases browse DATABASE_ID [--write]",
		setFlags: func(fs *flag.FlagSet) {
			fs.BoolVar(&write, "write", false, "unlock the write mode (requires the databases:write scope; default is read-only)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Databases.BrowseDatabase(ctx, &structurev1.BrowseDatabaseRequest{
				DatabaseId: args[0], ReadWrite: write,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintf(env.Stdout,
					"browse session %s opened (%s, read-only %t, enforcement %s)\nopen within %d seconds (one-time ticket):\n%s\n",
					resp.GetSessionId(), resp.GetBrowser(), resp.GetReadOnly(),
					shortEnforcement(resp.GetEnforcement()), resp.GetExpiresIn(), resp.GetUrl())
			})
		},
	}
}

// shortEnforcement 把执法层级枚举压成人类短形（session/tool/none——proto
// 枚举全名是 wire 面，终端面用词值）。
func shortEnforcement(e structurev1.BrowseReadOnlyEnforcement) string {
	switch e {
	case structurev1.BrowseReadOnlyEnforcement_BROWSE_READ_ONLY_ENFORCEMENT_SESSION:
		return "session"
	case structurev1.BrowseReadOnlyEnforcement_BROWSE_READ_ONLY_ENFORCEMENT_TOOL:
		return "tool"
	default:
		return "none"
	}
}
