package engine

// registryContentFor 面缺席族单测（P0-7.2）：Registry 未装配与 provider 无
// content 子面两形态都以 ErrRegistryUnavailable 哨兵上抛（API 层据此落
// E_REGISTRY_UNAVAILABLE 503）——不再裸 fmt.Errorf 落 E_INTERNAL 500。

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// fakeRegistryContent 是带 content 子面的假仓（R03-M13 用例面）：记录
// 到达上游的 repository——注入串若穿过归属校验，lastRepo 即暴露。
type fakeRegistryContent struct {
	*fakeRegistry
	catalog  []string
	tags     []capability.RegistryTag
	lastRepo string
}

func (f *fakeRegistryContent) Catalog(context.Context, capability.RegistryEndpoint) ([]string, error) {
	return f.catalog, nil
}

func (f *fakeRegistryContent) Tags(_ context.Context, _ capability.RegistryEndpoint, repository string) ([]capability.RegistryTag, error) {
	f.lastRepo = repository
	return f.tags, nil
}

func TestRegistryContentForUnconfiguredFamily(t *testing.T) {
	// 形态一：Registry 未装配（newTestEngine 缺省 Registry 为 nil）。
	e, _, _ := newTestEngine(t)
	_, err := e.RegistryCatalog(context.Background(), tProjectID)
	require.ErrorIs(t, err, ErrRegistryUnavailable)
	assert.Contains(t, err.Error(), "registry.addr", "message must carry the actionable config hint")

	inScope := strings.ToLower(tProjectID) + "/web"
	_, err = e.RegistryTags(context.Background(), tProjectID, inScope)
	require.ErrorIs(t, err, ErrRegistryUnavailable)

	// 形态二：Registry 装配但无 content 子面（fakeRegistry 未实现
	// RegistryContent——FacesOf(...).Content 为 nil）。
	db, _ := statetest.New(t)
	e2 := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)), Runtime: newFakeRuntime(), Registry: newFakeRegistry(), Logger: discardLogger()}, Options{})
	t.Cleanup(func() { _ = e2.Stop(context.Background()) })
	_, err = e2.RegistryCatalog(context.Background(), tProjectID)
	require.ErrorIs(t, err, ErrRegistryUnavailable)
	assert.Contains(t, err.Error(), "content face")

	_, err = e2.RegistryTags(context.Background(), tProjectID, inScope)
	require.ErrorIs(t, err, ErrRegistryUnavailable)
}

// newRegistryContentEngine 造装配了 content 子面的引擎（无 DB 依赖面——
// 归属校验先于内容面调用，正例需要假仓承接）。
func newRegistryContentEngine(t *testing.T) (*Engine, *fakeRegistryContent) {
	t.Helper()
	db, _ := statetest.New(t)
	reg := &fakeRegistryContent{fakeRegistry: newFakeRegistry()}
	e := New(Deps{DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)),
		Runtime: newFakeRuntime(), Registry: reg, Logger: discardLogger()}, Options{})
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return e, reg
}

