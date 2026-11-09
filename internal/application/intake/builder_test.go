package intake

// executeBuild 家族白盒（P5 wave-5 拆包随域迁入，通则⑥——断言面零漂移；
// engine 侧 builder_test/buildroute_test/buildinputs_test 的应用模块行为面，
// 经端口双底与白盒 seeding 驱动）。

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	specir "github.com/fleetlyrun/fleetly/internal/domain/shared/spec"
)

// D-4 防御纵深：executeBuild 无输入登记（正常路径被拾取前置检拦截，此
// 分支不可达；到达即输入丢失）→ 一跳终态 cancelled，不回 queued 弹跳。
func TestExecuteBuildWithoutInputGoesTerminal(t *testing.T) {
	fb := &fakeBuilder{digest: "sha256:x"}
	fx := newFixture(t, func(d *Deps) { d.Builders = map[string]capability.Builder{specir.BuilderDockerfile: fb} })
	ctx := context.Background()
	b := &build.Build{
		ID: "01JD0BUILD00000000000000B8", AppID: tAppID,
		RevisionID: "01JD0REV0000000000000000B8", State: build.StateBuilding,
	}
	require.NoError(t, fx.repos.Builds.Create(ctx, b))

	fx.in.executeBuild(b) // 同步直调：无登记路径
	got, err := fx.repos.Builds.Get(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, build.StateCancelled, got.State, "input-less build must land terminal, not bounce to queued")
	assert.Contains(t, got.Error, "build input lost")
	assert.True(t, got.State.Terminal())
	assert.Empty(t, fb.snapshot(), "the builder must never run without registered input")
}

// input 路由名不在册（漂移名或未装配 Provider）→ 终态 failed 精确文本
// （不 panic、不回 queued 弹跳）。
func TestExecuteBuildUnwiredBuilderFailsPrecisely(t *testing.T) {
	fbD := &fakeBuilder{digest: "sha256:x"}
	fx := newFixture(t, func(d *Deps) { d.Builders = map[string]capability.Builder{specir.BuilderDockerfile: fbD} })
	ctx := context.Background()
	b := &build.Build{ID: "01JD0B00000000000000000UN", AppID: tAppID,
		RevisionID: "01JD0REV0000000000000000F1", State: build.StateBuilding}
	require.NoError(t, fx.repos.Builds.Create(ctx, b))
	fx.in.build.inputs[b.ID] = capability.BuildRequest{BuildID: b.ID, Builder: "nope", ContextDir: "."}
	fx.in.executeBuild(b)
	got, err := fx.repos.Builds.Get(ctx, b.ID)
	require.NoError(t, err)
	assert.Equal(t, build.StateFailed, got.State)
	assert.Contains(t, got.Error, `builder "nope" is not wired`)
	assert.Contains(t, got.Error, specir.BuilderDockerfile, "the error lists the wired builders")
	assert.Empty(t, fbD.snapshot(), "the wired builders must not be engaged")
}

// 构建输入登记的终态释放（R03-M1，S16-c8）：build.inputs 持有
// BuildRequest——含推送凭证明文（RegistryCredential.Secret）。终态
// （succeeded/failed/expired/cancelled）必须清登记：否则进程生命期内凭证
// 常驻内存，且表只增不清（每次构建一行）。P5 wave-5：释放口随应用模块
// transitBuildRelease 包装（冻结边裁决），断言面经包装直调。
func TestBuildInputsReleasedOnEveryTerminal(t *testing.T) {
	cases := []struct {
		name     string
		from, to build.State
	}{
		{"succeeded", build.StateBuilding, build.StateSucceeded},
		{"failed", build.StateBuilding, build.StateFailed},
		{"expired", build.StateBuilding, build.StateExpired},
		{"cancelled", build.StateQueued, build.StateCancelled},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t, func(d *Deps) {
				d.Builders = map[string]capability.Builder{specir.BuilderDockerfile: &fakeBuilder{digest: "sha256:x"}}
			})
			ctx := context.Background()
			b := &build.Build{
				ID: fmt.Sprintf("01JD0BUILD00000000000000%02d", i), AppID: tAppID,
				RevisionID: "01JD0REV0000000000000000B1", State: tc.from,
			}
			require.NoError(t, fx.repos.Builds.Create(ctx, b))
			// 登记面按真实形态布置：推送凭证是明文敏感值。
			fx.in.build.inputs[b.ID] = capability.BuildRequest{
				BuildID: b.ID, Builder: specir.BuilderDockerfile, ContextDir: "/tmp/ctx",
				PushCred: &capability.RegistryCredential{Server: "registry.test", Username: "u", Secret: "s3cret"},
			}

			_, err := fx.in.transitBuildRelease(ctx, b, []build.State{tc.from}, tc.to, nil)
			require.NoError(t, err)

			fx.in.build.inputsMu.Lock()
			_, present := fx.in.build.inputs[b.ID]
			fx.in.build.inputsMu.Unlock()
			assert.False(t, present,
				"terminal build (%s) must release its input registration: the map carries push credentials and only grows", tc.to)
		})
	}
}
