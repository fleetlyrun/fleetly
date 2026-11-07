package cmd

// pubsubs 动词组（B4b，ADR-0096 决策 8：databases 组同构——create/list/get/
// update/delete/backup/backups/verify/download-backup/rotate-password；无
// browse〔ADR-0051 注册表外语官既有拒〕）。凭证值永不回显；连接面 = pub-<id>
// 网内 DNS + 4222；App 经 pubsub_refs 注入 FLEETLY_PUB_*。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/lynxland/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func newPubSubsCreateVerb() commands.Command {
	const name = "create"
	var project, engine, version string
	var idem idemKeyFlag
	var restoreFrom string
	var settings multiFlag
	var cpuMillis, memoryMb int
	return &flaggedVerb{
		name: name, synopsis: "Create a pubsub instance from a template (nats)",
		usage: "pubsubs create --project PROJECT_ID --engine ENGINE [--version VERSION] [--set KEY=VALUE]... [--cpu-millis N] [--memory-mb N] [--restore-from-backup BACKUP_ID] [--idempotency-key K] NAME",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "owning project id or name (required)")
			fs.StringVar(&engine, "engine", "", "template engine, e.g. nats or kafka (required; list with 'fleetly engines list')")
			fs.StringVar(&version, "version", "", "engine major version short name (empty = the engine default)")
			fs.Var(&settings, "set", "engine knob KEY=VALUE (repeatable; schema: 'fleetly engines list')")
			fs.IntVar(&cpuMillis, "cpu-millis", 0, "cpu limit in milli-cores (0 = unlimited)")
			fs.IntVar(&memoryMb, "memory-mb", 0, "memory limit in MiB (0 = unlimited)")
			fs.StringVar(&restoreFrom, "restore-from-backup", "",
				"restore this backup id into the new instance (engine must match; streams are restored one by one)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one NAME argument")
			}
			if project == "" || engine == "" {
				return usageErr(name, "--project and --engine are required")
			}
			knobs, err := parseKnobSettings(settings)
			if err != nil {
				return usageErr(name, err.Error())
			}
			var resources *structurev1.DatabaseResources
			if cpuMillis != 0 || memoryMb != 0 {
				if cpuMillis < 0 || cpuMillis > math.MaxInt32 || memoryMb < 0 || memoryMb > math.MaxInt32 {
					return usageErr(name, "--cpu-millis/--memory-mb out of range")
				}
				resources = &structurev1.DatabaseResources{CpuMillis: int32(cpuMillis), MemoryMb: int32(memoryMb)} //nolint:gosec // 界检在上
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				projectID, err := resolveProjectID(ctx, c, project)
				if err != nil {
					return err
				}
				ctx = idem.bind(ctx)
				resp, err := c.PubSubs.CreatePubSub(ctx, &structurev1.CreatePubSubRequest{
					ProjectId: projectID, Name: args[0], Engine: engine, RestoreFromBackup: restoreFrom,
					Version: version, Settings: knobs, Resources: resources,
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetPubsub(), func() {
					p := resp.GetPubsub()
					_, _ = fmt.Fprintf(env.Stdout,
						"created pubsub %s (id %s)\nengine %s %s\ncredential secret %s (value never shown; reference it from app process pubsub_refs or secret_refs)\nconnect in-network at %s:%d\n",
						p.GetName(), p.GetId(), p.GetEngine(), p.GetVersion(), p.GetCredentialsRef(), p.GetHost(), p.GetPort())
					if s := p.GetSettings(); len(s) > 0 {
						_, _ = fmt.Fprintf(env.Stdout, "settings %s\n", joinKnobSettings(s))
					}
					if r := p.GetResources(); r != nil {
						_, _ = fmt.Fprintf(env.Stdout, "resources cpu %d millis, memory %d MiB\n", r.GetCpuMillis(), r.GetMemoryMb())
					}
					if restoreFrom != "" {
						_, _ = fmt.Fprintf(env.Stdout, "restore pending from backup %s (streams are restored once the instance is running)\n", restoreFrom)
					}
					if note := resp.GetRestoreVersionNote(); note != "" {
						_, _ = fmt.Fprintf(env.Stdout, "warning: %s\n", note)
					}
				})
			})
		},
	}
}

