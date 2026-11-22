package cmd

// Functions 命令组（ADR-0077/0102 任务⑤；设计 v3 §11）：create 即首部署、
// 三平面独立动词（scale/exposure 行写零重部署）、rollback/revisions、logs
// （function_id 轴）、url、invoke（客户端直发——控制面不是数据面）。
// --json 双形态 golden 钉死；旗标前置于位置参数（仓律）。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/lynxland/commands"
	"google.golang.org/protobuf/proto"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// resolveFunctionID 把函数引用（id | project/name 复合 | name+--project）
// 解析为 id（名解析经 ListFunctions 匹配——Resolve RPC 未立，ADR-0092 名
// 寻址在边界解析的 CLI 面）。
func resolveFunctionID(ctx context.Context, c *fleetly.Client, projectFlag, ref string) (string, error) {
	if mintedIDRe.MatchString(ref) {
		return ref, nil
	}
	if before, name, found := strings.Cut(ref, "/"); found {
		projectID, err := resolveProjectID(ctx, c, before)
		if err != nil {
			return "", err
		}
		return resolveFunctionByName(ctx, c, projectID, name)
	}
	if projectFlag == "" {
		return "", fmt.Errorf("function name %q requires --project or the project/name form", ref)
	}
	projectID, err := resolveProjectID(ctx, c, projectFlag)
	if err != nil {
		return "", err
	}
	return resolveFunctionByName(ctx, c, projectID, ref)
}

func resolveFunctionByName(ctx context.Context, c *fleetly.Client, projectID, name string) (string, error) {
	resp, err := c.Functions.ListFunctions(ctx, &deliveryv1.ListFunctionsRequest{ProjectId: projectID})
	if err != nil {
		return "", err
	}
	for _, f := range resp.GetFunctions() {
		if f.GetName() == name {
			return f.GetId(), nil
		}
	}
	return "", fmt.Errorf("function %q not found in project", name)
}

// parseEnvFlags 把 --env K=V 面折为请求 map。
func parseEnvFlags(flags multiFlag) (map[string]string, error) {
	if len(flags) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(flags))
	for _, kv := range flags {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("--env expects KEY=VALUE (got %q)", kv)
		}
		out[key] = value
	}
	return out, nil
}

// functionResources 折资源旗标（两轴 0 = 不限）。
func functionResources(cpuMillis, memoryMb int) *specv1.ResourcesSpec {
	if cpuMillis == 0 && memoryMb == 0 {
		return nil
	}
	return &specv1.ResourcesSpec{CpuMillis: int64(cpuMillis), MemoryMb: int64(memoryMb)}
}

// waitFunction 收敛等待（事件信号 + 行重读真源；稳定态帧后收流）——
// --wait 旗标的单源实现（create/deploy 共用）。
func waitFunction(env *commands.Environment, jsonOut bool, fnID string) error {
	// 流式动词：独立拨号豁免请求级 deadline（logs/follow 同律——收敛可能
	// 慢于 120s 非流式预算，客户端 deadline 只会腰斩等待流）。
	return runRPC(context.Background(), []dialOption{noDeadline()}, func(ctx context.Context, c *fleetly.Client) error {
		stream, err := c.Functions.WaitFunction(ctx, &deliveryv1.WaitFunctionRequest{FunctionId: fnID})
		if err != nil {
			return err
		}
		var last *deliveryv1.Function
		for {
			frame, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
			last = frame.GetFunction()
		}
		if last == nil {
			return nil
		}
		return renderOut(env, jsonOut, last, func() {
			_, _ = fmt.Fprintf(env.Stdout, "function %s is %s (revision r%d)\n",
				last.GetName(), last.GetStatus(), last.GetCurrentRevisionSeq())
		})
	})
}

// ---- 动词 ----

