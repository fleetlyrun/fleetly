package cmd

// Delivery 与运维动词：deploy/deployments/rollback/revisions/builds/
// routes/nodes（events/logs 随 C8 批接入）。等待原语（F1.3，架构 §7）：
// deploy/rollback 的 --wait 经 WaitDeployment 流收至终态，不自写轮询。

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/lynx-go/commands"
	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// waitDeploymentFrames 经等待原语收流至终态（F1.3）：逐帧回调，返回终态。
// 流式长等待——调用方须以 noDeadline 拨号（服务端流面不经 unary 超时）。
func waitDeploymentFrames(ctx context.Context, c *fleetly.Client, depID string, onFrame func(frame *deliveryv1.WaitDeploymentResponse) error) (string, error) {
	stream, err := c.Deployments.WaitDeployment(ctx, &deliveryv1.WaitDeploymentRequest{DeploymentId: depID})
	if err != nil {
		return "", err
	}
	final := ""
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			return final, nil
		}
		if err != nil {
			return final, err
		}
		final = frame.GetDeployment().GetState()
		if onFrame != nil {
			if err := onFrame(frame); err != nil {
				return final, err
			}
		}
	}
}

// waitOnFrame 渲染一帧等待输出：机器形态 jsonl（protojson 归一空白），
// 人类形态状态行。
func waitOnFrame(env *commands.Environment, jsonOut bool, depID string) func(*deliveryv1.WaitDeploymentResponse) error {
	return func(frame *deliveryv1.WaitDeploymentResponse) error {
		if jsonOut {
			data, err := protoJSONMarshal.Marshal(frame)
			if err != nil {
				return err
			}
			var buf bytes.Buffer
			if err := json.Compact(&buf, data); err != nil {
				return err
			}
			_, err = fmt.Fprintln(env.Stdout, buf.String())
			return err
		}
		d := frame.GetDeployment()
		_, err := fmt.Fprintf(env.Stdout, "deployment %s %s\n", depID, d.GetState())
		return err
	}
}

// requireSucceeded 是等待原语的终态裁决：非 succeeded 终态即错误（退出码
// 非零——机器契约）。kind 是名词（deployment/build），错误文案与信封对齐。
func requireSucceeded(kind, id, final string) error {
	if final != "succeeded" {
		return fmt.Errorf("%s %s ended in state %s", kind, id, final)
	}
	return nil
}

// waitBuildFrames 经 WaitBuild 收流至终态（F1.3）：逐帧回调，返回终态。
// 流式长等待——调用方须以 noDeadline 拨号（服务端流面不经 unary 超时）。
func waitBuildFrames(ctx context.Context, c *fleetly.Client, buildID string, onFrame func(frame *deliveryv1.WaitBuildResponse) error) (string, error) {
	stream, err := c.Builds.WaitBuild(ctx, &deliveryv1.WaitBuildRequest{BuildId: buildID})
	if err != nil {
		return "", err
	}
	final := ""
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			return final, nil
		}
		if err != nil {
			return final, err
		}
		final = frame.GetBuild().GetState()
		if onFrame != nil {
			if err := onFrame(frame); err != nil {
				return final, err
			}
		}
	}
}

// waitBuildOnFrame 渲染一帧构建等待输出（形态同 waitOnFrame）。
func waitBuildOnFrame(env *commands.Environment, jsonOut bool, buildID string) func(*deliveryv1.WaitBuildResponse) error {
	return func(frame *deliveryv1.WaitBuildResponse) error {
		if jsonOut {
			data, err := protoJSONMarshal.Marshal(frame)
			if err != nil {
				return err
			}
			var buf bytes.Buffer
			if err := json.Compact(&buf, data); err != nil {
				return err
			}
			_, err = fmt.Fprintln(env.Stdout, buf.String())
			return err
		}
		b := frame.GetBuild()
		_, err := fmt.Fprintf(env.Stdout, "build %s %s\n", buildID, b.GetState())
		return err
	}
}

