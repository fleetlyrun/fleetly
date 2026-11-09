package cmd

// buckets 动词组（B5，ADR-0096 决策 8：pubsubs 组同构——create/list/get/update/
// delete/rotate-password；**无 backup 动词族**〔silo 转储挂账，目录
// capabilities.backup=false 如实；能力位裁剪不以拒绝文案占位〕；无 browse
// 〔ADR-0051 注册表外语官既有拒〕）。凭证值永不回显；连接面 = bkt-<id> 网内
// DNS + 9000（S3 API）；App 经 bucketstore_refs 注入 FLEETLY_BKT_<NAME>_
// {ENDPOINT|ACCESS_KEY|SECRET_KEY}。

import (
	"context"
	"flag"
	"fmt"
	"math"
	"strings"

	"github.com/lynxland/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

func newBucketsCreateVerb() commands.Command {
	const name = "create"
	var project, engine, version string
	var idem idemKeyFlag
	var settings multiFlag
	var cpuMillis, memoryMb int
	return &flaggedVerb{
		name: name, synopsis: "Create a bucket store instance from a template (silo)",
		usage: "buckets create --project PROJECT_ID --engine ENGINE [--version VERSION] [--set KEY=VALUE]... [--cpu-millis N] [--memory-mb N] [--idempotency-key K] NAME",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "owning project id or name (required)")
			fs.StringVar(&engine, "engine", "", "template engine: silo (required; list with 'fleetly engines list')")
			fs.StringVar(&version, "version", "", "engine version short name (empty = the engine default)")
			fs.Var(&settings, "set", "engine knob KEY=VALUE (repeatable; schema: 'fleetly engines list' — silo carries no knobs yet)")
			fs.IntVar(&cpuMillis, "cpu-millis", 0, "cpu limit in milli-cores (0 = unlimited)")
			fs.IntVar(&memoryMb, "memory-mb", 0, "memory limit in MiB (0 = unlimited)")
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
				resp, err := c.BucketStores.CreateBucketStore(ctx, &structurev1.CreateBucketStoreRequest{
					ProjectId: projectID, Name: args[0], Engine: engine,
					Version: version, Settings: knobs, Resources: resources,
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetBucketStore(), func() {
					b := resp.GetBucketStore()
					_, _ = fmt.Fprintf(env.Stdout,
						"created bucket store %s (id %s)\nengine %s %s\ncredential secret %s (value never shown; reference it from app process bucketstore_refs or secret_refs)\nS3 endpoint in-network at %s:%d\n",
						b.GetName(), b.GetId(), b.GetEngine(), b.GetVersion(), b.GetCredentialsRef(), b.GetHost(), b.GetPort())
					if s := b.GetSettings(); len(s) > 0 {
						_, _ = fmt.Fprintf(env.Stdout, "settings %s\n", joinKnobSettings(s))
					}
					if r := b.GetResources(); r != nil {
						_, _ = fmt.Fprintf(env.Stdout, "resources cpu %d millis, memory %d MiB\n", r.GetCpuMillis(), r.GetMemoryMb())
					}
				})
			})
		},
	}
}

func newBucketsListVerb() commands.Command {
	const name = "list"
	var project string
	var pages pageFlags
	return &flaggedVerb{
		name: name, synopsis: "List bucket store instances in a project (newest first)",
		usage: "buckets list --project PROJECT_ID [--after SEQ] [--limit N]",
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
				resp, err := c.BucketStores.ListBucketStores(ctx, &structurev1.ListBucketStoresRequest{
					ProjectId: projectID, AfterBucketStoreSeq: pages.cursor(), Limit: pages.limit32(),
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintln(env.Stdout, "NAME\tID\tENGINE\tVERSION\tSTATUS\tCREDENTIALS\tCREATED")
					for _, b := range resp.GetBucketStores() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
							b.GetId(), b.GetName(), b.GetEngine(), b.GetVersion(), b.GetStatus(), b.GetCredentialsRef(), b.GetCreatedAt())
					}
				})
			})
		},
	}
}

