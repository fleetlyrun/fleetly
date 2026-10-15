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

	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statertest"
)

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
	db, _ := statertest.New(t)
	e2 := New(Deps{DB: db, Repos: testRepos(db), Runtime: newFakeRuntime(), Registry: newFakeRegistry(), Logger: discardLogger()}, Options{})
	t.Cleanup(func() { _ = e2.Stop(context.Background()) })
	_, err = e2.RegistryCatalog(context.Background(), tProjectID)
	require.ErrorIs(t, err, ErrRegistryUnavailable)
	assert.Contains(t, err.Error(), "content face")

	_, err = e2.RegistryTags(context.Background(), tProjectID, inScope)
	require.ErrorIs(t, err, ErrRegistryUnavailable)
}
