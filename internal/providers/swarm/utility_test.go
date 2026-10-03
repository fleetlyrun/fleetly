package swarm

// 工具容器执行面（ADR-0039 决策 3/4）的 hermetic 测试：spec 组装、退出码
// 语义、材料名钳制、卷 bind 渲染（经 utilityExec 函数值缝——attach 的
// hijack 深度同 builders push seam 理由）；attachable 断言经 fakeDaemon
// 网络创建流水。真机全链由 dind 演练 e2e 承担。

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// captureSpec 是缝注入的捕获器（stdout/stderr/stdin 直通记录）。
type captureSpec struct {
	got    utilityContainerSpec
	stdout strings.Builder
	stderr strings.Builder
	stdin  string
}

func TestRunUtilitySpecAssembly(t *testing.T) {
	captured := &captureSpec{stdin: "DUMPSTREAM"}
	p := &Provider{utilityExec: func(_ context.Context, spec utilityContainerSpec, stdout, stderr io.Writer, stdin io.Reader) (int, error) {
		captured.got = spec
		_, _ = stdout.Write([]byte("out-frame"))
		_, _ = stderr.Write([]byte("err-frame"))
		buf := make([]byte, 64)
		n, _ := stdin.Read(buf)
		captured.stdin = string(buf[:n])
		return 0, nil
	}}
	err := p.RunUtility(context.Background(), capability.UtilityRequest{
		ID:        "01JDUMMY000000000000000000",
		Namespace: capability.NamespaceRef{Team: "acme", Project: "shop", Database: "01JDB00000000000000000000"},
		Image:     "postgres:17-bookworm",
		Argv:      []string{"pg_dump", "-h", "db-01j8", "--format=custom"},
		Env:       map[string]string{"Z_VAR": "z", "A_VAR": "a"},
		Networks:  []string{"internal", "default"},
		SecretFiles: map[string][]byte{ //nolint:gosec // 测试样本值（非凭据）
			"database-backup-pgpass": []byte("db-x:5432:fleetly:fleetly:pw"),
		},
		Volume: &capability.UtilityVolumeMount{VolumeID: "01JDV00000000000000000000", Target: "/seed"},
		Stdin:  strings.NewReader("DUMPSTREAM"),
	}, &captured.stdout, &captured.stderr)

	require.NoError(t, err)
	spec := captured.got
	assert.Equal(t, "fleetly-utility-01jdummy000000000000000000", spec.name, "carrier name = prefix + sanitized (lowercased) ULID")
	assert.Equal(t, "postgres:17-bookworm", spec.image)
	assert.Equal(t, []string{"pg_dump", "-h", "db-01j8", "--format=custom"}, spec.argv)
	assert.Equal(t, []string{"A_VAR=a", "Z_VAR=z"}, spec.env, "env sorted for deterministic spec")
	assert.Equal(t, []string{"fleetly-net-shop-default", "fleetly-net-shop-internal"}, spec.networks, "carrier names sorted")
	assert.True(t, spec.openStdin)
	require.Len(t, spec.binds, 2)
	assert.Regexp(t, `^[A-Za-z]:?[\\/].*[/\\]fleetly-utility-[^/\\]+[/\\]database-backup-pgpass:/run/secrets/database-backup-pgpass:ro$`,
		spec.binds[0], "material source lives in a private temp dir, mounts read-only at the secrets path")
	assert.Equal(t, "fleetly-vol-01jdv00000000000000000000:/seed:rw", spec.binds[1], "volume bind uses the carrier formula and rw for seeding")
	assert.Equal(t, "out-frame", captured.stdout.String())
	assert.Equal(t, "err-frame", captured.stderr.String())
	assert.Equal(t, "DUMPSTREAM", captured.stdin, "stdin streams through the lifecycle seam")
}