func newBucketsGetVerb() commands.Command {
	const name = "get"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Show one bucket store instance (connection face without credentials)",
		usage: "buckets get [--project PROJECT] BUCKET_STORE_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare bucket store name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BUCKET_STORE_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolveBucketStoreID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.BucketStores.GetBucketStore(ctx, &structurev1.GetBucketStoreRequest{Id: id})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetBucketStore(), func() {
					b := resp.GetBucketStore()
					_, _ = fmt.Fprintf(env.Stdout,
						"id %s\nname %s\nengine %s %s\nstatus %s\ncredential secret %s (value never shown)\nin-network S3 endpoint %s:%d\ncreated %s\n",
						b.GetId(), b.GetName(), b.GetEngine(), b.GetVersion(), b.GetStatus(),
						b.GetCredentialsRef(), b.GetHost(), b.GetPort(), b.GetCreatedAt())
					if s := b.GetSettings(); len(s) > 0 {
						_, _ = fmt.Fprintf(env.Stdout, "settings %s\n", joinKnobSettings(s))
					}
					if r := b.GetResources(); r != nil {
						_, _ = fmt.Fprintf(env.Stdout, "resources %s\n", describeResources(r))
					}
				})
			})
		},
	}
}

// buckets update（三种数据服务类型确认面同构，ADR-0089/0095 决策 9）：期望态补丁
// （resources 面；--set/--unset 旋钮面保留接口齐形——silo v1 零旋钮，受理
// 位拒任何键）——ConfirmableVerbs 强制集，铸票面 = PreviewUpdateBucketStore。
// 确认三形态与补丁语义 = pubsubs update 同款。
func newBucketsUpdateVerb() commands.Command {
	const name = "update"
	var project string
	var settings, unset multiFlag
	var cpu, memory optionalInt
	conf := confirmFlags{previewFace: true}
	return &flaggedVerb{
		name:     name,
		synopsis: "Update a bucket store instance's desired state (limits roll the carrier through a supervised restart)",
		usage:    "buckets update [--project PROJECT] [--set KEY=VALUE]... [--unset KEY]... [--cpu-millis N] [--memory-mb N] [--yes | --preview | --ticket T] BUCKET_STORE_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare bucket store name)")
			fs.Var(&settings, "set", "engine knob KEY=VALUE to set (repeatable; silo carries no knobs yet)")
			fs.Var(&unset, "unset", "engine knob KEY to remove (repeatable)")
			fs.Var(&cpu, "cpu-millis", "cpu limit in milli-cores (0 = unlimited; omitted = unchanged)")
			fs.Var(&memory, "memory-mb", "memory limit in MiB (0 = unlimited; omitted = unchanged)")
			conf.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BUCKET_STORE_ID argument")
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
			plan, err := conf.resolve("buckets update")
			if err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolveBucketStoreID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				current, err := c.BucketStores.GetBucketStore(ctx, &structurev1.GetBucketStoreRequest{Id: id})
				if err != nil {
					return err
				}
				before := current.GetBucketStore()
				req := &structurev1.UpdateBucketStoreRequest{BucketStoreId: id}
				if len(patch) > 0 || len(unset) > 0 {
					req.Settings = &structurev1.DatabaseSettingsUpdate{
						Values: mergeKnobSettings(before.GetSettings(), patch, unset),
					}
				}
				if cpu.set || memory.set {
					req.Resources = mergeResources(before.GetResources(), cpu, memory)
				}
				if plan.mode == confirmPreviewOnly || plan.mode == confirmInteractive {
					preview, perr := c.BucketStores.PreviewUpdateBucketStore(ctx, &structurev1.PreviewUpdateBucketStoreRequest{Update: req})
					if perr != nil {
						return perr
					}
					if rerr := renderBucketStoreUpdatePreview(env, jsonOut, args[0], preview); rerr != nil {
						return rerr
					}
					if plan.mode == confirmPreviewOnly {
						return nil
					}
					confirmed, cerr := promptConfirm(env, "Update bucket store")
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
				resp, err := c.BucketStores.UpdateBucketStore(ctx, req)
				if err != nil {
					return presentConfirmError(err)
				}
				return renderOut(env, jsonOut, resp, func() { renderBucketStoreUpdateResult(env, before, resp) })
			})
		},
	}
}