func newDeployVerb() commands.Command {
	const name = "deploy"
	var app, image, composeFile, fromDir, dockerfile, builder, railpackVersion, outputDir, process, idemKey, commit, httpProbe, portProtocol, specFile string
	var tcpProbe, port int
	var supersede, wait bool
	var appEnv envSlice
	return &flaggedVerb{
		name: name, synopsis: "Deploy an app from an image, compose file, uploaded directory, or raw AppSpec file",
		usage: "deploy --app APP_ID (--image REF | --compose-file PATH | --from-dir DIR | --spec-file PATH) [--builder dockerfile|railpack|static] [--dockerfile PATH] [--railpack-version SEMVER] [--output-dir DIR] [--port N] [--protocol http|h2c|tcp] [--env KEY=VALUE]... [--idempotency-key K] [--supersede] [--wait] [--http-probe PATH | --tcp-probe PORT]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.StringVar(&image, "image", "", "image reference (direct image deploy)")
			fs.StringVar(&composeFile, "compose-file", "", "compose file path (controlled subset)")
			fs.StringVar(&fromDir, "from-dir", "", "local directory to tar and upload as build source (F1.10; .git is never uploaded)")
			fs.StringVar(&specFile, "spec-file", "", "path to a normalized AppSpec JSON file (protojson snake_case; full spec surface, excludes single-process flags)")
			fs.StringVar(&builder, "builder", "", "builder for --from-dir deploys: dockerfile (default), railpack (zero-config source builds, version-pinned) or static (serve an artifact directory)")
			fs.StringVar(&dockerfile, "dockerfile", "", "dockerfile path inside the uploaded source (default Dockerfile; dockerfile builder only)")
			fs.StringVar(&railpackVersion, "railpack-version", "", "pinned railpack version, bare semver like 0.39.0 (required with --builder railpack)")
			fs.StringVar(&outputDir, "output-dir", "", "artifact directory inside the uploaded source to serve (default .; static builder only)")
			fs.StringVar(&process, "process", "", "process name for image and upload deploys (default web)")
			fs.IntVar(&port, "port", 0, "container port the process listens on, for image and upload deploys (enables routes; attaches the project default network)")
			fs.StringVar(&portProtocol, "protocol", "", "port protocol: http (default), h2c or tcp (with --port)")
			fs.Var(&appEnv, "env", "app-level variable KEY=VALUE, repeatable (overrides project shared variables; image and upload deploys only)")
			fs.StringVar(&idemKey, "idempotency-key", "", "idempotency key: same key+body replays the same response for 24h (sent as the Idempotency-Key header and the deployment dedup anchor)")
			fs.StringVar(&commit, "commit", "", "commit sha (webhook dedup anchor)")
			fs.BoolVar(&supersede, "supersede", false, "explicitly preempt any in-flight deployment")
			fs.BoolVar(&wait, "wait", false, "wait for the deployment to reach a terminal state (streams state transitions; non-zero exit unless succeeded)")
			fs.StringVar(&httpProbe, "http-probe", "", "http health probe path for image and upload deploys (absolute path, e.g. /healthz)")
			fs.IntVar(&tcpProbe, "tcp-probe", 0, "tcp health probe port for image and upload deploys")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" {
				return usageErr(name, "--app is required")
			}
			sources := 0
			for _, v := range []string{image, composeFile, fromDir, specFile} {
				if v != "" {
					sources++
				}
			}
			if sources != 1 {
				return usageErr(name, "exactly one of --image, --compose-file, --from-dir or --spec-file is required")
			}
			// spec_file 自带全部声明面：单进程形态旗标互斥（服务端同款执法，
			// CLI 先拒省一轮往返）。
			if specFile != "" && (process != "" || httpProbe != "" || tcpProbe != 0 || port != 0 ||
				portProtocol != "" || len(appEnv) > 0 || builder != "" || dockerfile != "" ||
				railpackVersion != "" || outputDir != "") {
				return usageErr(name, "process/probe/port/protocol/env/builder flags are for image or upload deploys; --spec-file carries the full process declaration")
			}
			if httpProbe != "" && tcpProbe != 0 {
				return usageErr(name, "--http-probe and --tcp-probe are mutually exclusive")
			}
			if (httpProbe != "" || tcpProbe != 0) && composeFile != "" {
				return usageErr(name, "--http-probe/--tcp-probe are for image or upload deploys; compose declares probes via healthcheck.http_path/tcp_port")
			}
			if len(appEnv) > 0 && composeFile != "" {
				return usageErr(name, "--env is for image or upload deploys; compose declares variables via each service's environment")
			}
			if (port != 0 || portProtocol != "") && composeFile != "" {
				return usageErr(name, "--port/--protocol are for image or upload deploys; compose declares ports per service (\"8080\" or \"8080/h2c\")")
			}
			if port == 0 && portProtocol != "" {
				return usageErr(name, "--protocol is only meaningful together with --port")
			}
			switch portProtocol {
			case "", "http", "h2c", "tcp":
			default:
				return usageErr(name, fmt.Sprintf("unknown --protocol %q (supported: http, h2c, tcp)", portProtocol))
			}
			if fromDir == "" && (builder != "" || dockerfile != "" || railpackVersion != "" || outputDir != "") {
				return usageErr(name, "--builder/--dockerfile/--railpack-version/--output-dir are for --from-dir deploys only")
			}
			switch builder {
			case "":
			case "dockerfile":
				if railpackVersion != "" || outputDir != "" {
					return usageErr(name, "--railpack-version/--output-dir are for the railpack/static builders")
				}
			case "railpack":
				if railpackVersion == "" {
					return usageErr(name, "--railpack-version is required with --builder railpack (a bare semver; a mismatched pin fails the build with the platform's version)")
				}
				if dockerfile != "" {
					return usageErr(name, "--dockerfile is for the dockerfile builder; railpack detects the source")
				}
			case "static":
				if railpackVersion != "" {
					return usageErr(name, "--railpack-version is for the railpack builder")
				}
				if dockerfile != "" {
					return usageErr(name, "--dockerfile is for the dockerfile builder; static serves an artifact directory")
				}
			default:
				return usageErr(name, fmt.Sprintf("unknown --builder %q (supported: dockerfile, railpack, static)", builder))
			}
			compose := ""
			if composeFile != "" {
				data, err := os.ReadFile(composeFile) //nolint:gosec // 用户显式指定的输入路径
				if err != nil {
					return fmt.Errorf("read compose file: %w", err)
				}
				compose = string(data)
			}
			specBody := ""
			if specFile != "" {
				data, err := os.ReadFile(specFile) //nolint:gosec // 用户显式指定的输入路径
				if err != nil {
					return fmt.Errorf("read spec file: %w", err)
				}
				specBody = string(data)
			}
			// --wait 与 --from-dir 的上传流都是流式长面：豁免请求级 deadline。
			var dialOpts []dialOption
			if wait || fromDir != "" {
				dialOpts = append(dialOpts, noDeadline())
			}
			ctx, cancel, c, err := dialFromEnv(ctx, dialOpts...)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			var uploadID string
			if fromDir != "" {
				info, serr := os.Stat(fromDir) //nolint:gosec // 用户显式指定的输入路径
				if serr != nil {
					return fmt.Errorf("stat %s: %w", fromDir, serr)
				}
				if !info.IsDir() {
					return usageErr(name, "--from-dir must be a directory")
				}
				projectID, perr := appProjectID(ctx, c, app)
				if perr != nil {
					return perr
				}
				up, uerr := uploadDir(ctx, c, projectID, fromDir)
				if uerr != nil {
					return uerr
				}
				uploadID = up.GetId()
			}
			// 头（通用幂等）与 body 字段（部署去重锚）同值下发——ADR-0024
			// 双源一致性：不一致即 409。--from-dir 形态上传先行完成，Deploy
			// 仍按普通 unary 走（ctx 无 deadline 时显式附 120s 上限）。
			if _, ok := ctx.Deadline(); !ok {
				var dctx context.Context
				dctx, cancel2 := context.WithTimeout(ctx, cliRequestTimeout)
				defer cancel2()
				ctx = dctx
			}
			ctx = fleetly.WithIdempotencyKey(ctx, idemKey)
			resp, err := c.Deployments.Deploy(ctx, &deliveryv1.DeployRequest{
				AppId: app, Image: image, ComposeYaml: compose, UploadId: uploadID, Dockerfile: dockerfile,
				Builder: builder, RailpackVersion: railpackVersion, OutputDir: outputDir,
				ProcessName:    process,
				Port:           int32(port), //nolint:gosec // 端口域内
				Protocol:       portProtocol,
				SpecFile:       specBody,
				Env:            appEnv.envMap(),
				IdempotencyKey: idemKey, CommitSha: commit, Supersede: supersede,
				HttpProbe: httpProbe, TcpProbe: int32(tcpProbe), //nolint:gosec // 端口域内
			})
			if err != nil {
				return err
			}
			depID := resp.GetDeployment().GetId()
			if wait {
				final, werr := waitDeploymentFrames(ctx, c, depID, waitOnFrame(env, jsonOut, depID))
				if werr != nil {
					return werr
				}
				return requireSucceeded("deployment", depID, final)
			}
			// 响应直渲染（P10）：admission 判定附注与 deployment 同体输出
			//（uploads put 同款先例——响应携带实体外字段时渲染响应本体）。
			return renderOut(env, jsonOut, resp, func() {
				d := resp.GetDeployment()
				a := resp.GetAdmission()
				// 人类形态打印判定："并入队列第 N 位/与既有部署去重"
				//（P10 提案原文锚）；merged/superseded 追加归因后缀。
				switch a.GetOutcome() {
				case "deduplicated":
					_, _ = fmt.Fprintf(env.Stdout,
						"deployment %s %s (revision %s, generation %d; deduplicated: matched the existing deployment)\n",
						d.GetId(), d.GetState(), d.GetToRevision(), d.GetGeneration())
				case "merged":
					_, _ = fmt.Fprintf(env.Stdout,
						"deployment %s %s (revision %s, generation %d, queue position %d; merged earlier queued requests)\n",
						d.GetId(), d.GetState(), d.GetToRevision(), d.GetGeneration(), a.GetPosition())
				case "superseded":
					_, _ = fmt.Fprintf(env.Stdout,
						"deployment %s %s (revision %s, generation %d, queue position %d; superseded the in-flight deployment)\n",
						d.GetId(), d.GetState(), d.GetToRevision(), d.GetGeneration(), a.GetPosition())
				default:
					_, _ = fmt.Fprintf(env.Stdout,
						"deployment %s %s (revision %s, generation %d, queue position %d)\n",
						d.GetId(), d.GetState(), d.GetToRevision(), d.GetGeneration(), a.GetPosition())
				}
			})
		},
	}
}