func newPubSubsListVerb() commands.Command {
	const name = "list"
	var project string
	var pages pageFlags
	return &flaggedVerb{
		name: name, synopsis: "List pubsub instances in a project (newest first)",
		usage: "pubsubs list --project PROJECT_ID [--after SEQ] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (required)")
			pages.register(fs, "after")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if project == "" {
				return usageErr(name, "--project is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				projectID, err := resolveProjectID(ctx, c, project)
				if err != nil {
					return err
				}
				resp, err := c.PubSubs.ListPubSubs(ctx, &structurev1.ListPubSubsRequest{
					ProjectId: projectID, AfterPubsubSeq: pages.cursor(), Limit: pages.limit32(),
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintln(env.Stdout, "NAME\tID\tENGINE\tVERSION\tSTATUS\tCREDENTIALS\tCREATED")
					for _, p := range resp.GetPubsubs() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
							p.GetId(), p.GetName(), p.GetEngine(), p.GetVersion(), p.GetStatus(), p.GetCredentialsRef(), p.GetCreatedAt())
					}
				})
			})
		},
	}
}

func newPubSubsGetVerb() commands.Command {
	const name = "get"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Show one pubsub instance (connection face without credentials)",
		usage: "pubsubs get [--project PROJECT] PUBSUB_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one PUBSUB_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolvePubSubID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.PubSubs.GetPubSub(ctx, &structurev1.GetPubSubRequest{Id: id})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetPubsub(), func() {
					p := resp.GetPubsub()
					_, _ = fmt.Fprintf(env.Stdout,
						"id %s\nname %s\nengine %s %s\nstatus %s\ncredential secret %s (value never shown)\nin-network endpoint %s:%d\ncreated %s\n",
						p.GetId(), p.GetName(), p.GetEngine(), p.GetVersion(), p.GetStatus(),
						p.GetCredentialsRef(), p.GetHost(), p.GetPort(), p.GetCreatedAt())
					if s := p.GetSettings(); len(s) > 0 {
						_, _ = fmt.Fprintf(env.Stdout, "settings %s\n", joinKnobSettings(s))
					}
					if r := p.GetResources(); r != nil {
						_, _ = fmt.Fprintf(env.Stdout, "resources %s\n", describeResources(r))
					}
				})
			})
		},
	}
}

