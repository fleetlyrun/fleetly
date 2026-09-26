//go:build manual

package substrate

// IMPL-T1-2 重点必查项的实机披露面探针（默认不跑；对本地 swarm 建一个
// replicas=0 的临时服务后删除——无容器启动、无镜像拉取）：
//
//	FLEETLY_MANUAL_SWARM=1 go test -tags manual ./internal/substrate -run TestManualRegistryAuthDisclosure -v
//
// 语义结论（源码取证，moby daemon/cluster/services.go + swarmkit
// api/specs.proto ContainerSpec.pull_options.registry_auth field 64）：
// create 携 auth → 写入 service spec 持久化（agent 拉取消费）；update 空
// auth → 复制 current spec 的 PullOptions（不丢）；update 携 auth → 覆盖。
// 本探针钉住披露面：Docker API 的 ContainerSpec 类型不含 PullOptions，
// inspect 原文永不出现 RegistryAuth（凭据不进 inspect/事件/日志；swarm
// raft 为静态加密目录 snap-v3-encrypted / wal-v3-encrypted）。若未来引擎
// 开始回显该字段，本探针即红（凭据泄露回归告警）。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
	mobyclient "github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/build"
)

func TestManualRegistryAuthDisclosure(t *testing.T) {
	if os.Getenv("FLEETLY_MANUAL_SWARM") != "1" {
		t.Skip("set FLEETLY_MANUAL_SWARM=1 to probe the local swarm")
	}
	cli, err := mobyclient.New(mobyclient.FromEnv)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const name = "fleetly-probe-registry-auth"
	zero := uint64(0)
	spec := swarm.ServiceSpec{
		Annotations: swarm.Annotations{Name: name},
		TaskTemplate: swarm.TaskSpec{
			ContainerSpec: &swarm.ContainerSpec{Image: "localhost:5999/private/probe:1"},
			RestartPolicy: &swarm.RestartPolicy{Condition: swarm.RestartPolicyConditionNone},
		},
		Mode: swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &zero}},
	}
	removeIfPresent := func() {
		if res, err := cli.ServiceInspect(ctx, name, mobyclient.ServiceInspectOptions{}); err == nil {
			if _, err := cli.ServiceRemove(ctx, res.Service.ID, mobyclient.ServiceRemoveOptions{}); err != nil {
				t.Fatalf("cleanup remove: %v", err)
			}
		}
	}
	removeIfPresent()
	t.Cleanup(removeIfPresent)

	authBlob, err := encodedRegistryAuth("localhost:5999", build.RegistryCredentials{User: "probe-user", Password: "fleetly-probe-pass"})
	if err != nil {
		t.Fatalf("encode auth: %v", err)
	}
	if _, err := cli.ServiceCreate(ctx, mobyclient.ServiceCreateOptions{Spec: spec, EncodedRegistryAuth: authBlob}); err != nil {
		t.Fatalf("service create: %v", err)
	}
	assertNoLeak := func(stage string) {
		t.Helper()
		res, err := cli.ServiceInspect(ctx, name, mobyclient.ServiceInspectOptions{})
		if err != nil {
			t.Fatalf("%s: inspect: %v", stage, err)
		}
		raw := string(res.Raw)
		if strings.Contains(raw, "RegistryAuth") || strings.Contains(raw, "PullOptions") || strings.Contains(raw, authBlob) {
			t.Fatalf("%s: inspect response exposes registry auth material (raw: %s)", stage, raw)
		}
	}
	assertNoLeak("create")

	// update 不带 auth：语义上保留既有 PullOptions（凭据不丢）；披露面不变。
	cur, err := cli.ServiceInspect(ctx, name, mobyclient.ServiceInspectOptions{})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	spec.Annotations.Labels = map[string]string{"fleetly.probe": "update-without-auth"}
	if _, err := cli.ServiceUpdate(ctx, cur.Service.ID, mobyclient.ServiceUpdateOptions{
		Version: cur.Service.Version, Spec: spec,
	}); err != nil {
		t.Fatalf("service update (no auth): %v", err)
	}
	assertNoLeak("update-without-auth")

	// update 携新 auth：覆盖；披露面不变。
	newBlob, err := encodedRegistryAuth("localhost:5999", build.RegistryCredentials{User: "probe-user-2", Password: "fleetly-probe-pass-2"})
	if err != nil {
		t.Fatalf("encode auth 2: %v", err)
	}
	cur, err = cli.ServiceInspect(ctx, name, mobyclient.ServiceInspectOptions{})
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}
	spec.Annotations.Labels = map[string]string{"fleetly.probe": "update-with-auth"}
	if _, err := cli.ServiceUpdate(ctx, cur.Service.ID, mobyclient.ServiceUpdateOptions{
		Version: cur.Service.Version, Spec: spec, EncodedRegistryAuth: newBlob,
	}); err != nil {
		t.Fatalf("service update (new auth): %v", err)
	}
	assertNoLeak("update-with-auth")
	t.Logf("create/update with and without auth all succeeded; inspect never exposes PullOptions/RegistryAuth")
}