// newDeploymentsWaitVerb 构造 deployments wait（standalone 等待面，F1.15
// torchwood 迁移场景）：webhook 等外部触发链提交的部署无法经 deploy --wait
// 附带等待——standalone 动词附着到在途部署收流至终态。终态裁决同 --wait
// （非 succeeded 非零退出）。
func newDeploymentsWaitVerb() commands.Command {
	const name = "wait"
	var depID string
	return &flaggedVerb{
		name:     name,
		synopsis: "Wait for a deployment to reach a terminal state (streams state transitions; non-zero exit unless succeeded)",
		usage:    "deployments wait --deployment DEPLOYMENT_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&depID, "deployment", "", "deployment id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if depID == "" {
				return usageErr(name, "--deployment is required")
			}
			// 流式长等待：拨号豁免请求级 deadline（deploy --wait 同款纪律）。
			ctx, cancel, c, err := dialFromEnv(ctx, noDeadline())
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			final, werr := waitDeploymentFrames(ctx, c, depID, waitOnFrame(env, jsonOut, depID))
			if werr != nil {
				return werr
			}
			return requireSucceeded("deployment", depID, final)
		},
	}
}

// newBuildsWaitVerb 构造 builds wait（WaitBuild 的 CLI 面）：构建行由
// git/upload 触发链异步铸出，Agent 需附着等待其终态（F1.3 原语收口面，
// 不自写轮询）。终态裁决同 deployments wait。
func newBuildsWaitVerb() commands.Command {
	const name = "wait"
	var buildID string
	return &flaggedVerb{
		name:     name,
		synopsis: "Wait for a build to reach a terminal state (streams state transitions; non-zero exit unless succeeded)",
		usage:    "builds wait --build BUILD_ID",
		setFlags: func(fs *flag.FlagSet) { fs.StringVar(&buildID, "build", "", "build id (required)") },
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if buildID == "" {
				return usageErr(name, "--build is required")
			}
			// 流式长等待：拨号豁免请求级 deadline（wait 家族同款纪律）。
			ctx, cancel, c, err := dialFromEnv(ctx, noDeadline())
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			final, werr := waitBuildFrames(ctx, c, buildID, waitBuildOnFrame(env, jsonOut, buildID))
			if werr != nil {
				return werr
			}
			return requireSucceeded("build", buildID, final)
		},
	}
}

