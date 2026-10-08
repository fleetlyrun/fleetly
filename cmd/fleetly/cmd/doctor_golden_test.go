package cmd

// doctor golden 双形态（F0.4）：探针接缝注入确定性结果（真机行为由 dind
// smoke 锚定）；另覆盖 fail 退出码路径。

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	telemetryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/telemetry/v1"
)

func injectDoctorProbes(t *testing.T, dk dockerProbeResult, portErr error, diskFree int64, diskErr error, remote *systemv1.GetStatusResponse, remoteErr error) {
	t.Helper()
	origDocker, origPort, origDisk, origRemote := probeDocker, probePort, probeDisk, probeRemote
	probeDocker = func(context.Context) dockerProbeResult { return dk }
	probePort = func(string) error { return portErr }
	probeDisk = func(string) (int64, error) { return diskFree, diskErr }
	probeRemote = func(context.Context, string) (*systemv1.GetStatusResponse, error) { return remote, remoteErr }
	t.Cleanup(func() {
		probeDocker, probePort, probeDisk, probeRemote = origDocker, origPort, origDisk, origRemote
	})
}

func TestGoldenDoctor(t *testing.T) {
	// 暴露自证输入钉空（ADR-0036）：golden 断言不依赖环境变量——真机
	// 由 dind smoke 锚定，夹具路径永远是"未配置面 + 缺省绑面"。
	t.Setenv(envProxyConfigEndpoint, "")
	t.Setenv(envRegistryAddr, "")
	t.Setenv(envRuntimeProvider, "")
	t.Setenv(envServerGRPCAddr, "")
	t.Setenv(envServerHTTPAddr, "")
	t.Setenv(envServerProxyConfAddr, "")
	injectDoctorProbes(t,
		dockerProbeResult{
			ClientVersion: "29.7.2", ServerVersion: "29.7.2", SwarmState: "active",
			SystemTime: time.Now().UTC(),
		},
		nil, 128<<30, nil,
		&systemv1.GetStatusResponse{State: systemv1.StatusState_STATUS_STATE_HEALTHY, Version: "0.1.0-test"},
		nil,
	)
	code, out, stderr := runCLI(t, "doctor")
	if code != 0 || stderr != "" {
		t.Fatalf("doctor: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "doctor", out)

	code, out, stderr = runCLI(t, "doctor", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("doctor --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "doctor-json", out)
}

func TestDoctorFailuresExitNonZero(t *testing.T) {
	t.Setenv(envProxyConfigEndpoint, "")
	t.Setenv(envRegistryAddr, "")
	injectDoctorProbes(t,
		dockerProbeResult{Err: "exec: docker: not found"},
		errors.New("connection refused"), 1<<30, nil,
		nil, errors.New("connection refused"),
	)
	code, out, stderr := runCLI(t, "doctor")
	assert.Equal(t, 1, code, "failing checks must exit 1")
	assert.Contains(t, stderr, "doctor:")
	assert.Contains(t, out, "[fail]")
}

// injectAlertingProbe 注入告警面探针（F2.5/ADR-0041 doctor 两件——其余
// 探针同时钉健康形态，断言只看 alerting 行）。
func injectAlertingProbe(t *testing.T, chs []*telemetryv1.NotificationChannel, sts []*telemetryv1.AlertState) {
	t.Helper()
	injectDoctorProbes(t,
		dockerProbeResult{ClientVersion: "29.7.2", ServerVersion: "29.7.2", SwarmState: "active"},
		nil, 128<<30, nil,
		&systemv1.GetStatusResponse{State: systemv1.StatusState_STATUS_STATE_HEALTHY, Version: "0.1.0-test"},
		nil,
	)
	orig := probeAlerting
	probeAlerting = func(context.Context, string) ([]*telemetryv1.NotificationChannel, []*telemetryv1.AlertState, error) {
		return chs, sts, nil
	}
	t.Cleanup(func() { probeAlerting = orig })
}

// TestDoctorAlertingChecks（ADR-0041 锚 4——doctor 两件）：①无通道 → warn
// 且建议可行动（channels create 指引；e2e dind smoke 3c 段断言同款）；
// ②内置规则 firing → warn 行 + platform_backup.s3 处置建议（24h 持续窗
// 不进 e2e——单测是唯一锚）；③通道在场 → 消警 ok（e2e smoke F2.5 段断言
// 同款）。warn-only 检查不得置失败退出码。
func TestDoctorAlertingChecks(t *testing.T) {
	t.Setenv(envProxyConfigEndpoint, "")
	t.Setenv(envRegistryAddr, "")

	// ① 无通道：warn + 可行动建议。
	injectAlertingProbe(t, nil, nil)
	code, out, _ := runCLI(t, "doctor")
	assert.Equal(t, 0, code, "warn-only checks must keep exit 0")
	assert.Contains(t, out, "[warn] notification channels")
	assert.Contains(t, out, "no notification channel configured")
	assert.Contains(t, out, "fleetly channels create", "the warn must carry actionable advice")

	// ② 内置规则 firing：s3 未配持续告警的诊断行（规则 id + 消警路径）。
	injectAlertingProbe(t, nil, []*telemetryv1.AlertState{{
		RuleId: "platform-offsite-backup", Metric: "platform_offsite_backup",
		State: "firing", StateSince: "2026-10-04T00:00:00Z", System: true,
	}})
	code, out, _ = runCLI(t, "doctor")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "[warn] platform-offsite-backup")
	assert.Contains(t, out, "firing since 2026-10-04T00:00:00Z")
	assert.Contains(t, out, "platform_backup.s3", "the system-rule warn must point at the offsite repo config")

	// ③ 通道在场：消警（无通道 warn 消失、检查归位 ok——非 firing 的
	// 系统行不产生诊断）。
	injectAlertingProbe(t,
		[]*telemetryv1.NotificationChannel{{Id: "01JALERTINGTEST", Name: "ops-hook", Kind: "webhook"}},
		[]*telemetryv1.AlertState{{
			RuleId: "platform-offsite-backup", Metric: "platform_offsite_backup",
			State: "ok", StateSince: "2026-10-04T00:00:00Z", System: true,
		}},
	)
	code, out, _ = runCLI(t, "doctor")
	assert.Equal(t, 0, code)
	assert.Contains(t, out, "[ok")
	assert.Contains(t, out, "notification channels")
	assert.Contains(t, out, "1 channel(s) configured")
	assert.NotContains(t, out, "[warn] notification channels", "a configured channel must clear the warn")
	assert.NotContains(t, out, "[warn] platform-offsite-backup", "a non-firing system rule must not warn")
}

// TestDoctorK3sRuntimeFormSkipsDockerProbes（ADR-0055 实录锚）：k3s 形态
// （env FLEETLY_RUNTIME_PROVIDER=k3s，daemon 同键）下 docker 面整体跳过、
// 以一行 ok 呈报——k3s 节点无 docker 是合法形态，恒红会淹没真信号
// （staging k3s 实证）。docker 探针注入"若被咨询即 fail"形态反证未触达。
func TestDoctorK3sRuntimeFormSkipsDockerProbes(t *testing.T) {
	t.Setenv(envProxyConfigEndpoint, "")
	t.Setenv(envRegistryAddr, "")
	t.Setenv(envRuntimeProvider, "k3s")
	injectDoctorProbes(t,
		dockerProbeResult{Err: "exec: docker: not found"}, // 会被 fail 的形态
		nil, 128<<30, nil,
		&systemv1.GetStatusResponse{State: systemv1.StatusState_STATUS_STATE_HEALTHY, Version: "0.1.0-test"},
		nil,
	)
	code, out, stderr := runCLI(t, "doctor")
	assert.Equal(t, 0, code, "k3s form must not fail on absent docker")
	assert.Empty(t, stderr)
	assert.Contains(t, out, "[ok  ] runtime form")
	assert.Contains(t, out, "k3s (local docker probes skipped")
	assert.NotContains(t, out, "docker cli", "docker probes must be skipped entirely in k3s form")
}

// TestDoctorPortProbeUsesEffectiveBind（ADR-0055 实录锚）：端口监听探测目标
// = 生效绑址——钉址绑面（env FLEETLY_SERVER_GRPC_ADDR 同 daemon 键）时探
// 该地址而非 127.0.0.1 假警；通配缺省维持回环探测（golden 形态不变）。
func TestDoctorPortProbeUsesEffectiveBind(t *testing.T) {
	t.Setenv(envProxyConfigEndpoint, "")
	t.Setenv(envRegistryAddr, "")
	t.Setenv(envRuntimeProvider, "k3s")
	injectDoctorProbes(t,
		dockerProbeResult{Err: "exec: docker: not found"},
		nil, 128<<30, nil,
		&systemv1.GetStatusResponse{State: systemv1.StatusState_STATUS_STATE_HEALTHY, Version: "0.1.0-test"},
		nil,
	)
	var probed []string
	orig := probePort
	probePort = func(a string) error { probed = append(probed, a); return nil }
	t.Cleanup(func() { probePort = orig })

	t.Run("pinned bind probes the pinned address", func(t *testing.T) {
		t.Setenv(envServerGRPCAddr, "10.124.0.5:9090")
		t.Setenv(envServerHTTPAddr, "10.124.0.5:9091")
		probed = nil
		_, _, _ = runCLI(t, "doctor")
		assert.Contains(t, probed, "10.124.0.5:9090", "grpc probe must target the pinned bind")
		assert.Contains(t, probed, "10.124.0.5:9091", "http probe must target the pinned bind")
		assert.NotContains(t, probed, "127.0.0.1:9090")
	})
	t.Run("wildcard default stays loopback", func(t *testing.T) {
		t.Setenv(envServerGRPCAddr, "")
		t.Setenv(envServerHTTPAddr, "")
		probed = nil
		_, _, _ = runCLI(t, "doctor")
		assert.Contains(t, probed, doctorGRPCPort)
		assert.Contains(t, probed, doctorHTTPPort)
	})
}
