package cmd

// Platform 组动词（F2.3，ADR-0039 决策 10）：Platform Backup 的手动触发
// 与快照列举。升级序的预备动词（ADR-0015：Platform Backup 前置——升级
// 脚本先 `platform backup` 拿到成功快照再动二进制）。

import (
	"context"
	"flag"
	"fmt"

	"github.com/lynx-go/commands"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
)

// platform backup：同步执行（restic 链对数据根是本地操作，时长有上界——
// 与数据库轨"触发不等执行"分立）。
func newPlatformBackupVerb() commands.Command {
	const name = "backup"
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Run a platform backup now (synchronous; the snapshot id rides the response)",
		usage:    "platform backup [--idempotency-key K]",
		setFlags: func(fs *flag.FlagSet) { idem.declare(fs) },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			// restic 链跑在请求内：豁免请求级 deadline（BackupTimeout 服务端
			// 兜底）。
			ctx, cancel, c, err := dialFromEnv(ctx, noDeadline())
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Platform.TriggerPlatformBackup(ctx, &systemv1.TriggerPlatformBackupRequest{})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetSnapshot(), func() {
				s := resp.GetSnapshot()
				_, _ = fmt.Fprintf(env.Stdout, "platform backup snapshot %s (taken %s on %s)\n",
					s.GetId(), s.GetTime(), s.GetHostname())
			})
		},
	}
}

// platform backups：快照列举，新→旧分页（restic 仓直读）。
func newPlatformBackupsVerb() commands.Command {
	const name = "backups"
	var after string
	var limit int
	return &flaggedVerb{
		name:     name,
		synopsis: "List platform backup snapshots (newest first)",
		usage:    "platform backups [--after SNAPSHOT_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&after, "after", "", "pagination cursor: the last snapshot id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
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
			resp, err := c.Platform.ListPlatformBackups(ctx, &systemv1.ListPlatformBackupsRequest{
				AfterSnapshotId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tTIME\tHOSTNAME")
				for _, s := range resp.GetSnapshots() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", s.GetId(), s.GetTime(), s.GetHostname())
				}
			})
		},
	}
}
