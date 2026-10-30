package cmd

// apps 动词族（从 verbs_structure.go 分出——sizeguard 1200 行红线；B1 W3
// 批 apps delete 确认三形态 + apps scale 新动词入此）：restart 双面动词/
// 生效基线选源助手/delete 拆除三形态/scale 伸缩。全部 --json 双形态
//（golden 钉死）；旗标经闭包变量捕获（commands.Flagged 契约）。

import (
	"context"
	"flag"
	"fmt"
	"strconv"

	"github.com/lynx-go/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// newAppsRestartVerb 是重启双面动词（ADR-0083 §3 双面操作者契约；推翻
// ADR-0069 决策 6）：**缺省 = 声明面**——取**生效基线**（最新 succeeded
// 部署的 to_revision）为源，**不是** R 号最大的冻结 Revision（H15：失败/
// 取消/回滚后最新冻结体可能从未生效，以其为源会把失败 spec 重新上线，
// 且引擎对 succeeded 基线判定为非 seq-only → 迁移重跑）。全部进程
// restart_seq+1，经 Deploy(spec_file) 走正常部署管线（有 lineage、seq-only
// 判定免迁移、内容等价语义护航滚动替换）；`--runtime` 旗标走 ADR-0069
// 动作面逃生（单进程零 lineage 强制重排；进程存在性校验仍对最新冻结
// Spec——逃生面语义不动）。hook-owned 应用的所有权闸门错误
// （E_APP_HOOK_OWNED）如实透出，--runtime 即逃生；声明面对 hook-owned 应用
// 走 spec_file overlay（材料半边取冻结侧），天然合法。无 succeeded 基线
// （从未成功部署）诚实报错。
func newAppsRestartVerb() commands.Command {
	const name = "restart"
	var app, process string
	var runtimeFace bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Restart an app declaratively: bump restart_seq on the effective baseline revision and redeploy (lineage + no migration re-run). --runtime force-reschedules one process in place (escape hatch, no lineage)",
		usage:    "apps restart --app APP_ID [--runtime --process NAME]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.BoolVar(&runtimeFace, "runtime", false, "escape hatch: force-reschedule one process's carriers in place (ADR-0069; no revision or deployment, brief per-replica downtime)")
			fs.StringVar(&process, "process", "", "process name (required with --runtime; not valid without it)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" {
				return usageErr(name, "--app is required")
			}
			if runtimeFace {
				if process == "" {
					return usageErr(name, "--process is required with --runtime")
				}
				return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
					_, err := c.Deployments.RestartProcess(ctx, &deliveryv1.RestartProcessRequest{AppId: app, ProcessName: process})
					if err != nil {
						return err
					}
					return renderOut(env, jsonOut, &deliveryv1.RestartProcessResponse{}, func() {
						_, _ = fmt.Fprintf(env.Stdout, "restart requested for process %s of app %s (carriers force-rescheduled)\n", process, app)
					})
				})
			}
			if process != "" {
				return usageErr(name, "--process is only meaningful with --runtime (the declarative face restarts every process of the app)")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				latest, err := latestSucceededRevision(ctx, c, app)
				if err != nil {
					return err
				}
				got, err := c.Revisions.GetRevision(ctx, &deliveryv1.GetRevisionRequest{AppId: app, Seq: latest.GetSeq()})
				if err != nil {
					return err
				}
				spec := got.GetSpec()
				if len(spec.GetProcesses()) == 0 {
					return fmt.Errorf("frozen revision R%d of app %s declares no processes (nothing to restart)", latest.GetSeq(), app)
				}
				for _, p := range spec.GetProcesses() {
					if p.GetRestartSeq() == ^uint32(0) {
						return fmt.Errorf("process %s restart_seq is already at the maximum (4294967295); re-declare the app with a fresh spec instead", p.GetName())
					}
					p.RestartSeq = p.GetRestartSeq() + 1
				}
				// 冻结体 marshal 与服务端冻结咽喉同参（protojson
				// UseProtoNames + EmitUnpopulated:false）——seq 变更即内容
				// 变更（digest 覆盖 restart_seq），受理走同一咽喉。
				body, err := protoJSONMarshal.Marshal(spec)
				if err != nil {
					return err
				}
				// B1 定形：声明面重启是无人值守的组合动词（重启语义即确认，
				// 无预览交互面）——显式 skip 直发（--yes 语义；审计
				// proof=skipped 落 LedgerOnly）。
				resp, err := c.Deployments.Deploy(ctx, &deliveryv1.DeployRequest{
					AppId: app, SpecFile: string(body),
					Confirmation: &deliveryv1.Confirmation{Skip: true},
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					d := resp.GetDeployment()
					_, _ = fmt.Fprintf(env.Stdout,
						"deployment %s %s (revision %s, generation %d, queue position %d; declarative restart of R%d: restart_seq bumped on %d process(es))\n",
						d.GetId(), d.GetState(), d.GetToRevision(), d.GetGeneration(),
						resp.GetAdmission().GetPosition(), latest.GetSeq(), len(spec.GetProcesses()))
				})
			})
		},
	}
}