func newFunctionsCreateVerb() commands.Command {
	const name = "create"
	var project, image, command, exposure, host string
	var envs, secrets multiFlag
	var port, cpuMillis, memoryMb, minReplicas, maxReplicas, idleTimeout int
	var idem idemKeyFlag
	var wait bool
	return &flaggedVerb{
		name: name, synopsis: "Create a function (first deploy included; default exposure is private)",
		usage: "functions create --project PROJECT_ID --image REF NAME [--command c1,c2] [--port N] [--env KEY=VALUE]... " +
			"[--secret NAME]... [--cpu-millis N] [--memory-mb N] [--min-replicas N] [--max-replicas N] " +
			"[--idle-timeout SECONDS] [--exposure public --host HOST] [--idempotency-key K] [--wait]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "owning project id or name (required)")
			fs.StringVar(&image, "image", "", "container image reference (required)")
			fs.StringVar(&command, "command", "", "entrypoint override (comma-separated)")
			fs.IntVar(&port, "port", 8080, "handler listen port")
			fs.Var(&envs, "env", "plain env KEY=VALUE (repeatable)")
			fs.Var(&secrets, "secret", "project secret name mounted at /run/secrets/<name> (repeatable)")
			fs.IntVar(&cpuMillis, "cpu-millis", 0, "per-replica CPU limit in millis (0 = unlimited)")
			fs.IntVar(&memoryMb, "memory-mb", 0, "per-replica memory limit in MiB (0 = unlimited)")
			fs.IntVar(&minReplicas, "min-replicas", 1, "minimum replicas (v1 floor: 1)")
			fs.IntVar(&maxReplicas, "max-replicas", 1, "maximum replicas")
			fs.IntVar(&idleTimeout, "idle-timeout", 0, "scale-to-zero idle window in seconds (0 = platform default 300; requires --min-replicas 0)")
			fs.StringVar(&exposure, "exposure", "private", "private | public")
			fs.StringVar(&host, "host", "", "public host (required for public; default derives sslip.io from the connection address)")
			idem.declare(fs)
			fs.BoolVar(&wait, "wait", false, "stream until the function reaches a stable status")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "exactly one NAME argument is required")
			}
			if image == "" {
				return usageErr(name, "--image is required")
			}
			envMap, err := parseEnvFlags(envs)
			if err != nil {
				return usageErr(name, err.Error())
			}
			publicHost := host
			if exposure == "public" && publicHost == "" && connectionIsIP() {
				// sslip 缺省（quickstart 先例同锚：连接地址的 IP 面）。
				// 域名连接不派生——交给服务端 functions.base_domain 缺省层
				//（P2-4：域名部署有真 DNS 面，托管域归服务端拼装）。
				publicHost = sslipHost(envOr(envAddr, defaultAddr), args[0])
			}
			var waitID string
			if err := runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				projectID, err := resolveProjectID(ctx, c, project)
				if err != nil {
					return err
				}
				ctx = idem.bind(ctx)
				resp, err := c.Functions.CreateFunction(ctx, &deliveryv1.CreateFunctionRequest{
					ProjectId: projectID, Name: args[0], Image: image,
					Command: splitCommandFlag(command), Env: envMap, SecretRefs: secrets,
					Port: clampInt32(port), MinReplicas: int64(minReplicas), MaxReplicas: int64(maxReplicas),
					IdleTimeoutSeconds: int64(idleTimeout),
					Exposure:           exposure, Host: publicHost,
					Resources: functionResources(cpuMillis, memoryMb),
				})
				if err != nil {
					return err
				}
				fn := resp.GetFunction()
				if err := renderOut(env, jsonOut, fn, func() {
					_, _ = fmt.Fprintf(env.Stdout,
						"created function %s (id %s)\nrevision r%d, in-network at %s\nexposure %s\n",
						fn.GetName(), fn.GetId(), fn.GetCurrentRevisionSeq(), fn.GetPrivateUrl(), fn.GetExposure())
					if fn.GetPublicUrl() != "" {
						_, _ = fmt.Fprintf(env.Stdout, "public at %s\n", fn.GetPublicUrl())
					}
				}); err != nil {
					return err
				}
				if wait {
					waitID = fn.GetId()
				}
				return nil
			}); err != nil {
				return err
			}
			if wait {
				return waitFunction(env, jsonOut, waitID)
			}
			return nil
		},
	}
}

// clampInt32 把旗标 int 折为 proto int32（gosec G115 显式收窄——端口域
// 1..65535 由服务端 CEL 执法，这里只做形态转换的溢出钳制）。
func clampInt32(v int) int32 {
	if v > 2147483647 {
		return 2147483647
	}
	if v < -2147483648 {
		return -2147483648
	}
	return int32(v)
}