// pubsubs update（B4c 确认面三家族对齐，ADR-0089/0095 决策 9）：期望态补丁
// （--set/--unset 旋钮 + 资源面）——ConfirmableVerbs 强制集，铸票面 =
// PreviewUpdatePubSub。确认三形态与补丁语义 = databases update 同款（读行
// 叠加补丁整集下发；两调用流第 2 步须携同一组旗标）。本族首批旋钮全
// static：预演恒 replace 形态（instant lane 恒空）。
func newPubSubsUpdateVerb() commands.Command {
	const name = "update"
	var project string
	var settings, unset multiFlag
	var cpu, memory optionalInt
	conf := confirmFlags{previewFace: true}
	return &flaggedVerb{
		name:     name,
		synopsis: "Update a pubsub instance's desired state (knobs and limits roll the carrier through a supervised restart)",
		usage:    "pubsubs update [--project PROJECT] [--set KEY=VALUE]... [--unset KEY]... [--cpu-millis N] [--memory-mb N] [--yes | --preview | --ticket T] PUBSUB_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
			fs.Var(&settings, "set", "engine knob KEY=VALUE to set (repeatable; patch: knobs not named keep their current values)")
			fs.Var(&unset, "unset", "engine knob KEY to remove (repeatable)")
			fs.Var(&cpu, "cpu-millis", "cpu limit in milli-cores (0 = unlimited; omitted = unchanged)")
			fs.Var(&memory, "memory-mb", "memory limit in MiB (0 = unlimited; omitted = unchanged)")
			conf.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one PUBSUB_ID argument")
			}
			patch, err := parseKnobSettings(settings)
			if err != nil {
				return usageErr(name, err.Error())
			}
			for _, key := range unset {
				if key == "" {
					return usageErr(name, "--unset expects a knob KEY")
				}
				if _, both := patch[key]; both {
					return usageErr(name, fmt.Sprintf("knob %q is both set and unset (pick one)", key))
				}
			}
			if len(patch) == 0 && len(unset) == 0 && !cpu.set && !memory.set {
				return usageErr(name, "nothing to update: pass --set/--unset and/or --cpu-millis/--memory-mb")
			}
			for _, axis := range []optionalInt{cpu, memory} {
				if axis.set && (axis.value < 0 || axis.value > math.MaxInt32) {
					return usageErr(name, "--cpu-millis/--memory-mb out of range")
				}
			}
			// 确认三形态解析在拨号前（互斥校验与非 TTY 拒绝都是本地面）。
			plan, err := conf.resolve("pubsubs update")
			if err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolvePubSubID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				// 合并基线 = 行当前期望态（补丁语义的读半边）。
				current, err := c.PubSubs.GetPubSub(ctx, &structurev1.GetPubSubRequest{Id: id})
				if err != nil {
					return err
				}
				before := current.GetPubsub()
				req := &structurev1.UpdatePubSubRequest{PubsubId: id}
				if len(patch) > 0 || len(unset) > 0 {
					req.Settings = &structurev1.DatabaseSettingsUpdate{
						Values: mergeKnobSettings(before.GetSettings(), patch, unset),
					}
				}
				if cpu.set || memory.set {
					req.Resources = mergeResources(before.GetResources(), cpu, memory)
				}
				// 确认前半：--preview 打印即退出；交互默认打印预演后 y/N——
				// y 以预演票带票直发，N 中止（零写请求）。
				if plan.mode == confirmPreviewOnly || plan.mode == confirmInteractive {
					preview, perr := c.PubSubs.PreviewUpdatePubSub(ctx, &structurev1.PreviewUpdatePubSubRequest{Update: req})
					if perr != nil {
						return perr
					}
					if rerr := renderPubSubUpdatePreview(env, jsonOut, args[0], preview); rerr != nil {
						return rerr
					}
					if plan.mode == confirmPreviewOnly {
						return nil
					}
					confirmed, cerr := promptConfirm(env, "Update pubsub")
					if cerr != nil {
						return cerr
					}
					if !confirmed {
						_, _ = fmt.Fprintln(env.Stdout, "aborted (nothing was sent)")
						return nil
					}
					req.Confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
				} else {
					req.Confirmation = plan.confirmation()
					if plan.mode == confirmSkip && !jsonOut {
						_, _ = fmt.Fprintln(env.Stdout, confirmSkipNote)
					}
				}
				resp, err := c.PubSubs.UpdatePubSub(ctx, req)
				if err != nil {
					return presentConfirmError(err)
				}
				return renderOut(env, jsonOut, resp, func() { renderPubSubUpdateResult(env, before, resp) })
			})
		},
	}
}

// renderPubSubUpdatePreview 渲染 pubsub Update 预演（机器形态 =
// PreviewUpdatePubSubResponse protojson；人类形态走两族共享体）。
func renderPubSubUpdatePreview(env *commands.Environment, jsonOut bool, pubsub string, resp *structurev1.PreviewUpdatePubSubResponse) error {
	if jsonOut {
		return writeProtoJSON(env.Stdout, resp)
	}
	return renderUpdatePreviewBody(env, "pubsub "+pubsub,
		resp.GetSettingsChanges(), resp.GetResourcesFrom(), resp.GetResourcesTo(),
		resp.GetDynamicKeys(), resp.GetStaticKeys(), resp.GetRollout(), resp.GetTicket(), resp.GetTicketExpiresIn())
}

