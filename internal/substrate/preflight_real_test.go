//go:build fleetly_docker

package substrate_test

// 实机集成测试（T2.9 验收第 5 项）：PreflightImage 对真实 Docker daemon 的
// 两分支验证——可得（digest 带出）与缺失（E_IMAGE_UNAVAILABLE +
// W_ROLLBACK_IMAGE_RISK，errors.Is 判定 ErrImageNotFound）。
//
// 不进 CI（CI 无 docker）。跑法（本机 Docker 可用时）：
//
//	go test -tags fleetly_docker ./internal/substrate/ -run TestRealDaemonPreflight -v
//
// 用镜像：FLEETLY_PREFLIGHT_IMAGE（缺省 moby/buildkit:v0.32.2——平台自管
// buildkitd 的钉版镜像，跑过 fleetlyd 即存在；缺失时测试自动 pull）。

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/build"
	"github.com/fleetlyrun/fleetly/internal/substrate"
)

func newRealClient(t *testing.T) *substrate.Client {
	t.Helper()
	c, err := substrate.NewClient("")
	if err != nil {
		t.Fatalf("construct substrate client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := c.Ping(ctx); err != nil {
		t.Skipf("docker daemon unreachable: %v", err)
	}
	return c
}

func TestRealDaemonPreflight(t *testing.T) {
	c := newRealClient(t)
	image := "moby/buildkit:v0.32.2"
	if custom := os.Getenv("FLEETLY_PREFLIGHT_IMAGE"); custom != "" {
		image = custom
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// 预热：确保被测镜像存在（缺失时拉取；「清空类操作跟空态断言」的
	// 逆命题——「存在类操作先做存在断言」）。
	if err := c.EnsureImagePresent(ctx, image); err != nil {
		t.Fatalf("ensure image %s: %v", image, err)
	}
	// 可得分支。
	got, err := build.PreflightImage(ctx, c, image)
	if err != nil {
		t.Fatalf("preflight (available): %v", err)
	}
	if !got.Available || got.Digest == "" || got.Warning != "" {
		t.Fatalf("available preflight result = %+v", got)
	}
	t.Logf("available: %s -> %s", image, got.Digest)

	// 缺失分支：造一个独占 tag（指向同一镜像），preflight 可得 → rmi →
	// preflight 缺失（E_IMAGE_UNAVAILABLE + W_ROLLBACK_IMAGE_RISK）。
	testTag := fmt.Sprintf("fleetly-preflight-test:rm-%d", time.Now().UnixNano())
	if err := c.TagImage(ctx, image, testTag); err != nil {
		t.Fatalf("tag image: %v", err)
	}
	t.Cleanup(func() { _ = c.RemoveImage(context.Background(), testTag) })

	if got, err := build.PreflightImage(ctx, c, testTag); err != nil || !got.Available {
		t.Fatalf("preflight (tagged) = %+v, %v; want available", got, err)
	}
	if err := c.RemoveImage(ctx, testTag); err != nil {
		t.Fatalf("rmi %s: %v", testTag, err)
	}
	got, err = build.PreflightImage(ctx, c, testTag)
	if err == nil {
		t.Fatal("preflight after rmi must fail")
	}
	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error = %T, want *apperr.Error", err)
	}
	if appErr.Code() != "E_IMAGE_UNAVAILABLE" {
		t.Fatalf("code = %s, want E_IMAGE_UNAVAILABLE", appErr.Code())
	}
	if appErr.Context()["warning"] != build.WarningRollbackImageRisk {
		t.Fatalf("context warning = %q, want W_ROLLBACK_IMAGE_RISK", appErr.Context()["warning"])
	}
	if !errors.Is(err, build.ErrImageNotFound) {
		t.Fatal("errors.Is(err, ErrImageNotFound) = false, want true")
	}
	if got.Available || got.Warning != build.WarningRollbackImageRisk {
		t.Fatalf("missing preflight result = %+v", got)
	}
	t.Logf("missing: %s -> E_IMAGE_UNAVAILABLE + W_ROLLBACK_IMAGE_RISK (as designed)", testTag)
}
