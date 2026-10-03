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
	t.Setenv(envEdgeConfigEndpoint, "")
	t.Setenv(envRegistryAddr, "")
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
	t.Setenv(envEdgeConfigEndpoint, "")
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