// renderPubSubUpdateResult 渲染 pubsub Update 回执（renderUpdateResult 双
// 车道形态——B6a kafka 起 instant 车道在册：nats 引擎 dynamic 恒空，输出
// 与单车道时代逐位一致）。
func renderPubSubUpdateResult(env *commands.Environment, before *structurev1.PubSub, resp *structurev1.UpdatePubSubResponse) {
	p := resp.GetPubsub()
	lines := []string{fmt.Sprintf("updated pubsub %s (id %s)", p.GetName(), p.GetId())}
	resourcesChanged := describeResources(before.GetResources()) != describeResources(p.GetResources())
	if len(resp.GetDynamicApplied()) > 0 {
		lines = append(lines, "instant knobs now in effect (no restart): "+strings.Join(resp.GetDynamicApplied(), ", "))
	}
	if len(resp.GetStaticApplied()) > 0 {
		lines = append(lines, "rolling knobs saved (the carrier rolls to pick them up): "+strings.Join(resp.GetStaticApplied(), ", "))
	}
	if resourcesChanged {
		lines = append(lines, "resources now "+describeResources(p.GetResources())+" (the carrier rolls to pick them up)")
	}
	if len(resp.GetDynamicApplied()) == 0 && len(resp.GetStaticApplied()) == 0 && !resourcesChanged {
		lines = append(lines, "no changes (the request matches the current desired state)")
	}
	if s := p.GetSettings(); len(s) == 0 {
		lines = append(lines, "settings: none (engine defaults)")
	} else {
		lines = append(lines, "settings: "+joinKnobSettings(s))
	}
	if len(resp.GetStaticApplied()) > 0 || resourcesChanged {
		lines = append(lines, "watch the roll with 'fleetly pubsubs get' until status is running again")
	}
	_, _ = fmt.Fprintln(env.Stdout, strings.Join(lines, "\n"))
}

func newPubSubsDeleteVerb() commands.Command {
	const name = "delete"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Delete a pubsub instance (tears down carriers; volume and credential secret are retained)",
		usage: "pubsubs delete [--project PROJECT] PUBSUB_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one PUBSUB_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolvePubSubID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.PubSubs.DeletePubSub(ctx, &structurev1.DeletePubSubRequest{Id: id})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintf(env.Stdout,
						"deleted pubsub %s (carriers torn down; data volume and credential secret retained)\n", args[0])
				})
			})
		},
	}
}

func newPubSubsBackupVerb() commands.Command {
	const name = "backup"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Trigger a per-stream backup now (returns a pending ledger row; memory-storage streams are refused)",
		usage: "pubsubs backup [--project PROJECT] PUBSUB_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one PUBSUB_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolvePubSubID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.PubSubs.TriggerPubSubBackup(ctx, &structurev1.TriggerPubSubBackupRequest{PubsubId: id})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetBackup(), func() {
					b := resp.GetBackup()
					_, _ = fmt.Fprintf(env.Stdout,
						"backup triggered (id %s) status %s\ncompletion lands in database.backup_succeeded / database.backup_failed events (aggregate pubsub)\n",
						b.GetId(), b.GetStatus())
				})
			})
		},
	}
}

