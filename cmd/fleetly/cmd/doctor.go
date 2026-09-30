package cmd

// fleetly doctor（F0.4）：本机诊断（Docker 版本/daemon 可达/swarm 态/
// fleetlyd 端口监听/磁盘余量/时钟漂移）+ 远程 fleetlyd status 合流，输出
// 处置建议。探针全部是包级接缝（golden 注入确定性假探针；真机行为由
// dind smoke 锚定）。--json 双形态；任一 fail 退出码 1（可脚本分支）。

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/lynx-go/commands"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// doctorCheckStatus 三态（ok/warn/fail）。
const (
	checkOK   = "ok"
	checkWarn = "warn"
	checkFail = "fail"
)

// doctorCheck 是一条诊断结论（area=local|remote）。
type doctorCheck struct {
	Area   string `json:"area"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Advice string `json:"advice,omitempty"`
}

// doctorSummary 是计数面。
type doctorSummary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

// doctorReport 是 --json 形态。
type doctorReport struct {
	Checks  []doctorCheck `json:"checks"`
	Summary doctorSummary `json:"summary"`
}

// dockerProbeResult 是 Docker 面一次探测结果。
type dockerProbeResult struct {
	ClientVersion string    // 客户端版本（空 = docker CLI 不可用）
	ServerVersion string    // daemon 版本（空 = daemon 不可达）
	SwarmState    string    // LocalNodeState（active/inactive/pending/error）
	SystemTime    time.Time // daemon 时钟（零值 = 未知）
	Err           string    // CLI 缺失/命令失败原因
}

// 探针接缝（golden 注入；真机走 exec/RPC/系统调用）。
var (
	probeDocker = execDockerProbe
	probePort   = execPortProbe
	probeDisk   = syscallDiskProbe
	probeRemote = rpcRemoteProbe
)

const (
	// doctorPorts 是本机 fleetlyd 监听面（安装预检）。
	doctorGRPCPort = "127.0.0.1:9080"
	doctorHTTPPort = "127.0.0.1:9081"
	// doctorDriftTolerance 是时钟漂移容差（LE 证书窗口与审计时间线的下限）。
	doctorDriftTolerance = 2 * time.Minute
	// disk 阈值：2 GiB 硬下限（镜像+构建上下文），10 GiB 建议线。
	doctorDiskFail  = 2 << 30
	doctorDiskWarn  = 10 << 30
	doctorProbeWait = 1500 * time.Millisecond
	// doctorDockerWait 是 docker CLI 探测超时（docker info 首调明显慢于
	// version——Windows/Docker Desktop 实测 >1.5s）。
	doctorDockerWait = 5 * time.Second
)

func newDoctorVerb() commands.Command {
	const name = "doctor"
	var addr string
	return &flaggedVerb{
		name:     name,
		synopsis: "Diagnose the local install (docker, ports, disk, clock) and the remote fleetlyd",
		usage:    "doctor [--addr ADDR] (local checks probe this machine; remote merges 'fleetly status')",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&addr, "addr", "", fmt.Sprintf("fleetlyd gRPC address to merge status from (env %s)", envAddr))
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if err := noArgs(name, args); err != nil {
				return err
			}
			if addr == "" {
				addr = envOr(envAddr, defaultAddr)
			}
			report := runDoctorProbes(ctx, addr)
			if jsonOut {
				return writeJSON(env.Stdout, report)
			}
			writeDoctorHuman(env, report)
			if report.Summary.Fail > 0 {
				return fmt.Errorf("doctor: %d check(s) failed; see the report above", report.Summary.Fail)
			}
			return nil
		},
	}
}

// runDoctorProbes 跑全部探针并汇总（探针失败本身就是诊断结论，不中断）。
func runDoctorProbes(ctx context.Context, addr string) doctorReport {
	var checks []doctorCheck
	local := func(c doctorCheck) doctorCheck { c.Area = "local"; return c }

	// Docker 面。
	dk := probeDocker(ctx)
	switch {
	case dk.Err != "" && dk.ClientVersion == "":
		checks = append(checks, local(doctorCheck{
			Name: "docker cli", Status: checkFail, Detail: dk.Err,
			Advice: "install Docker: curl -fsSL https://get.docker.com | sh (or use your OS package manager)",
		}))
	case dk.ServerVersion == "":
		checks = append(checks, local(doctorCheck{
			Name: "docker daemon", Status: checkFail, Detail: "docker CLI works but the daemon is unreachable",
			Advice: "start the docker service (systemctl start docker) and re-run 'fleetly doctor'",
		}))
	default:
		checks = append(checks,
			local(doctorCheck{Name: "docker client", Status: checkOK, Detail: dk.ClientVersion}),
			local(doctorCheck{Name: "docker daemon", Status: checkOK, Detail: dk.ServerVersion}),
		)
		switch dk.SwarmState {
		case "active":
			checks = append(checks, local(doctorCheck{Name: "swarm", Status: checkOK, Detail: "active"}))
		case "":
			// daemon 不可达时已由上一条报告。
		default:
			checks = append(checks, local(doctorCheck{
				Name: "swarm", Status: checkWarn, Detail: dk.SwarmState,
				Advice: "initialize a single-node swarm: docker swarm init (the installer does this for you)",
			}))
		}
		if !dk.SystemTime.IsZero() {
			drift := time.Since(dk.SystemTime)
			if drift < 0 {
				drift = -drift
			}
			if drift > doctorDriftTolerance {
				checks = append(checks, local(doctorCheck{
					Name: "clock drift", Status: checkWarn,
					Detail: fmt.Sprintf("%s off the docker daemon clock", drift.Round(time.Second)),
					Advice: "enable NTP/time sync (cert windows and audit timelines depend on it)",
				}))
			} else {
				checks = append(checks, local(doctorCheck{Name: "clock drift", Status: checkOK, Detail: "within tolerance"}))
			}
		}
	}

	// 端口监听（安装后置检查：fleetlyd 应在监听）。
	for _, p := range []struct{ name, addr string }{
		{"fleetlyd grpc (:9080)", doctorGRPCPort},
		{"gateway http (:9081)", doctorHTTPPort},
	} {
		if err := probePort(p.addr); err != nil {
			checks = append(checks, local(doctorCheck{
				Name: p.name, Status: checkWarn, Detail: "not listening",
				Advice: "expected after 'fleetly init' on this machine; if fleetlyd should be running, check its logs",
			}))
		} else {
			checks = append(checks, local(doctorCheck{Name: p.name, Status: checkOK, Detail: "listening"}))
		}
	}

	// 磁盘余量（数据根所在卷；doctor 在哪跑就查哪）。
	if free, err := probeDisk("."); err != nil {
		checks = append(checks, local(doctorCheck{
			Name: "disk", Status: checkWarn, Detail: err.Error(), Advice: "check the data root volume manually",
		}))
	} else {
		gib := float64(free) / (1 << 30)
		switch {
		case free < doctorDiskFail:
			checks = append(checks, local(doctorCheck{
				Name: "disk", Status: checkFail, Detail: fmt.Sprintf("%.1f GiB free", gib),
				Advice: "free at least a few GiB (images and build contexts live here)",
			}))
		case free < doctorDiskWarn:
			checks = append(checks, local(doctorCheck{
				Name: "disk", Status: checkWarn, Detail: fmt.Sprintf("%.1f GiB free", gib),
				Advice: "consider pruning unused images (docker image prune) before deploying more",
			}))
		default:
			checks = append(checks, local(doctorCheck{Name: "disk", Status: checkOK, Detail: fmt.Sprintf("%.1f GiB free", gib)}))
		}
	}

	// 远程 fleetlyd 合流（status 面，PUBLIC）。
	if resp, err := probeRemote(ctx, addr); err != nil {
		checks = append(checks, doctorCheck{
			Area: "remote", Name: "fleetlyd " + addr, Status: checkFail, Detail: err.Error(),
			Advice: fmt.Sprintf("verify the address (flag --addr / env %s) and that fleetlyd is running", envAddr),
		})
	} else {
		state := statusStateText(resp.GetState())
		checks = append(checks, doctorCheck{
			Area: "remote", Name: "fleetlyd " + addr, Status: checkOK,
			Detail: fmt.Sprintf("%s, server %s", state, serverVersionOrUnknown(resp.GetVersion())),
		})
	}

	rep := doctorReport{Checks: checks}
	for _, c := range checks {
		switch c.Status {
		case checkOK:
			rep.Summary.OK++
		case checkWarn:
			rep.Summary.Warn++
		case checkFail:
			rep.Summary.Fail++
		}
	}
	return rep
}

// writeDoctorHuman 渲染人类形态（每行 [状态] 名称 + 细节；warn/fail 追加
// 处置建议行）。
func writeDoctorHuman(env *commands.Environment, rep doctorReport) {
	for _, c := range rep.Checks {
		_, _ = fmt.Fprintf(env.Stdout, "[%-4s] %-24s %s\n", c.Status, c.Name, c.Detail)
		if c.Advice != "" && c.Status != checkOK {
			_, _ = fmt.Fprintf(env.Stdout, "        advice: %s\n", c.Advice)
		}
	}
	_, _ = fmt.Fprintf(env.Stdout, "\n%d ok, %d warning(s), %d failed\n",
		rep.Summary.OK, rep.Summary.Warn, rep.Summary.Fail)
}

// execDockerProbe 经 docker CLI 探测版本/daemon/swarm/时钟。
func execDockerProbe(ctx context.Context) dockerProbeResult {
	out := dockerProbeResult{}
	ver, err := dockerFormat(ctx, "{{.Client.Version}}")
	if err != nil {
		out.Err = fmt.Sprintf("docker CLI unavailable: %v", err)
		return out
	}
	out.ClientVersion = ver
	if srv, err := dockerFormat(ctx, "{{.Server.Version}}"); err == nil {
		out.ServerVersion = srv
	}
	if st, err := dockerFormat(ctx, "{{.Swarm.LocalNodeState}}"); err == nil {
		out.SwarmState = st
	}
	if ts, err := dockerFormat(ctx, "{{.SystemTime}}"); err == nil {
		if t, perr := time.Parse(time.RFC3339Nano, ts); perr == nil {
			out.SystemTime = t
		}
	}
	return out
}

// dockerFormat 跑 docker <subject> --format（probe 专用小助手）。
func dockerFormat(ctx context.Context, goTpl string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, doctorDockerWait)
	defer cancel()
	subject := "info"
	if goTpl == "{{.Client.Version}}" || goTpl == "{{.Server.Version}}" {
		subject = "version"
	}
	out, err := execCommand(callCtx, "docker", subject, "--format", goTpl)
	if err != nil {
		return "", err
	}
	return trimSpace(out), nil
}

// execPortProbe 探测 TCP 监听。
func execPortProbe(addr string) error {
	return dialTCPTimeout(addr, doctorProbeWait)
}

// rpcRemoteProbe 拉 fleetlyd status（PUBLIC 面；凭证可选携带）。
func rpcRemoteProbe(ctx context.Context, addr string) (*systemv1.GetStatusResponse, error) {
	callCtx, cancel := context.WithTimeout(ctx, fleetly.DefaultTimeout)
	defer cancel()
	c, err := dialClient(addr)
	if err != nil {
		return nil, err
	}
	defer c.Close() //nolint:errcheck // 进程退出路径
	return c.System.GetStatus(fleetly.WithToken(callCtx, resolveToken("")), nil)
}