// splitCommandFlag 拆逗号 command 旗标。
func splitCommandFlag(command string) []string {
	if command == "" {
		return nil
	}
	parts := strings.Split(command, ",")
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newFunctionsListVerb() commands.Command {
	const name = "list"
	var project, exposure string
	return &flaggedVerb{
		name: name, synopsis: "List functions in a project",
		usage: "functions list --project PROJECT_ID [--exposure private|public]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "owning project id or name (required)")
			fs.StringVar(&exposure, "exposure", "", "filter by exposure (empty = all)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				projectID, err := resolveProjectID(ctx, c, project)
				if err != nil {
					return err
				}
				resp, err := c.Functions.ListFunctions(ctx, &deliveryv1.ListFunctionsRequest{
					ProjectId: projectID, Exposure: exposure,
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					if len(resp.GetFunctions()) == 0 {
						_, _ = fmt.Fprintln(env.Stdout, "no functions")
						return
					}
					_, _ = fmt.Fprintln(env.Stdout, "NAME\tSTATUS\tEXPOSURE\tREPLICAS\tREVISION")
					for _, f := range resp.GetFunctions() {
						_, _ = fmt.Fprintf(env.Stdout, "%s\t%s\t%s\t%d\tr%d\n",
							f.GetName(), f.GetStatus(), f.GetExposure(), f.GetReplicas(), f.GetCurrentRevisionSeq())
					}
				})
			})
		},
	}
}

func newFunctionsGetVerb() commands.Command {
	const name = "get"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Show one function",
		usage: "functions get [--project PROJECT_ID] FUNCTION",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project for name addressing")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "exactly one FUNCTION argument is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				fnID, err := resolveFunctionID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Functions.GetFunction(ctx, &deliveryv1.GetFunctionRequest{Id: fnID})
				if err != nil {
					return err
				}
				fn := resp.GetFunction()
				return renderOut(env, jsonOut, fn, func() {
					_, _ = fmt.Fprintf(env.Stdout,
						"function %s (id %s)\nstatus %s, replicas %d (window %d..%d)\nrevision r%d (digest %s)\nin-network %s\n",
						fn.GetName(), fn.GetId(), fn.GetStatus(), fn.GetReplicas(),
						fn.GetMinReplicas(), fn.GetMaxReplicas(),
						fn.GetCurrentRevisionSeq(), fn.GetCurrentRevisionDigest(), fn.GetPrivateUrl())
					if fn.GetPublicUrl() != "" {
						_, _ = fmt.Fprintf(env.Stdout, "public %s\n", fn.GetPublicUrl())
					}
				})
			})
		},
	}
}

func newFunctionsURLVerb() commands.Command {
	const name = "url"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Print the function URLs (private and public rows)",
		usage: "functions url [--project PROJECT_ID] FUNCTION",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project for name addressing")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "exactly one FUNCTION argument is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				fnID, err := resolveFunctionID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Functions.GetFunction(ctx, &deliveryv1.GetFunctionRequest{Id: fnID})
				if err != nil {
					return err
				}
				fn := resp.GetFunction()
				return renderOut(env, jsonOut, fn, func() {
					_, _ = fmt.Fprintf(env.Stdout, "private %s\n", fn.GetPrivateUrl())
					if fn.GetPublicUrl() != "" {
						_, _ = fmt.Fprintf(env.Stdout, "public %s\n", fn.GetPublicUrl())
					} else {
						_, _ = fmt.Fprintln(env.Stdout, "public (not exposed)")
					}
				})
			})
		},
	}
}

