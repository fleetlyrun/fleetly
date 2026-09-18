package build

// preflight 测试（T2.9 验收：镜像缺失 → E_IMAGE_UNAVAILABLE +
// W_ROLLBACK_IMAGE_RISK）：假 ImageSource 两分支 + 底座错误透传。

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

// fakeImages 是 preflight 用的假镜像端口。
type fakeImages struct {
	byRef map[string]string // ref → digest（存在集）
	err   error             // 非缺失类错误注入
}

func (f fakeImages) InspectImage(_ context.Context, ref string) (ImageInfo, error) {
	if f.err != nil {
		return ImageInfo{}, f.err
	}
	if digest, ok := f.byRef[ref]; ok {
		return ImageInfo{ID: digest}, nil
	}
	return ImageInfo{}, ErrImageNotFound
}

func (fakeImages) LoadImage(_ context.Context, _ io.Reader) error { return nil }

// TestPreflightImageAvailable 分支一：镜像可得 → digest 带出。
func TestPreflightImageAvailable(t *testing.T) {
	images := fakeImages{byRef: map[string]string{"fleetly-local/a:b": "sha256:aa"}}
	got, err := PreflightImage(context.Background(), images, "fleetly-local/a:b")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	if !got.Available || got.Digest != "sha256:aa" || got.Warning != "" {
		t.Fatalf("result = %+v", got)
	}
}

// TestPreflightImageMissing 分支二：镜像缺失 → E_IMAGE_UNAVAILABLE 信封 +
// W_ROLLBACK_IMAGE_RISK 警告（context 与结果双通道）。
func TestPreflightImageMissing(t *testing.T) {
	images := fakeImages{byRef: map[string]string{}}
	got, err := PreflightImage(context.Background(), images, "fleetly-local/a:gone")
	if err == nil {
		t.Fatal("missing image must fail preflight")
	}
	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("error = %T, want *apperr.Error", err)
	}
	if appErr.Code() != "E_IMAGE_UNAVAILABLE" {
		t.Fatalf("code = %s, want E_IMAGE_UNAVAILABLE", appErr.Code())
	}
	if appErr.Context()["warning"] != WarningRollbackImageRisk {
		t.Fatalf("context warning = %q, want W_ROLLBACK_IMAGE_RISK", appErr.Context()["warning"])
	}
	if appErr.Envelope().GetPhase() != "preflight" {
		t.Fatalf("phase = %q, want preflight", appErr.Envelope().GetPhase())
	}
	if got.Available || got.Digest != "" || got.Warning != WarningRollbackImageRisk {
		t.Fatalf("result = %+v", got)
	}
	// 缺失哨兵必须可被调用方 errors.Is 判定（引擎侧分类依据）。
	if !errors.Is(err, ErrImageNotFound) {
		t.Fatal("errors.Is(err, ErrImageNotFound) = false, want true")
	}
}

// TestPreflightImageDaemonError 底座不可达等其他错误原样透传（不挪用镜像
// 码语义——归 E_RUNTIME_UNAVAILABLE 面由引擎票裁决）。
func TestPreflightImageDaemonError(t *testing.T) {
	daemonDown := errors.New("daemon unreachable")
	images := fakeImages{err: daemonDown}
	_, err := PreflightImage(context.Background(), images, "fleetly-local/a:b")
	if !errors.Is(err, daemonDown) {
		t.Fatalf("error = %v, want daemonDown passthrough", err)
	}
}