// renderBucketStoreUpdatePreview 渲染 bucket store Update 预演（机器形态 =
// PreviewUpdateBucketStoreResponse protojson；人类形态走三类型共享体）。
func renderBucketStoreUpdatePreview(env *commands.Environment, jsonOut bool, bucketStore string, resp *structurev1.PreviewUpdateBucketStoreResponse) error {
	if jsonOut {
		return writeProtoJSON(env.Stdout, resp)
	}
	return renderUpdatePreviewBody(env, "bucket store "+bucketStore,
		resp.GetSettingsChanges(), resp.GetResourcesFrom(), resp.GetResourcesTo(),
		resp.GetDynamicKeys(), resp.GetStaticKeys(), resp.GetRollout(), resp.GetTicket(), resp.GetTicketExpiresIn())
}

// renderBucketStoreUpdateResult 渲染 bucket store Update 回执
// （renderPubSubUpdateResult 同形）。
func renderBucketStoreUpdateResult(env *commands.Environment, before *structurev1.BucketStore, resp *structurev1.UpdateBucketStoreResponse) {
	b := resp.GetBucketStore()
	lines := []string{fmt.Sprintf("updated bucket store %s (id %s)", b.GetName(), b.GetId())}
	resourcesChanged := describeResources(before.GetResources()) != describeResources(b.GetResources())
	if len(resp.GetStaticApplied()) > 0 {
		lines = append(lines, "rolling knobs saved (the carrier rolls to pick them up): "+strings.Join(resp.GetStaticApplied(), ", "))
	}
	if resourcesChanged {
		lines = append(lines, "resources now "+describeResources(b.GetResources())+" (the carrier rolls to pick them up)")
	}
	if len(resp.GetStaticApplied()) == 0 && !resourcesChanged {
		lines = append(lines, "no changes (the request matches the current desired state)")
	}
	if s := b.GetSettings(); len(s) == 0 {
		lines = append(lines, "settings: none (engine defaults)")
	} else {
		lines = append(lines, "settings: "+joinKnobSettings(s))
	}
	if len(resp.GetStaticApplied()) > 0 || resourcesChanged {
		lines = append(lines, "watch the roll with 'fleetly buckets get' until status is running again")
	}
	_, _ = fmt.Fprintln(env.Stdout, strings.Join(lines, "\n"))
}

func newBucketsDeleteVerb() commands.Command {
	const name = "delete"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Delete a bucket store instance (tears down carriers; volume and credential secret are retained)",
		usage: "buckets delete [--project PROJECT] BUCKET_STORE_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare bucket store name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BUCKET_STORE_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolveBucketStoreID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.BucketStores.DeleteBucketStore(ctx, &structurev1.DeleteBucketStoreRequest{Id: id})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintf(env.Stdout,
						"deleted bucket store %s (carriers torn down; data volume and credential secret retained)\n", args[0])
				})
			})
		},
	}
}

func newBucketsRotatePasswordVerb() commands.Command {
	const name = "rotate-password"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Rotate the root secret key (declarative: the secret is rewritten and the instance rolls with the new key)",
		usage: "buckets rotate-password [--project PROJECT] BUCKET_STORE_ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (resolves a bare bucket store name)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one BUCKET_STORE_ID argument")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				id, err := resolveBucketStoreID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.BucketStores.RotateBucketStorePassword(ctx, &structurev1.RotateBucketStorePasswordRequest{BucketStoreId: id})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp.GetBucketStore(), func() {
					b := resp.GetBucketStore()
					_, _ = fmt.Fprintf(env.Stdout,
						"rotated credential for bucket store %s (secret %s rewritten; value never shown)\nthe carrier rolls to pick up the new key; apps referencing it keep the old value until redeployed\n",
						b.GetName(), b.GetCredentialsRef())
				})
			})
		},
	}
}