func newFunctionsDeployVerb() commands.Command {
	const name = "deploy"
	var project, image, command, uploadID, railpackVersion string
	var envs, secrets multiFlag
	var port, cpuMillis, memoryMb int
	var wait bool
	return &flaggedVerb{
		name: name, synopsis: "Replace the function source and shape (full replace; capacity/exposure untouched)",
		usage: "functions deploy [--project PROJECT_ID] FUNCTION (--image REF | --upload-id ID --railpack-version V) [--command c1,c2] [--port N] " +
			"[--env KEY=VALUE]... [--secret NAME]... [--cpu-millis N] [--memory-mb N] [--wait]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project for name addressing")
			fs.StringVar(&image, "image", "", "container image reference (required unless --upload-id)")
			fs.StringVar(&uploadID, "upload-id", "", "uploaded source archive id (build readiness gate; mutually exclusive with --image)")
			fs.StringVar(&railpackVersion, "railpack-version", "", "railpack builder pinned version (required for --upload-id, e.g. 0.39.0)")
			fs.StringVar(&command, "command", "", "entrypoint override (comma-separated)")
			fs.IntVar(&port, "port", 8080, "handler listen port")
			fs.Var(&envs, "env", "plain env KEY=VALUE (repeatable)")
			fs.Var(&secrets, "secret", "project secret name (repeatable)")
			fs.IntVar(&cpuMillis, "cpu-millis", 0, "per-replica CPU limit in millis (0 = unlimited)")
			fs.IntVar(&memoryMb, "memory-mb", 0, "per-replica memory limit in MiB (0 = unlimited)")
			fs.BoolVar(&wait, "wait", false, "stream until the new revision is stable")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "exactly one FUNCTION argument is required")
			}
			if image == "" && uploadID == "" {
				return usageErr(name, "either --image or --upload-id is required")
			}
			if image != "" && uploadID != "" {
				return usageErr(name, "--image and --upload-id are mutually exclusive")
			}
			envMap, err := parseEnvFlags(envs)
			if err != nil {
				return usageErr(name, err.Error())
			}
			var waitID string
			if err := runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				fnID, err := resolveFunctionID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Functions.DeployFunction(ctx, &deliveryv1.DeployFunctionRequest{
					FunctionId: fnID, Image: image, UploadId: uploadID, RailpackVersion: railpackVersion,
					Command: splitCommandFlag(command), Env: envMap, SecretRefs: secrets,
					Port: clampInt32(port), Resources: functionResources(cpuMillis, memoryMb),
				})
				if err != nil {
					return err
				}
				fn := resp.GetFunction()
				if err := renderOut(env, jsonOut, fn, func() {
					_, _ = fmt.Fprintf(env.Stdout, "deployed %s to revision r%d\n",
						fn.GetName(), fn.GetCurrentRevisionSeq())
				}); err != nil {
					return err
				}
				if wait {
					waitID = fn.GetId()
				}
				return nil
			}); err != nil {
				return err
			}
			if wait {
				return waitFunction(env, jsonOut, waitID)
			}
			return nil
		},
	}
}

func newFunctionsScaleVerb() commands.Command {
	const name = "scale"
	var project string
	var idleTimeout int
	return &flaggedVerb{
		name: name, synopsis: "Set the replica count (capacity plane; zero redeploy)",
		usage: "functions scale [--project PROJECT_ID] FUNCTION N [--idle-timeout SECONDS]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project for name addressing")
			fs.IntVar(&idleTimeout, "idle-timeout", 0, "scale-to-zero idle window in seconds (0 = leave unchanged)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 2 {
				return usageErr(name, "FUNCTION and N arguments are required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				fnID, err := resolveFunctionID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				var replicas int
				if _, err := fmt.Sscanf(args[1], "%d", &replicas); err != nil || replicas < 0 {
					return usageErr(name, fmt.Sprintf("N must be a non-negative integer (got %q)", args[1]))
				}
				resp, err := c.Functions.ScaleFunction(ctx, &deliveryv1.ScaleFunctionRequest{
					FunctionId: fnID, Replicas: int64(replicas), IdleTimeoutSeconds: int64(idleTimeout),
				})
				if err != nil {
					return err
				}
				fn := resp.GetFunction()
				return renderOut(env, jsonOut, fn, func() {
					_, _ = fmt.Fprintf(env.Stdout, "scaled %s to %d replicas (window %d..%d)\n",
						fn.GetName(), fn.GetReplicas(), fn.GetMinReplicas(), fn.GetMaxReplicas())
				})
			})
		},
	}
}

func newFunctionsExposureVerb() commands.Command {
	const name = "exposure"
	var project, host string
	return &flaggedVerb{
		name: name, synopsis: "Flip exposure between private and public (route face republishes immediately)",
		usage: "functions exposure [--project PROJECT_ID] FUNCTION public --host HOST | functions exposure [--project PROJECT_ID] FUNCTION private",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project for name addressing")
			fs.StringVar(&host, "host", "", "public host (required for public; default derives sslip.io)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 2 {
				return usageErr(name, "FUNCTION and STATE (public|private) arguments are required")
			}
			state := args[1]
			if state != "public" && state != "private" {
				return usageErr(name, fmt.Sprintf("STATE must be public or private (got %q)", state))
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				fnID, err := resolveFunctionID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				if state == "public" && host == "" && connectionIsIP() {
					// sslip 缺省（IP 面）；域名连接交服务端 base_domain 层。
					host = sslipHost(envOr(envAddr, defaultAddr), "fn-"+fnID)
				}
				resp, err := c.Functions.SetFunctionExposure(ctx, &deliveryv1.SetFunctionExposureRequest{
					FunctionId: fnID, Exposure: state, Host: host,
				})
				if err != nil {
					return err
				}
				fn := resp.GetFunction()
				return renderOut(env, jsonOut, fn, func() {
					_, _ = fmt.Fprintf(env.Stdout, "%s exposure is %s\n", fn.GetName(), fn.GetExposure())
					if fn.GetPublicUrl() != "" {
						_, _ = fmt.Fprintf(env.Stdout, "public %s\n", fn.GetPublicUrl())
					}
				})
			})
		},
	}
}