func TestRunUtilityReadOnlyVolume(t *testing.T) {
	var got utilityContainerSpec
	p := &Provider{utilityExec: func(_ context.Context, spec utilityContainerSpec, _, _ io.Writer, _ io.Reader) (int, error) {
		got = spec
		return 0, nil
	}}
	err := p.RunUtility(context.Background(), capability.UtilityRequest{
		ID: "u1", Namespace: capability.NamespaceRef{Project: "p"}, Image: "i", Argv: []string{"x"},
		Networks: []string{"default"},
		Volume:   &capability.UtilityVolumeMount{VolumeID: "v1", Target: "/data", ReadOnly: true},
	}, io.Discard, io.Discard)
	require.NoError(t, err)
	assert.Equal(t, "fleetly-vol-v1:/data:ro", got.binds[0])
}

func TestRunUtilityExitCodeMapsToError(t *testing.T) {
	p := &Provider{utilityExec: func(context.Context, utilityContainerSpec, io.Writer, io.Writer, io.Reader) (int, error) {
		return 3, nil
	}}
	err := p.RunUtility(context.Background(), capability.UtilityRequest{
		ID: "u2", Namespace: capability.NamespaceRef{Project: "p"}, Image: "i", Argv: []string{"x"},
		Networks: []string{"default"},
	}, io.Discard, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 3")
	assert.Contains(t, err.Error(), "u2", "error carries the utility ID for diagnosis")
}

func TestRunUtilityValidation(t *testing.T) {
	p := &Provider{}
	base := capability.UtilityRequest{ID: "u", Image: "i", Argv: []string{"x"}, Networks: []string{"default"}}
	for name, mutate := range map[string]func(*capability.UtilityRequest){
		"missing id":       func(r *capability.UtilityRequest) { r.ID = "" },
		"missing image":    func(r *capability.UtilityRequest) { r.Image = "" },
		"missing argv":     func(r *capability.UtilityRequest) { r.Argv = nil },
		"missing networks": func(r *capability.UtilityRequest) { r.Networks = nil },
	} {
		t.Run(name, func(t *testing.T) {
			req := base
			mutate(&req)
			err := p.RunUtility(context.Background(), req, io.Discard, io.Discard)
			assert.ErrorContains(t, err, "are required")
		})
	}
}

// 材料名钳制（bind 拼接输入面——端口不信任调用方，P2 家族同源纪律）。
func TestRunUtilityMaterialNameInjection(t *testing.T) {
	p := &Provider{}
	for _, name := range []string{"", "../escape", "sub/dir", `back\slash`, "C:drive", "/abs"} {
		err := p.RunUtility(context.Background(), capability.UtilityRequest{
			ID: "u", Namespace: capability.NamespaceRef{Project: "p"}, Image: "i", Argv: []string{"x"},
			Networks:    []string{"default"},
			SecretFiles: map[string][]byte{name: []byte("v")}, //nolint:gosec // 测试样本值
		}, io.Discard, io.Discard)
		assert.ErrorContains(t, err, "plain file name", "material name %q must be rejected", name)
	}
}

// attachable 断言（ADR-0039 决策 4）：新建项目网络必须 Attachable（工具
// 容器附着面），驱动 overlay 与平台标签不变。
func TestEnsureNetworksAttachable(t *testing.T) {
	f := newFakeDaemon()
	cli := f.newClient(t)
	p := &Provider{cli: cli}

	ns := capability.NamespaceRef{Team: "acme", Project: "shop", App: "web"}
	ws := []capability.Workload{{Networks: []string{"default"}}}
	require.NoError(t, p.ensureNetworks(context.Background(), ns, ws))

	creates := f.networkCreates()
	require.Len(t, creates, 1)
	assert.Equal(t, "fleetly-net-shop-default", creates[0].name)
	assert.Equal(t, "overlay", creates[0].opts.Driver)
	assert.True(t, creates[0].opts.Attachable, "project networks must be attachable for utility containers (ADR-0039)")
	assert.Equal(t, "shop", creates[0].opts.Labels[labelNetProject])
	assert.Equal(t, "default", creates[0].opts.Labels[labelNetPlatform])
}
