package cmd

// Platform 组动词（F2.3，ADR-0039 决策 10）：Platform Backup 的手动触发
// 与快照列举；控制面自身日志读取（ADR-0091）。升级序的预备动词（ADR-0015：
// Platform Backup 前置——升级脚本先 `platform backup` 拿到成功快照再动
// 二进制）。

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/lynxland/commands"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
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
			return runRPC(ctx, []dialOption{noDeadline()}, func(ctx context.Context, c *fleetly.Client) error {
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
		usage:    "platform backups [--after SEQ] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&after, "after", "", "pagination cursor: the snapshot id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
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
			})
		},
	}
}

// platform logs：控制面自身日志（daemon JSON 行镜像文件的服务端直读，
// ADR-0091）——控制面排障不经主机登录：gRPC 请求/失败行、内部错误 cause
// （error_id 关联）皆在此可达。--error-id 是 --text 的糖（E_INTERNAL 报错
// 打印的 error_id 直查 cause 全文——错误三件套闭环的第三件）。
func newPlatformLogsVerb() commands.Command {
	const name = "logs"
	var since, until, text, errorID string
	var tail int64
	var follow bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Read the control plane's own logs (the daemon's mirrored log file, server-side)",
		usage:    "platform logs [--tail N] [--since T] [--until T] [--text SUBSTRING | --error-id ID] [--follow]",
		setFlags: func(fs *flag.FlagSet) {
			fs.Int64Var(&tail, "tail", 200, "tail lines (0 = the whole retained window)")
			fs.StringVar(&since, "since", "", "time window start (RFC3339, e.g. 2026-11-03T00:00:00Z)")
			fs.StringVar(&until, "until", "", "time window end (RFC3339)")
			fs.StringVar(&text, "text", "", "substring filter (case-sensitive)")
			fs.StringVar(&errorID, "error-id", "", "filter by error_id (sugar for --text; joins the E_INTERNAL envelope)")
			fs.BoolVar(&follow, "follow", false, "keep streaming new lines")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if text != "" && errorID != "" {
				return usageErr(name, "--text and --error-id are mutually exclusive (--error-id is --text sugar)")
			}
			if errorID != "" {
				text = errorID
			}
			// 流式动词：拨号豁免请求级 deadline（follow 会话长存活；服务端
			// Follow 面以 ctx 取消收流，events follow 同款纪律）。
			return runRPC(ctx, []dialOption{noDeadline()}, func(ctx context.Context, c *fleetly.Client) error {
				stream, err := c.Platform.StreamPlatformLogs(ctx, &systemv1.StreamPlatformLogsRequest{
					TailLines: tail, Follow: follow, Since: since, Until: until, Text: text,
				})
				if err != nil {
					return err
				}
				for {
					frame, rerr := stream.Recv()
					if rerr == io.EOF {
						return nil
					}
					if rerr != nil {
						return rerr
					}
					if jsonOut {
						// NDJSON = 原始镜像行原样透传（line 本身就是 daemon
						// 的 JSON 对象；protojson 会把 bytes 包成 base64，
						// 反而让消费者多解一层）。
						_, _ = fmt.Fprintln(env.Stdout, string(frame.GetLine()))
						continue
					}
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", frame.GetTime(), frame.GetLevel(), frame.GetMsg())
				}
			})
		},
	}
}