// newDeploymentsCancelVerb 构造 deployments cancel（ADR-0016 CLI 补面）：
// CancelDeployment 的 API/REST 面在册而 CLI 缺席，Agent 语义断裂。幂等
// 语义照 API——排队/在途可取消（终态拒：E_NOT_CANCELLABLE 信封，随机
// error_id 不可 golden，错误面断言在 apitest）。
func newDeploymentsCancelVerb() commands.Command {
	const name = "cancel"
	return &flaggedVerb{
		name:     name,
		synopsis: "Cancel a queued or in-flight deployment (terminal deployments refuse)",
		usage:    "deployments cancel DEPLOYMENT_ID",
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DEPLOYMENT_ID argument")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Deployments.CancelDeployment(ctx, &deliveryv1.CancelDeploymentRequest{Id: args[0]})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetDeployment(), func() {
				d := resp.GetDeployment()
				_, _ = fmt.Fprintf(env.Stdout, "deployment %s cancelled (revision %s, generation %d)\n", d.GetId(), d.GetToRevision(), d.GetGeneration())
			})
		},
	}
}

func newDeploymentsListVerb() commands.Command {
	const name = "list"
	var app, after string
	var limit int
	return &flaggedVerb{
		name:     name,
		synopsis: "List deployments for an app (newest first)",
		usage:    "deployments list --app APP_ID [--after DEPLOYMENT_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.StringVar(&after, "after", "", "pagination cursor: the last deployment id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" {
				return usageErr(name, "--app is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Deployments.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{
				AppId: app, AfterDeploymentId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tSTATE\tREVISION\tGENERATION\tUPDATED")
				for _, d := range resp.GetDeployments() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%d\t%s\n", d.GetId(), d.GetState(), d.GetToRevision(), d.GetGeneration(), d.GetUpdatedAt())
				}
			})
		},
	}
}