func newFunctionsStatsVerb() commands.Command {
	const name = "stats"
	var project string
	var window int
	return &flaggedVerb{
		name: name, synopsis: "Show request-rate and latency observations (public functions; private has no request observability)",
		usage: "functions stats [--project PROJECT_ID] FUNCTION [--window SECONDS]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project for name addressing")
			fs.IntVar(&window, "window", 0, "observation window in seconds (0 = default 3600; max 86400)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "exactly one FUNCTION argument is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				fnID, err := resolveFunctionID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Functions.GetFunctionStats(ctx, &deliveryv1.GetFunctionStatsRequest{
					FunctionId: fnID, WindowSeconds: int64(window),
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					if !resp.GetObserved() {
						_, _ = fmt.Fprintln(env.Stdout, "no request observability (private functions have no per-invocation axis)")
						return
					}
					_, _ = fmt.Fprintf(env.Stdout, "window %s .. %s\n", resp.GetWindowStart(), resp.GetWindowEnd())
					renderSeries := func(title string, pts []*deliveryv1.FunctionStatsSample, unit string) {
						_, _ = fmt.Fprintf(env.Stdout, "%s\n", title)
						if len(pts) == 0 {
							_, _ = fmt.Fprintln(env.Stdout, "  (no samples in window)")
							return
						}
						for _, p := range pts {
							_, _ = fmt.Fprintf(env.Stdout, "  %s  %.4g %s\n", p.GetTs(), p.GetValue(), unit)
						}
					}
					renderSeries("requests/s:", resp.GetRequestsPerSecond(), "req/s")
					renderSeries("p95 latency:", resp.GetP95Ms(), "ms")
				})
			})
		},
	}
}

func newFunctionsRollbackVerb() commands.Command {
	const name = "rollback"
	var project string
	var toRevision int64
	return &flaggedVerb{
		name: name, synopsis: "Re-track to a frozen revision (replays the existing row; default: previous)",
		usage: "functions rollback [--project PROJECT_ID] FUNCTION [--to-revision N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project for name addressing")
			fs.Int64Var(&toRevision, "to-revision", 0, "target revision seq (0 = previous)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "exactly one FUNCTION argument is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				fnID, err := resolveFunctionID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Functions.RollbackFunction(ctx, &deliveryv1.RollbackFunctionRequest{
					FunctionId: fnID, RevisionSeq: toRevision,
				})
				if err != nil {
					return err
				}
				fn := resp.GetFunction()
				return renderOut(env, jsonOut, fn, func() {
					_, _ = fmt.Fprintf(env.Stdout, "rolled back %s to revision r%d\n",
						fn.GetName(), fn.GetCurrentRevisionSeq())
				})
			})
		},
	}
}

func newFunctionsRevisionsVerb() commands.Command {
	const name = "revisions"
	var project string
	var revisionsLimit int
	return &flaggedVerb{
		name: name, synopsis: "List frozen revisions (retention window)",
		usage: "functions revisions [--project PROJECT_ID] FUNCTION [--limit N]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project for name addressing")
			fs.IntVar(&revisionsLimit, "limit", 0, "page size (0 = server default 50; max 200 — the retention window holds up to 100)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "exactly one FUNCTION argument is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				fnID, err := resolveFunctionID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Functions.ListFunctionRevisions(ctx, &deliveryv1.ListFunctionRevisionsRequest{
					FunctionId: fnID, Limit: int32(revisionsLimit), //nolint:gosec // limit 恒小值域
				})
				if err != nil {
					return err
				}
				return renderOut(env, jsonOut, resp, func() {
					if len(resp.GetRevisions()) == 0 {
						_, _ = fmt.Fprintln(env.Stdout, "no revisions")
						return
					}
					_, _ = fmt.Fprintln(env.Stdout, "SEQ\tDIGEST\tCREATED")
					for _, r := range resp.GetRevisions() {
						_, _ = fmt.Fprintf(env.Stdout, "r%d\t%s\t%s\n", r.GetSeq(), r.GetDigest(), r.GetCreatedAt())
					}
				})
			})
		},
	}
}