// latestSucceededRevision 取 App 的**生效基线** Revision（H15）：最新
// state=succeeded 部署行的 to_revision 所指的冻结体——语义源是"当前生效"
// 而非"R 号最大者"（rollback 不冻结新号；failed/cancelled 的冻结体不是
// 生效态，以其为重启源会重新上线失败 spec 并让引擎判定为非 seq-only）。
// 分页全部走客户端组合（部署新→旧、Revision R 号升序），零新 RPC。
func latestSucceededRevision(ctx context.Context, c *fleetly.Client, app string) (*deliveryv1.Revision, error) {
	deployments := make([]*deliveryv1.Deployment, 0, 50)
	afterDeployment := int64(0)
	for {
		resp, err := c.Deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{
			AppId: app, AfterDeploymentSeq: afterDeployment, Limit: 200,
		})
		if err != nil {
			return nil, err
		}
		page := resp.GetDeployments()
		deployments = append(deployments, page...)
		// 部署列表新→旧：扫到首个 succeeded 所在页即可停（同页更旧行仍由
		// 选择纯函数按 seq 取最大）。空页 = 已到尾。
		succeeded := false
		for _, d := range page {
			if d.GetState() == "succeeded" {
				succeeded = true
				break
			}
		}
		if len(page) == 0 || succeeded {
			break
		}
		afterDeployment = page[len(page)-1].GetSeq()
	}
	revisions := make([]*deliveryv1.Revision, 0, 50)
	afterRevision := int64(0)
	for {
		resp, err := c.Revisions.ListRevisions(ctx, &deliveryv1.ListRevisionsRequest{
			AppId: app, AfterSeq: afterRevision, Limit: 200,
		})
		if err != nil {
			return nil, err
		}
		page := resp.GetRevisions()
		revisions = append(revisions, page...)
		if len(page) == 0 {
			break
		}
		afterRevision = page[len(page)-1].GetSeq()
	}
	rev := selectRestartBaseline(deployments, revisions)
	if rev == nil {
		return nil, fmt.Errorf("app %s has no successful baseline to restart (deploy first)", app)
	}
	return rev, nil
}

// selectRestartBaseline 是声明面重启的选源纯函数（H15 可单测面）：取 seq
// 最大的 succeeded 部署行的 to_revision，再在 Revision 行集中按 id 命中该
// 冻结体。无 succeeded 部署（或行集缺对应 Revision——不该发生）返回 nil，
// 由调用方报"无成功基线"。输入序无关：部署行可乱序，纯函数按 seq 取最大。
func selectRestartBaseline(deployments []*deliveryv1.Deployment, revisions []*deliveryv1.Revision) *deliveryv1.Revision {
	var latest *deliveryv1.Deployment
	for _, d := range deployments {
		if d.GetState() != "succeeded" {
			continue
		}
		if latest == nil || d.GetSeq() > latest.GetSeq() {
			latest = d
		}
	}
	if latest == nil {
		return nil
	}
	for _, r := range revisions {
		if r.GetId() == latest.GetToRevision() {
			return r
		}
	}
	return nil
}

// apps delete（ADR-0023）：收口删除——拆载体 + 撤路由 + tombstone；活跃
// 部署在（E_CONFLICT）时拒绝（先 cancel 或等终态）。B1 W3 确认三形态：
// 交互默认 = PreviewTeardown（级联 + 保留清单 + 驱动权冲突）→ y/N；--yes；
// --preview/--ticket 两调用流。
func newAppsDeleteVerb() commands.Command {
	const name = "delete"
	var app string
	conf := confirmFlags{previewFace: true}
	return &flaggedVerb{
		name: name, synopsis: "Delete an app (tears down carriers and routes; refuses while deployments are active)",
		usage: "apps delete --app APP_ID [--yes | --preview | --ticket T]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			conf.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" {
				return usageErr(name, "--app is required")
			}
			plan, err := conf.resolve("apps delete")
			if err != nil {
				return err
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				var confirmation *deliveryv1.Confirmation
				// 确认前半：--preview 打印即退出；交互默认打印拆除预演后
				// y/N——y 以预览票带票直发，N 中止（零请求）。
				if plan.mode == confirmPreviewOnly || plan.mode == confirmInteractive {
					preview, perr := c.Deployments.PreviewTeardown(ctx, &deliveryv1.PreviewTeardownRequest{AppId: app})
					if perr != nil {
						return perr
					}
					if rerr := renderTeardownPreview(env, jsonOut, app, preview); rerr != nil {
						return rerr
					}
					if plan.mode == confirmPreviewOnly {
						return nil
					}
					confirmed, cerr := promptConfirm(env, "Delete app")
					if cerr != nil {
						return cerr
					}
					if !confirmed {
						_, _ = fmt.Fprintln(env.Stdout, "aborted (nothing was sent)")
						return nil
					}
					confirmation = &deliveryv1.Confirmation{Ticket: preview.GetTicket()}
				} else {
					confirmation = plan.confirmation()
					if plan.mode == confirmSkip && !jsonOut {
						_, _ = fmt.Fprintln(env.Stdout, confirmSkipNote)
					}
				}
				resp, err := c.Apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{
					Id:           app,
					Confirmation: confirmation,
				})
				if err != nil {
					return presentConfirmError(err)
				}
				return renderOut(env, jsonOut, resp, func() {
					_, _ = fmt.Fprintf(env.Stdout, "deleted app %s (carriers and routes torn down)\n", app)
				})
			})
		},
	}
}

