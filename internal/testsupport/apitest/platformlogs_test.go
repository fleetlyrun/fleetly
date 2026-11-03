package apitest_test

// StreamPlatformLogs 验收（ADR-0091）：
//   - 授权矩阵：非平台管理员（他队 builtin-admin、default Team 的
//     builtin-admin、member）一律 403 且门先于镜像读取（删掉镜像目录仍
//     403——403 before 404/503，调用方无法用状态差异探测存储配置；
//     metrics_authz 同款纪律）；
//   - 端到端锚：logger 打标记者行 → tee 镜像落盘 → RPC text 过滤取回
//     （msg/level/原始行三面齐全）——控制面排障「打日志 → CLI 读回」闭环；
//   - 镜像缺失 = E_LOG_STORE_UNAVAILABLE（与"无日志行"分立的诚实失败）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	systemv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/system/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/identity"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/apperrstatus"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// TestStreamPlatformLogsRequiresPlatformAdmin 钉平台门：daemon 日志含全平台
// 请求行与内部错误，跨队可见面必须平台管理员（ListPlatformBackups 同口径）。
func TestStreamPlatformLogsRequiresPlatformAdmin(t *testing.T) {
	h := apitest.New(t)
	platform := systemv1.NewPlatformServiceClient(h.Conn)

	// 三类非平台身份（metrics_authz 同款铸造链，全程走产品面）。
	_, teamAdmin := teamAdminSession(t, h, "acme", "acme-admin", "acme-admin-pass")
	createUserAs(t, h, "default-admin", identity.DefaultTeamID, identity.RoleAdminID, "default-admin-pass")
	defaultAdmin, _ := loginSession(t, h, "default-admin", "default-admin-pass")
	createUserAs(t, h, "default-member", identity.DefaultTeamID, identity.RoleMemberID, "default-member-pass")
	member, _ := loginSession(t, h, "default-member", "default-member-pass")

	// 镜像目录整体移除：非管理员仍必须 403 而非镜像缺失的
	// E_LOG_STORE_UNAVAILABLE——门先于读取，状态差异不可探测。
	require.NoError(t, os.RemoveAll(filepath.Join(h.DataRoot, "logs")))

	// 流式面错误在 Recv 浮现（服务端流 handler 的错误走 trailers）——
	// 统一收流取首个 Recv 错误做门断言。
	call := func(ctx context.Context) error {
		stream, err := platform.StreamPlatformLogs(ctx, &systemv1.StreamPlatformLogsRequest{})
		if err != nil {
			return err
		}
		_, rerr := stream.Recv()
		return rerr
	}
	cases := []struct {
		name string
		ctx  context.Context
	}{
		{"other team builtin-admin", teamAdmin},
		{"default team builtin-admin", defaultAdmin},
		{"default team member", member},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantCode(t, call(tc.ctx), codes.PermissionDenied, "E_FORBIDDEN")
		})
	}
}

// TestStreamPlatformLogsEndToEndMarker 控制面日志闭环：标记者行经 harness
// logger（tee 主径 Discard）落镜像 → 平台 owner 经 RPC text 过滤取回。
// 镜像写入异步：轮询 RPC 直到标记者可见（deadline 兜底）。
func TestStreamPlatformLogsEndToEndMarker(t *testing.T) {
	h := apitest.New(t)
	platform := systemv1.NewPlatformServiceClient(h.Conn)

	// 两行入镜像：标记者 + 干扰行（text 过滤只取前者）。
	h.Log.Error("platformlogs e2e marker", "method", "/fleetly.system.v1.PlatformService/ListPlatformBackups", "error_id", "err-e2e-marker")
	h.Log.Info("unrelated noise line")

	deadline := time.Now().Add(5 * time.Second)
	for {
		stream, err := platform.StreamPlatformLogs(h.Ctx(), &systemv1.StreamPlatformLogsRequest{Text: "e2e marker"})
		require.NoError(t, err)
		var hits []*systemv1.StreamPlatformLogsResponse
		for {
			fr, rerr := stream.Recv()
			if rerr != nil {
				break // io.EOF：收集完毕（错误面由空结果重试兜住）
			}
			hits = append(hits, fr)
		}
		if len(hits) == 1 {
			assert.Equal(t, "platformlogs e2e marker", hits[0].GetMsg())
			assert.Equal(t, "error", hits[0].GetLevel())
			assert.NotEmpty(t, hits[0].GetTime())
			assert.Contains(t, string(hits[0].GetLine()), "err-e2e-marker", "raw JSON line must keep full attrs (error_id join key)")
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("marker line never reached the mirror (hits=%d)", len(hits))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestStreamPlatformLogsNoMirror 镜像缺失的诚实失败：镜像目录被移除后
// 平台 owner 收到 E_LOG_STORE_UNAVAILABLE（不是空流——空流会伪装成
// 「控制面没日志」，掩盖配置漂移）。
func TestStreamPlatformLogsNoMirror(t *testing.T) {
	h := apitest.New(t)
	platform := systemv1.NewPlatformServiceClient(h.Conn)
	require.NoError(t, os.RemoveAll(filepath.Join(h.DataRoot, "logs")))

	// 流式 RPC 的服务端错误在首个 Recv 浮现（headers 时点 handler 尚未
	// return）——空流成功形态（EOF + 零帧）恰是本测试要排除的伪装。
	stream, err := platform.StreamPlatformLogs(h.Ctx(), &systemv1.StreamPlatformLogsRequest{})
	require.NoError(t, err)
	_, rerr := stream.Recv()
	require.Error(t, rerr, "a missing mirror must fail honestly, not stream an empty success")
	env, ok := apperrstatus.FromError(rerr)
	require.True(t, ok)
	assert.Equal(t, "E_LOG_STORE_UNAVAILABLE", env.Code())
}