// R03-M13：repository 必须收敛为平台仓名白名单 <lower(projectID)>/<name>
// （name = [a-z0-9._-]+，禁 .. 段、编码/查询/片段元字符、大小写混杂）；
// 非法形态在受理期 E_INVALID_ARGUMENT（engine 哨兵），绝不透传到上游。
func TestRegistryTagsRepositoryWhitelist(t *testing.T) {
	e, reg := newRegistryContentEngine(t)
	ctx := context.Background()
	prefix := strings.ToLower(tProjectID)
	valid := prefix + "/web"
	reg.tags = []capability.RegistryTag{{Name: "r1", Digest: "sha256:aa", SizeBytes: 1, PushedAt: "2026-10-19T00:00:00Z"}}

	tags, err := e.RegistryTags(ctx, tProjectID, valid)
	require.NoError(t, err, "a normal platform repository name must stay accepted")
	require.Len(t, tags, 1)
	assert.Equal(t, valid, reg.lastRepo, "the accepted repository must reach the content face unchanged")
	assert.Equal(t, "r1", tags[0].Name)

	// 负例：路径段注入与白名单外字符。每个都必须在上游调用前被拒。
	for _, repo := range []string{
		prefix + "/../other-project/app", // 段穿越
		prefix + "/%2e%2e",               // 编码穿越
		prefix + "/..",                   // 裸父段
		prefix + "/.",                    // 裸当前段
		prefix + "/?x=1",                 // 查询串注入
		prefix + "/#frag",                // 片段注入
		prefix + "/",                     // 空段
		prefix + "//web",                 // 空中间段
		prefix + "/web/v2",               // 多级仓名（超出单段）
		prefix + "/Web",                  // 大小写混杂（双段小写纪律）
		strings.ToUpper(prefix) + "/web", // 项目段大写
		"../other/web",                   // 前纲外的穿越
		"",
	} {
		reg.lastRepo = ""
		_, err := e.RegistryTags(ctx, tProjectID, repo)
		require.Error(t, err, "repository %q must be rejected", repo)
		assert.ErrorIs(t, err, ErrInvalidRegistryRepository, "repository %q must be an invalid-argument class error", repo)
		assert.Empty(t, reg.lastRepo, "repository %q must never reach the content face", repo)
	}

	// 形态合法但不属于本项目：维持 not found（不泄漏跨项目存在性），
	// 且同样不触达上游。
	reg.lastRepo = ""
	_, err = e.RegistryTags(ctx, tProjectID, "01jd0other0000000000000000/web")
	require.ErrorIs(t, err, state.ErrNotFound)
	assert.Empty(t, reg.lastRepo)
}

// R03-M13 正例面：catalog 按项目前纲过滤后仍只含本项目仓名；tag 请求的
// 归一化不改变既有合法仓名字符串（零 diff 面）。
func TestRegistryCatalogKeepsOnlyProjectScope(t *testing.T) {
	e, reg := newRegistryContentEngine(t)
	prefix := strings.ToLower(tProjectID)
	reg.catalog = []string{prefix + "/web", prefix + "/api", "01jd0other0000000000000000/web"}
	repos, err := e.RegistryCatalog(context.Background(), tProjectID)
	require.NoError(t, err)
	assert.Equal(t, []string{prefix + "/web", prefix + "/api"}, repos)
}

// 项目段白名单的 entityid 正形正例（ff0b8681 回归锚；评审 P3 跟进——该修
// 复当时只由 cmd golden 捕获）：项目 ID 的实体形态是 `<name>-<seq>`（如
// `regcat-1`），白名单初版把项目段写死为 ULID 形 `[a-z0-9]+` 而误拒。正例
// 补齐后，entityid 正形与多连字符名段在 engine 层有直接承载；负例钉住段
// 形边界（首段空/尾连字符/双连字符不构成段）。
func TestRegistryTagsRepositoryEntityIDProjectSegment(t *testing.T) {
	e, reg := newRegistryContentEngine(t)
	ctx := context.Background()
	projectID := "regcat-1"
	valid := projectID + "/web"
	reg.tags = []capability.RegistryTag{{Name: "r1", Digest: "sha256:aa", SizeBytes: 1, PushedAt: "2026-10-19T00:00:00Z"}}

	tags, err := e.RegistryTags(ctx, projectID, valid)
	require.NoError(t, err, "an entityid-shaped project segment must stay accepted")
	assert.Equal(t, valid, reg.lastRepo, "the accepted repository must reach the content face unchanged")
	assert.Equal(t, "r1", tags[0].Name)

	// 多连字符名段同样合法（`<name>-<seq>` 的 name 自身可含连字符）。
	multiDash := projectID + "/my-app-web"
	reg.lastRepo = ""
	_, err = e.RegistryTags(ctx, projectID, multiDash)
	require.NoError(t, err)
	assert.Equal(t, multiDash, reg.lastRepo)

	// 段形边界：项目段尾连字符/双连字符不构成段（`-` 单独不是段分隔），
	// 白名单拒绝且不触达上游。
	for _, repo := range []string{
		"regcat-/" + "web", // 项目段尾连字符
		"regcat--1/web",    // 空名段（双连字符）
		"-regcat-1/web",    // 首连字符
	} {
		reg.lastRepo = ""
		_, err := e.RegistryTags(ctx, projectID, repo)
		require.Error(t, err, "repository %q must be rejected", repo)
		assert.Empty(t, reg.lastRepo, "repository %q must never reach the content face", repo)
	}
}