func newFunctionsLogsVerb() commands.Command {
	const name = "logs"
	var project string
	var tail int64
	var follow bool
	var since, until, text string
	return &flaggedVerb{
		name: name, synopsis: "Stream function carrier logs (function_id axis)",
		usage: "functions logs [--project PROJECT_ID] FUNCTION [--tail N] [--follow] [--since RFC3339] [--until RFC3339] [--text QUERY]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project for name addressing")
			fs.Int64Var(&tail, "tail", 0, "recent lines to fetch first (0 = all buffered)")
			fs.BoolVar(&follow, "follow", false, "keep streaming new lines")
			fs.StringVar(&since, "since", "", "window start (RFC3339)")
			fs.StringVar(&until, "until", "", "window end (RFC3339)")
			fs.StringVar(&text, "text", "", "text filter over the retention window")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "exactly one FUNCTION argument is required")
			}
			// 流式动词：拨号豁免请求级 deadline（follow 长存活）。
			return runRPC(ctx, []dialOption{noDeadline()}, func(ctx context.Context, c *fleetly.Client) error {
				fnID, err := resolveFunctionID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				stream, err := c.Logs.StreamLogs(ctx, &telemetryv1.StreamLogsRequest{
					FunctionId: fnID, TailLines: tail, Follow: follow, Since: since, Until: until, Text: text,
				})
				if err != nil {
					return err
				}
				// 帧经 json.Compact 归一（protojson detrand 的随机空白按二进制
				// 撒种——TrimSpace 只去首尾，键值间 ": " 形态漂移会咬 golden；
				// telemetry logs 动词同款纪律）。
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
					if errors.Is(err, io.EOF) {
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
					_, _ = fmt.Fprintf(env.Stdout, "%s %s %s\n", frame.GetTime(), frame.GetWorkloadId(), string(frame.GetLine()))
				}
			})
		},
	}
}

func newFunctionsDeleteVerb() commands.Command {
	const name = "delete"
	var project string
	return &flaggedVerb{
		name: name, synopsis: "Delete a function (tombstone; revisions cleared with the aggregate)",
		usage: "functions delete [--project PROJECT_ID] FUNCTION",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project for name addressing")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "exactly one FUNCTION argument is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				fnID, err := resolveFunctionID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				if _, err := c.Functions.DeleteFunction(ctx, &deliveryv1.DeleteFunctionRequest{Id: fnID}); err != nil {
					return err
				}
				if jsonOut {
					_, _ = fmt.Fprintln(env.Stdout, `{"deleted":true}`)
					return nil
				}
				_, _ = fmt.Fprintf(env.Stdout, "deleted function %s\n", args[0])
				return nil
			})
		},
	}
}

func newFunctionsInvokeVerb() commands.Command {
	const name = "invoke"
	var project, data, method string
	return &flaggedVerb{
		name: name, synopsis: "Invoke a public function directly from this client (the control plane never proxies)",
		usage: "functions invoke [--project PROJECT_ID] FUNCTION [--data @file.json|JSON] [--method POST]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project for name addressing")
			fs.StringVar(&data, "data", "{}", "request body (@file reads a file; default {})")
			fs.StringVar(&method, "method", http.MethodPost, "HTTP method")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "exactly one FUNCTION argument is required")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *fleetly.Client) error {
				fnID, err := resolveFunctionID(ctx, c, project, args[0])
				if err != nil {
					return err
				}
				resp, err := c.Functions.GetFunction(ctx, &deliveryv1.GetFunctionRequest{Id: fnID})
				if err != nil {
					return err
				}
				fn := resp.GetFunction()
				if fn.GetPublicUrl() == "" {
					return fmt.Errorf("function %s is private — invoke it from inside the project network at %s (the control plane does not proxy)", fn.GetName(), fn.GetPrivateUrl())
				}
				body := data
				if strings.HasPrefix(data, "@") {
					raw, rerr := os.ReadFile(strings.TrimPrefix(data, "@"))
					if rerr != nil {
						return rerr
					}
					body = string(raw)
				}
				req, rerr := http.NewRequestWithContext(ctx, method, fn.GetPublicUrl(), strings.NewReader(body))
				if rerr != nil {
					return rerr
				}
				req.Header.Set("Content-Type", "application/json")
				hresp, herr := http.DefaultClient.Do(req)
				if herr != nil {
					return herr
				}
				defer func() { _ = hresp.Body.Close() }()
				_, _ = fmt.Fprintf(env.Stdout, "%s %s\n", hresp.Proto, hresp.Status)
				if _, cerr := io.Copy(env.Stdout, hresp.Body); cerr != nil {
					return cerr
				}
				return nil
			})
		},
	}
}