func newRollbackVerb() commands.Command {
	const name = "rollback"
	var app, to string
	var wait bool
	var idem idemKeyFlag
	return &flaggedVerb{
		name: name, synopsis: "Roll back an app by replaying a revision (first-class verb)",
		usage: "rollback --app APP_ID [--to REVISION_ID] [--wait] (default target: last successful baseline)",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.StringVar(&to, "to", "", "target revision id (default: last succeeded baseline)")
			fs.BoolVar(&wait, "wait", false, "wait for the replay deployment to reach a terminal state (non-zero exit unless succeeded)")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" {
				return usageErr(name, "--app is required")
			}
			var dialOpts []dialOption
			if wait {
				dialOpts = append(dialOpts, noDeadline())
			}
			ctx, cancel, c, err := dialFromEnv(ctx, dialOpts...)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Deployments.Rollback(ctx, &deliveryv1.RollbackRequest{AppId: app, ToRevision: to})
			if err != nil {
				return err
			}
			depID := resp.GetDeployment().GetId()
			if wait {
				final, werr := waitDeploymentFrames(ctx, c, depID, waitOnFrame(env, jsonOut, depID))
				if werr != nil {
					return werr
				}
				return requireSucceeded("deployment", depID, final)
			}
			return renderOut(env, jsonOut, resp, func() {
				d := resp.GetDeployment()
				_, _ = fmt.Fprintf(env.Stdout, "rolling back via deployment %s (target %s)\n", d.GetId(), d.GetToRevision())
			})
		},
	}
}

