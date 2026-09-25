package api

// 自动扩缩策略 API 面（W5-S1，D-V3W5-2）：scope 门（读 read / 写 deploy）、
// 项目角色门（requireAppAccess 第 2 门——机具令牌全库等价）、请求形状校验
//（max ≥ min / 至少一维目标）、CRUD 往返与 404 语义。

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	testsupport "github.com/fleetlyrun/fleetly/internal/testsupport"
)

// scalingReq 是 Set 请求便捷构造（缺省 min1/max4/cpu60/mem70/cd180）。
func scalingReq(app string, mutate func(r *serverv1.SetScalingPolicyRequest)) *serverv1.SetScalingPolicyRequest {
	r := &serverv1.SetScalingPolicyRequest{
		Name:            app,
		Service:         "web",
		MinReplicas:     1,
		MaxReplicas:     4,
		TargetCpuPct:    60,
		TargetMemPct:    70,
		CooldownSeconds: 180,
	}
	if mutate != nil {
		mutate(r)
	}
	return r
}

func TestScalingPolicyScopeGates(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	app, err := testsupport.SeedAppE(t, env.st, "scalingapp")
	if err != nil {
		t.Fatalf("seed app: %v", err)
	}
	_ = app

	// 读 scope 调写面：PermissionDenied（scope 门先行）。
	_, err = serverv1.NewAppsServiceClient(env.conn).SetScalingPolicy(authCtx(ctx, env.readTok),
		scalingReq("scalingapp", nil))
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read token set policy = %v, want PermissionDenied", err)
	}
	// 读 scope 调删面：同拒。
	_, err = serverv1.NewAppsServiceClient(env.conn).RemoveScalingPolicy(authCtx(ctx, env.readTok),
		&serverv1.RemoveScalingPolicyRequest{Name: "scalingapp", Service: "web"})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("read token remove policy = %v, want PermissionDenied", err)
	}

	// deploy scope 写面放行（机具令牌 = 全库 admin 等价的第 2 门语义）。
	if _, err := serverv1.NewAppsServiceClient(env.conn).SetScalingPolicy(authCtx(ctx, env.depTok),
		scalingReq("scalingapp", nil)); err != nil {
		t.Fatalf("deploy token set policy: %v", err)
	}
	// 读面（read scope）放行。
	got, err := serverv1.NewAppsServiceClient(env.conn).GetScalingPolicy(authCtx(ctx, env.readTok),
		&serverv1.GetScalingPolicyRequest{Name: "scalingapp", Service: "web"})
	if err != nil {
		t.Fatalf("read token get policy: %v", err)
	}
	if got.GetMinReplicas() != 1 || got.GetMaxReplicas() != 4 || got.GetTargetCpuPct() != 60 || got.GetCooldownSeconds() != 180 {
		t.Fatalf("policy view = %+v", got)
	}
	// deploy scope 删面放行。
	if _, err := serverv1.NewAppsServiceClient(env.conn).RemoveScalingPolicy(authCtx(ctx, env.depTok),
		&serverv1.RemoveScalingPolicyRequest{Name: "scalingapp", Service: "web"}); err != nil {
		t.Fatalf("deploy token remove policy: %v", err)
	}
	// 删除后读面 404。
	_, err = serverv1.NewAppsServiceClient(env.conn).GetScalingPolicy(authCtx(ctx, env.readTok),
		&serverv1.GetScalingPolicyRequest{Name: "scalingapp", Service: "web"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("get removed policy = %v, want NotFound", err)
	}
}

func TestScalingPolicyRequestValidation(t *testing.T) {
	env := newTestEnv(t)
	ctx := authCtx(context.Background(), env.depTok)
	client := serverv1.NewAppsServiceClient(env.conn)
	if _, err := testsupport.SeedAppE(t, env.st, "scalingval"); err != nil {
		t.Fatalf("seed app: %v", err)
	}

	cases := []struct {
		name string
		req  *serverv1.SetScalingPolicyRequest
	}{
		{"max below min", scalingReq("scalingval", func(r *serverv1.SetScalingPolicyRequest) { r.MaxReplicas = 0 })},
		{"no target at all", scalingReq("scalingval", func(r *serverv1.SetScalingPolicyRequest) { r.TargetCpuPct = 0; r.TargetMemPct = 0 })},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := client.SetScalingPolicy(ctx, tc.req); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("set = %v, want InvalidArgument", err)
			}
		})
	}
}

func TestScalingPolicyMissingAppAndService(t *testing.T) {
	env := newTestEnv(t)
	ctx := authCtx(context.Background(), env.admTok)
	client := serverv1.NewAppsServiceClient(env.conn)

	// app 不存在：404（不泄漏不可见资源的存在性——同一 404 形态）。
	if _, err := client.GetScalingPolicy(ctx, &serverv1.GetScalingPolicyRequest{Name: "ghost", Service: "web"}); status.Code(err) != codes.NotFound {
		t.Fatalf("get policy of missing app = %v, want NotFound", err)
	}
	if _, err := client.SetScalingPolicy(ctx, scalingReq("ghost", nil)); status.Code(err) != codes.NotFound {
		t.Fatalf("set policy of missing app = %v, want NotFound", err)
	}
	if _, err := client.RemoveScalingPolicy(ctx, &serverv1.RemoveScalingPolicyRequest{Name: "ghost", Service: "web"}); status.Code(err) != codes.NotFound {
		t.Fatalf("remove policy of missing app = %v, want NotFound", err)
	}

	// app 在、策略未设置：get/remove 均 404（未配置即无策略语义）。
	if _, err := testsupport.SeedAppE(t, env.st, "scalingnone"); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	if _, err := client.GetScalingPolicy(ctx, &serverv1.GetScalingPolicyRequest{Name: "scalingnone", Service: "web"}); status.Code(err) != codes.NotFound {
		t.Fatalf("get unset policy = %v, want NotFound", err)
	}
	if _, err := client.RemoveScalingPolicy(ctx, &serverv1.RemoveScalingPolicyRequest{Name: "scalingnone", Service: "web"}); status.Code(err) != codes.NotFound {
		t.Fatalf("remove unset policy = %v, want NotFound", err)
	}
}
