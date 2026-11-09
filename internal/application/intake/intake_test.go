package intake

// 卫星白盒测试共用夹具（P5 wave-5 拆包随域迁入，通则⑥——断言面零漂移；
// manageds/browses 卫星同款：engine 侧同源夹具的隔离副本，测试天然跨层
// 可引 infrastructure 持久化构造面）。transitBuild 端口绑最小四件一双底
//（Transit+Get，无事实面——四件一完备性由 engine transition_test 承载，
// 本包测卫星行为面）。

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/app"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence"
	apprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/app"
	buildrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/build"
	deploymentrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/deployment"
	githubapprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/githubapp"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	revisionrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/revision"
	secretrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/secret"
	sourcerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// 测试夹具锚（engine 侧同源隔离副本）。
const (
	tProjectID = "01JD0PROJ000000000000000001"
	tAppID     = "01JD0APP0000000000000000001"
)

// testBuildTimeout 是白盒直调 executeBuild 的超时锚（秒级——测试不真等）。
const testBuildTimeout = 30 * time.Second

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// fakeBuilder 是构建端口假底座（engine 侧同源隔离副本；可编程 digest/失败/
// 日志帧）。快照面（snapshot）供分派断言。
type fakeBuilder struct {
	mu     sync.Mutex
	digest string
	fail   bool
	logs   []string
	calls  []capability.BuildRequest
}

func (f *fakeBuilder) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake", Capability: capability.KindBuilder, Version: "test"}
}
func (f *fakeBuilder) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}
func (f *fakeBuilder) Build(ctx context.Context, req capability.BuildRequest, w capability.LogWriter) (capability.BuildResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	f.mu.Unlock()
	for _, line := range f.logs {
		if err := w.WriteLog(ctx, capability.LogFrame{WorkloadID: req.BuildID, Line: []byte(line)}); err != nil {
			return capability.BuildResult{}, err
		}
	}
	if f.fail {
		return capability.BuildResult{}, assert.AnError
	}
	return capability.BuildResult{Digest: f.digest}, nil
}

func (f *fakeBuilder) snapshot() []capability.BuildRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.BuildRequest, len(f.calls))
	copy(out, f.calls)
	return out
}

// intakeRepos 构建卫星测试的最小 repo 集（按实测消费面填充；nil 字段不在
// 断言路径——transact.Stores 是聚合目录，缺席即不可达）。
func intakeRepos(db state.DB) transact.Stores {
	clock := db.Clock()
	run := db.Runner()
	return transact.Stores{
		NextSeq: func(ctx context.Context, table string) (int64, error) {
			return persistence.NextSeq(ctx, run, table)
		},
		Projects:      projectrepo.New(clock, run),
		Apps:          apprepo.New(clock, run),
		Revisions:     revisionrepo.New(clock, run),
		Deployments:   deploymentrepo.New(clock, run),
		Builds:        buildrepo.New(clock, run),
		Secrets:       secretrepo.New(clock, run),
		SourceUploads: sourcerepo.New(clock, run),
		GitHubApps:    githubapprepo.New(clock, run),
	}
}

// stubTransitBuild 是 transitBuild 端口的最小双底：CAS Transit + 刷新 Get
// （无事实面——四件一完备性在 engine transition_test；本包断言状态面与
// 卫星侧终态副作用）。
func stubTransitBuild(repos transact.Stores) func(context.Context, *build.Build, []build.State, build.State, func(*build.Build)) (*build.Build, error) {
	return func(ctx context.Context, b *build.Build, from []build.State, to build.State, mut func(*build.Build)) (*build.Build, error) {
		if err := repos.Builds.Transit(ctx, b.ID, from, to, mut); err != nil {
			return nil, err
		}
		return repos.Builds.Get(ctx, b.ID)
	}
}

// fixture 是卫星白盒夹具：内存库 + 最小 repo 集 + 默认端口绑定（端口缺省
// 为精确失败闭包——未接线即用例缺陷，不静默通过）。
type fixture struct {
	in    *Intake
	db    *persistence.DB
	repos transact.Stores
}

// newFixture 建卫星夹具（mutate 可覆写 Deps——builders/logging/registry 等
// 按用例注入）。
func newFixture(t *testing.T, mutate func(*Deps)) *fixture {
	t.Helper()
	db, _ := statetest.New(t)
	repos := intakeRepos(db)
	root := t.TempDir() // 单次定格：闭包逐拍读同一根（t.TempDir 每次调用返回新目录）
	deps := Deps{
		Repos:            repos,
		Log:              discardLogger(),
		BuildConcurrency: 2,
		BuildTimeout:     testBuildTimeout,
		DataRoot:         func() string { return root },
		RootCtx:          func() context.Context { return context.Background() },
		Spawn:            func(fn func()) { fn() }, // 同步执行（白盒直调形态）
		TransitBuild:     stubTransitBuild(repos),
		LoadSpec:         func(context.Context, string) (*specv1.AppSpec, error) { return nil, assert.AnError },
		CommitWrite: func(context.Context, transact.Stores, contract.WriteFact) error {
			return assert.AnError
		},
		FailDeployment: func(context.Context, *deployment.Deployment, string) (*deployment.Deployment, error) {
			return nil, assert.AnError
		},
		TransitAndReload: func(context.Context, *deployment.Deployment, []deployment.State, deployment.State, func(*deployment.Deployment)) (*deployment.Deployment, error) {
			return nil, assert.AnError
		},
		EventTeamOfProject: func(context.Context, transact.Stores, string) string { return "" },
		ProjectTeam:        func(context.Context, string) (string, error) { return "default", nil },
		RegistryEndpoint: func(context.Context) (capability.RegistryEndpoint, error) {
			return capability.RegistryEndpoint{}, assert.AnError
		},
		RegistryProjectEndpoints: func() capability.ProjectEndpoints { return nil },
		RegistryEndpointFor: func(context.Context, string) (capability.RegistryEndpoint, error) {
			return capability.RegistryEndpoint{}, assert.AnError
		},
		ManagedEndpoint: func(context.Context) (capability.RegistryEndpoint, error) {
			return capability.RegistryEndpoint{}, assert.AnError
		},
		KickBuild: func() {},
	}
	if mutate != nil {
		mutate(&deps)
	}
	return &fixture{in: New(deps), db: db, repos: repos}
}

// seedProjectApp 播 project/app 夹具行（域盖戳与归属解析的权威表种子）。
func (fx *fixture) seedProjectApp(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	clock := fx.db.Clock()
	require.NoError(t, projectrepo.New(clock, fx.db.Runner()).Create(ctx, &project.Project{ID: tProjectID, Name: "shop", TeamID: "default"}))
	require.NoError(t, apprepo.New(clock, fx.db.Runner()).Create(ctx, &app.App{ID: tAppID, ProjectID: tProjectID, Name: "web"}))
}

// putSecret 落一条 Secret（age 信封；engine 侧同源隔离副本）。
func (fx *fixture) putSecret(t *testing.T, projectID, name string, value []byte) {
	t.Helper()
	ct, err := fx.in.cipher.Seal(value)
	require.NoError(t, err)
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	require.NoError(t, fx.repos.Secrets.Upsert(context.Background(), &secret.Secret{
		ID: "01JD0SEC" + fmt.Sprintf("%018x", uint64(h.Sum32())), ProjectID: projectID,
		Name: name, Ciphertext: ct, Fingerprint: material.Fingerprint(value),
	}))
}