func newRevisionsListVerb() commands.Command {
	const name = "list"
	var app string
	var after int64
	var limit int
	return &flaggedVerb{
		name:     name,
		synopsis: "List frozen revisions for an app (R1..Rn order)",
		usage:    "revisions list --app APP_ID [--after SEQ] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.Int64Var(&after, "after", 0, "pagination cursor: the seq of the last revision of the previous page (0 = first page)")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" {
				return usageErr(name, "--app is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Revisions.ListRevisions(ctx, &deliveryv1.ListRevisionsRequest{
				AppId: app, AfterSeq: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "SEQ\tID\tDIGEST\tCREATED")
				for _, r := range resp.GetRevisions() {
					_, _ = fmt.Fprintf(env.Stdout, "R%d\t%s\t%s\t%s\n", r.GetSeq(), r.GetId(), r.GetDigest(), r.GetCreatedAt())
				}
			})
		},
	}
}

func newRevisionsDiffVerb() commands.Command {
	const name = "diff"
	var app string
	var from, to int64
	return &flaggedVerb{
		name: name, synopsis: "Field-level diff between two revisions",
		usage: "revisions diff --app APP_ID --from SEQ --to SEQ",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.Int64Var(&from, "from", 0, "from revision seq (required)")
			fs.Int64Var(&to, "to", 0, "to revision seq (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" || from == 0 || to == 0 {
				return usageErr(name, "--app, --from and --to are required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Revisions.DiffRevisions(ctx, &deliveryv1.DiffRevisionsRequest{AppId: app, FromSeq: from, ToSeq: to})
			if err != nil {
				return err
			}
			if err := renderOut(env, jsonOut, resp, func() {
				if len(resp.GetEntries()) == 0 {
					_, _ = fmt.Fprintln(env.Stdout, "no differences")
					return
				}
				_, _ = fmt.Fprintln(env.Stdout, "PATH\tOLD\tNEW")
				for _, e := range resp.GetEntries() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\n", e.GetPath(), e.GetOldValue(), e.GetNewValue())
				}
			}); err != nil {
				return err
			}
			// 有变化 → 退出码 2（diff 类动词机器契约；stdout 照常是 diff 面）。
			if len(resp.GetEntries()) > 0 {
				return errChanges
			}
			return nil
		},
	}
}

func newBuildsListVerb() commands.Command {
	const name = "list"
	var app, after string
	var limit int
	return &flaggedVerb{
		name:     name,
		synopsis: "List builds for an app (newest first)",
		usage:    "builds list --app APP_ID [--after BUILD_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.StringVar(&after, "after", "", "pagination cursor: the last build id of the previous page")
			fs.IntVar(&limit, "limit", 50, "page size (max 200)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if app == "" {
				return usageErr(name, "--app is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := c.Builds.ListBuilds(ctx, &deliveryv1.ListBuildsRequest{
				AppId: app, AfterBuildId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tSTATE\tDIGEST\tCREATED")
				for _, b := range resp.GetBuilds() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\n", b.GetId(), b.GetState(), b.GetDigest(), b.GetCreatedAt())
				}
			})
		},
	}
}