// scaleWaitReport 是 apps scale --wait 的终态摘要（--json 形态；字段名
// 沿 protojson snake_case 惯例）。
type scaleWaitReport struct {
	AppID        string `json:"app_id"`
	Process      string `json:"process"`
	Replicas     int    `json:"replicas"`
	DeploymentID string `json:"deployment_id"`
	State        string `json:"state"`
}

// newAppsScaleVerb 是 ScaleProcess 的 CLI 皮（B1s，设计稿 §1.2 第二轮定稿
// + §1.6 真机案例动线闭环终点）：读当前生效 Spec 改目标进程 replicas 的
// scale-only overlay——ADR-0064 内容锚定等价保证零载体替换。豁免强制确认
// （Restart/Scale 轻且可逆族），无票面；--wait 对齐 deploy 动词（收流至
// 终态，非 succeeded 非零退出）。幂等键沿 deploy 动词惯例（--idempotency-key
// 头形态下发；受理去重由 admission 判定承载）。
func newAppsScaleVerb() commands.Command {
	const name = "scale"
	var wait bool
	var idem idemKeyFlag
	return &flaggedVerb{
		name:     name,
		synopsis: "Scale one process of an app to N replicas (scale-only overlay on the effective spec; zero carrier replacement)",
		usage:    "apps scale [--wait] [--idempotency-key K] APP_ID PROCESS REPLICAS",
		setFlags: func(fs *flag.FlagSet) {
			fs.BoolVar(&wait, "wait", false, "wait for the scale deployment to reach a terminal state (streams state transitions; non-zero exit unless succeeded)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 3 {
				return usageErr(name, "expected exactly APP_ID PROCESS REPLICAS arguments")
			}
			replicas, err := strconv.Atoi(args[2])
			if err != nil || replicas < 0 {
				return usageErr(name, "REPLICAS must be a non-negative integer")
			}
			// --wait 等待流是流式长面：豁免请求级 deadline（deploy --wait
			// 同款纪律）。
			var dialOpts []dialOption
			if wait {
				dialOpts = append(dialOpts, noDeadline())
			}
			return runRPC(ctx, dialOpts, func(ctx context.Context, c *fleetly.Client) error {
				// ScaleProcess 走 unary 上限（ctx 无 deadline 时显式附
				// 120s——H14 同款）；--wait 等待流用原始 ctx。
				unaryCtx, cancelUnary := unaryDeadlineContext(ctx)
				defer cancelUnary()
				unaryCtx = idem.bind(unaryCtx)
				resp, err := c.Deployments.ScaleProcess(unaryCtx, &deliveryv1.ScaleProcessRequest{
					AppId: args[0], Process: args[1], Replicas: int32(replicas), //nolint:gosec // 参数域内已校验
				})
				if err != nil {
					return err
				}
				if wait {
					// 等待锚 = 受理部署行：ScaleProcessResponse 只携带判定
					// 附注（无 deployment 字段），以 App 最新部署行（新→旧
					// 首行）附着——merged/deduplicated 语义下最新行即命中
					// 行（admission 判定与 Deploy 同面）。
					list, lerr := c.Deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: args[0], Limit: 1})
					if lerr != nil {
						return lerr
					}
					if len(list.GetDeployments()) == 0 {
						return fmt.Errorf("apps scale: no deployment row found for app %s to wait on", args[0])
					}
					depID := list.GetDeployments()[0].GetId()
					// 帧回调 nil + 终态单行摘要（deploy --wait 的逐帧行在
					// 并发驱动下帧数非确定——终态裁决同形，输出形态取确定
					// 性单行；golden 钉死）。
					final, werr := waitDeploymentFrames(ctx, c, depID, nil)
					if werr != nil {
						return werr
					}
					if rerr := requireSucceeded("deployment", depID, final); rerr != nil {
						return rerr
					}
					if jsonOut {
						return writeJSON(env.Stdout, scaleWaitReport{
							AppID: args[0], Process: args[1], Replicas: replicas,
							DeploymentID: depID, State: final,
						})
					}
					_, err := fmt.Fprintf(env.Stdout, "process %s of app %s scaled to %d replicas (deployment %s %s)\n",
						args[1], args[0], replicas, depID, final)
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					a := resp.GetAdmission()
					_, _ = fmt.Fprintf(env.Stdout, "process %s of app %s scaled to %d replicas (admission: %s at position %d)\n",
						args[1], args[0], replicas, a.GetOutcome(), a.GetPosition())
				})
			})
		},
	}
}