func newPubSubsBackupsVerb() commands.Command {
	const name = "backups"
	var project string
	var pages pageFlags
	return &flaggedVerb{
		name: name, synopsis: "List backups of a pubsub instance (newest first)",
		usage: "pubsubs backups [--project PROJECT] PUBSUB_ID [--after SEQ] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
			pages.register(fs, "after")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one PUBSUB_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolvePubSubID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.PubSubs.ListPubSubBackups(ctx, &structurev1.ListPubSubBackupsRequest{
					PubsubId: id, AfterBackupSeq: pages.cursor(), Limit: pages.limit32(),
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintln(env.Stdout, "ID\tSTATUS\tSIZE\tOBJECT\tFINISHED\tERROR")
					for _, b := range resp.GetBackups() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%d\t%s\t%s\t%s\n",
							b.GetId(), b.GetStatus(), b.GetSizeBytes(), b.GetObjectKey(), b.GetFinishedAt(), b.GetError())
					}
				})
			})
		},
	}
}

func newPubSubsVerifyVerb() commands.Command {
	const name = "verify"
	var project, pubsubRef string
	return &flaggedVerb{
		name: name, synopsis: "Verify a backup object set (manifest-driven per-member digest recomputation)",
		usage: "pubsubs verify --pubsub PUBSUB_ID [--project PROJECT] BACKUP_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsubRef, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BACKUP_ID argument")
			}
			if pubsubRef == "" {
				return usageErr(name, "--pubsub is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolvePubSubID(ctx, c, project, pubsubRef)
				if err != nil {
					return err
				}
				resp, err := c.PubSubs.VerifyPubSubBackup(ctx, &structurev1.VerifyPubSubBackupRequest{PubsubId: id, BackupId: args[0]})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					if resp.GetOk() {
						_, _ = fmt.Fprintf(env.Stdout, "backup %s verified (digest %s)\n", args[0], resp.GetDigest())
						return
					}
					_, _ = fmt.Fprintf(env.Stdout, "backup %s FAILED verification: %s\n", args[0], resp.GetError())
				})
			})
		},
	}
}

func newPubSubsDownloadBackupVerb() commands.Command {
	const name = "download-backup"
	var project, pubsubRef, out string
	return &flaggedVerb{
		name:     name,
		synopsis: "Download a succeeded backup object set to a local file (members concatenated in manifest order)",
		usage:    "pubsubs download-backup --pubsub PUBSUB_ID [--project PROJECT] BACKUP_ID --out PATH",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&pubsubRef, "pubsub", "", "owning pubsub id or name (required)")
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
			fs.StringVar(&out, "out", "", "output file path (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BACKUP_ID argument")
			}
			if pubsubRef == "" {
				return usageErr(name, "--pubsub is required")
			}
			if out == "" {
				return usageErr(name, "--out is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolvePubSubID(ctx, c, project, pubsubRef)
				if err != nil {
					return err
				}
				stream, err := c.PubSubs.DownloadPubSubBackup(ctx, &structurev1.DownloadPubSubBackupRequest{PubsubId: id, BackupId: args[0]})
				if err != nil {
					return err
				}
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
					data, merr := json.Marshal(&downloadSummary{Database: id, Backup: args[0], Path: out, Bytes: size})
					if merr != nil {
						return merr
					}
					_, _ = fmt.Fprintln(env.Stdout, string(data))
					return nil
				}
				_, _ = fmt.Fprintf(env.Stdout, "downloaded %s (%d bytes) → %s\n", args[0], size, out)
				return nil
			})
		},
	}
}

func newPubSubsRotatePasswordVerb() commands.Command {
	const name = "rotate-password"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Rotate the platform credential (declarative: the secret is rewritten and the instance restarts with the new user password)",
		usage: "pubsubs rotate-password [--project PROJECT] PUBSUB_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare pubsub name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one PUBSUB_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolvePubSubID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.PubSubs.RotatePubSubPassword(ctx, &structurev1.RotatePubSubPasswordRequest{PubsubId: id})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetPubsub(), func() {
					p := resp.GetPubsub()
					_, _ = fmt.Fprintf(env.Stdout,
						"rotated credential for pubsub %s (secret %s rewritten; value never shown)\napps referencing it keep the old value until redeployed\n",
						p.GetName(), p.GetCredentialsRef())
				})
			})
		},
	}
}