// builds logs（B4）：读构建日志（最近缓冲 + follow 续流至终态；持久化
// 检索 N2——诚实边界同 F0.25）。
func newBuildsLogsVerb() commands.Command {
	const name = "logs"
	var build string
	var follow bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Stream build logs (recent buffer; --follow keeps streaming until the build finishes)",
		usage:    "builds logs --build BUILD_ID [--follow]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&build, "build", "", "build id (required)")
			fs.BoolVar(&follow, "follow", false, "keep streaming new output")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if build == "" {
				return usageErr(name, "--build is required")
			}
			// 流式动词：拨号豁免请求级 deadline（follow 续流至构建终态，
			// 时长由构建决定；服务端流面不经 unary 超时拦截器）。
			ctx, cancel, c, err := dialFromEnv(ctx, noDeadline())
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			stream, err := c.Builds.StreamBuildLogs(ctx, &deliveryv1.StreamBuildLogsRequest{
				BuildId: build, Follow: follow,
			})
			if err != nil {
				return err
			}
			compact := func(m proto.Message) (string, error) {
				data, err := protoJSONMarshal.Marshal(m)
				if err != nil {
					return "", err
				}
				var buf bytes.Buffer
				if err := json.Compact(&buf, data); err != nil {
					return "", err
				}
				return buf.String(), nil
			}
			for {
				frame, err := stream.Recv()
				if err == io.EOF {
					return nil
				}
				if err != nil {
					return err
				}
				if jsonOut {
					line, err := compact(frame)
					if err != nil {
						return err
					}
					_, _ = fmt.Fprintln(env.Stdout, line)
					continue
				}
				_, _ = fmt.Fprintf(env.Stdout, "%s %s\n", frame.GetTime(), string(frame.GetLine()))
			}
		},
	}
}

func newRoutesCreateVerb() commands.Command {
	const name = "create"
	var project, host, path, app, process, protocol, tlsMode string
	var port int
	var idem idemKeyFlag
	return &flaggedVerb{
		name: name, synopsis: "Create a route (host/path -> app process port)",
		usage: "routes create --project P --host H --app A --process NAME --port N [--protocol http|h2c|tcp] [--tls auto|none]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id (required)")
			fs.StringVar(&host, "host", "", "hostname (required)")
			fs.StringVar(&path, "path", "", "path prefix (default /)")
			fs.StringVar(&app, "app", "", "app id (required)")
			fs.StringVar(&process, "process", "", "process name (required)")
			fs.IntVar(&port, "port", 0, "target port (required)")
			fs.StringVar(&protocol, "protocol", "http", "backend protocol: http | h2c | tcp")
			fs.StringVar(&tlsMode, "tls", "auto", "tls mode: auto | none")
			idem.declare(fs)
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if project == "" || host == "" || app == "" || process == "" || port == 0 {
				return usageErr(name, "--project, --host, --app, --process and --port are required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			ctx = idem.bind(ctx)
			resp, err := c.Routes.CreateRoute(ctx, &proxyv1.CreateRouteRequest{
				ProjectId: project, Host: host, Path: path, AppId: app, Process: process,
				Port:     int32(port), //nolint:gosec // 端口域内
				Protocol: protocol, TlsMode: tlsMode,
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp.GetRoute(), func() {
				r := resp.GetRoute()
				_, _ = fmt.Fprintf(env.Stdout, "created route %s -> %s:%d (%s)\n", r.GetHost(), r.GetProcess(), r.GetPort(), r.GetProtocol())
				if r.GetTlsMode() == "none" {
					_, _ = fmt.Fprintf(env.Stdout, "warning: TLS is disabled for %s - traffic to this route is plaintext until you recreate it with --tls auto\n", r.GetHost())
				}
			})
		},
	}
}

func newRoutesListVerb() commands.Command {
	const name = "list"
	var project, after string
	var limit int
	return &flaggedVerb{
		name:     name,
		synopsis: "List routes",
		usage:    "routes list [--project PROJECT_ID] [--after ROUTE_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "filter by project")
			fs.StringVar(&after, "after", "", "pagination cursor: the last route id of the previous page")
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
			resp, err := c.Routes.ListRoutes(ctx, &proxyv1.ListRoutesRequest{
				ProjectId: project, AfterRouteId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "ID\tHOST\tPATH\tAPP\tPROCESS\tPORT\tPROTO\tTLS")
				for _, r := range resp.GetRoutes() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
						r.GetId(), r.GetHost(), r.GetPath(), r.GetAppId(), r.GetProcess(), r.GetPort(), r.GetProtocol(), r.GetTlsMode())
				}
			})
		},
	}
}

func newNodesListVerb() commands.Command {
	const name = "list"
	var after string
	var limit int
	return &flaggedVerb{
		name:     name,
		synopsis: "List observed cluster nodes",
		usage:    "nodes list [--after NODE_ID] [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&after, "after", "", "pagination cursor: the last platform node id of the previous page")
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
			resp, err := c.Nodes.ListNodes(ctx, &runtimev1.ListNodesRequest{
				AfterNodeId: after, Limit: int32(limit), //nolint:gosec // 旗标域内钳制
			})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, "PLATFORM_ID\tROLE\tAVAILABLE\tHOSTNAME\tLAST_SEEN")
				for _, n := range resp.GetNodes() {
					_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%t\t%s\t%s\n", n.GetPlatformId(), n.GetRole(), n.GetAvailable(), n.GetHostname(), n.GetLastSeenAt())
				}
			})
		},
	}
}

func newNodesEnrollVerb() commands.Command {
	const name = "enroll"
	var rotate bool
	return &flaggedVerb{
		name: name, synopsis: "Print the worker join command (zero platform install on workers)",
		usage: "nodes enroll [--rotate]",
		setFlags: func(fs *flag.FlagSet) {
			fs.BoolVar(&rotate, "rotate", false, "invalidate all existing join tokens first (leak response)")
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
			resp, err := c.Nodes.EnrollNode(ctx, &runtimev1.EnrollNodeRequest{Rotate: rotate})
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintln(env.Stdout, resp.GetJoinCommand())
			})
		},
	}
}

// newNodesAdminVerb 构造节点运维动词（drain/cordon/uncordon；平台节点 ID
// 为锚，幂等可重放——结果即动作本身，无数据面返回）。
func newNodesAdminVerb(name, past, synopsis string, call func(ctx context.Context, c *fleetly.Client, nodeID string) (proto.Message, error)) commands.Command {
	var nodeID string
	return &flaggedVerb{
		name: name, synopsis: synopsis,
		usage: "nodes " + name + " --node ID",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&nodeID, "node", "", "platform node id (required)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if nodeID == "" {
				return usageErr(name, "--node is required")
			}
			ctx, cancel, c, err := dialFromEnv(ctx)
			if err != nil {
				return err
			}
			defer cancel()
			defer c.Close() //nolint:errcheck // 进程退出路径
			resp, err := call(ctx, c, nodeID)
			if err != nil {
				return err
			}
			return renderOut(env, jsonOut, resp, func() {
				_, _ = fmt.Fprintf(env.Stdout, "%s node %s\n", past, nodeID)
			})
		},
	}
}

func newNodesDrainVerb() commands.Command {
	return newNodesAdminVerb("drain", "drained", "Set a node to drain (workloads reschedule off it)",
		func(ctx context.Context, c *fleetly.Client, nodeID string) (proto.Message, error) {
			return c.Nodes.DrainNode(ctx, &runtimev1.DrainNodeRequest{NodeId: nodeID})
		})
}

func newNodesCordonVerb() commands.Command {
	return newNodesAdminVerb("cordon", "cordoned", "Cordon a node (no new placements, existing workloads stay)",
		func(ctx context.Context, c *fleetly.Client, nodeID string) (proto.Message, error) {
			return c.Nodes.CordonNode(ctx, &runtimev1.CordonNodeRequest{NodeId: nodeID})
		})
}

func newNodesUncordonVerb() commands.Command {
	return newNodesAdminVerb("uncordon", "uncordoned", "Uncordon a node (resume placements)",
		func(ctx context.Context, c *fleetly.Client, nodeID string) (proto.Message, error) {
			return c.Nodes.UncordonNode(ctx, &runtimev1.UncordonNodeRequest{NodeId: nodeID})
		})
}
