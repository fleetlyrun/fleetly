package engine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/statetest"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
)

// putSecret 落一条 Secret（age 信封）。
func putSecret(t *testing.T, e *Engine, projectID, name string, value []byte) {
	t.Helper()
	ct, err := e.cipher.Seal(value)
	require.NoError(t, err)
	require.NoError(t, e.secrets.Upsert(context.Background(), e.db.Runner(), &secret.Secret{
		ID: "01JD0SEC0000000000000000" + name[:1], ProjectID: projectID,
		Name: name, Ciphertext: ct, Fingerprint: material.Fingerprint(value),
	}))
}

// 材料装配：secret_refs → SecretFiles；registry Secret → RegistryAuth；
// 引用不存在的 Secret → 精确错误。
func TestResolveMaterials(t *testing.T) {
	db, _ := statetest.New(t)
	cipher, err := material.LoadCipher(t.TempDir())
	require.NoError(t, err)
	rt := newFakeRuntime()
	e := New(Deps{DB: db, Runtime: rt, Cipher: cipher, Logger: discardLogger()}, Options{})
	ctx := context.Background()

	const ghcrSpec = `{"schema_version":1,"app":{"id":"` + tAppID + `","project":"` + tProjectID + `"},` +
		`"source":{"image":{"ref":"ghcr.io/acme/web:1"}},"processes":[` +
		`{"name":"web","image":"ghcr.io/acme/web:1","replicas":1,"secret_refs":["api-token"]}]}`
	spec, err := e.loadSpec(freezeSpec(t, e, 1, ghcrSpec))
	require.NoError(t, err)

	putSecret(t, e, tProjectID, "api-token", []byte("tok-123"))
	cred, _ := json.Marshal(registryCredentialJSON{Server: "ghcr.io", Username: "ci", Secret: "pat-x"}) //nolint:gosec // 测试夹具样本值
	putSecret(t, e, tProjectID, "registry:ghcr.io", cred)

	m, err := e.resolveMaterials(ctx, spec, tProjectID)
	require.NoError(t, err)
	assert.Equal(t, []byte("tok-123"), m.SecretFiles["api-token"])
	require.NotNil(t, m.RegistryAuth["ghcr.io"])
	assert.Equal(t, "ci", m.RegistryAuth["ghcr.io"].Username)
	assert.Equal(t, "pat-x", m.RegistryAuth["ghcr.io"].Secret)

	// 无凭证主机（docker.io）：匿名拉取（无 RegistryAuth 条目）。
	assert.NotContains(t, m.RegistryAuth, "docker.io")
}

func TestResolveMaterialsMissingSecretFails(t *testing.T) {
	db, _ := statetest.New(t)
	cipher, _ := material.LoadCipher(t.TempDir())
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Cipher: cipher, Logger: discardLogger()}, Options{})
	spec, err := e.loadSpec(freezeSpec(t, e, 2, `{"schema_version":1,`+
		`"app":{"id":"`+tAppID+`","project":"`+tProjectID+`"},`+
		`"source":{"image":{"ref":"nginx:1"}},"processes":[`+
		`{"name":"web","image":"nginx:1","replicas":1,"secret_refs":["nope"]}]}`))
	require.NoError(t, err)
	_, err = e.resolveMaterials(context.Background(), spec, tProjectID)
	assert.ErrorContains(t, err, `secret "nope"`)
}

// 卷钉住：首次挂载锚定首个可用节点（不可变）；投影合并调度约束。
func TestVolumePinning(t *testing.T) {
	db, _ := statetest.New(t)
	rt := newFakeRuntime()
	e := New(Deps{DB: db, Runtime: rt, Logger: discardLogger()}, Options{})
	ctx := context.Background()
	require.NoError(t, e.volumes.Create(ctx, db.Runner(), &volume.Volume{
		ID: "01JD0VOL00000000000000000", ProjectID: tProjectID, Name: "data",
	}))

	ws := []capability.Workload{{
		ID: tAppID + "-web", Process: "web", Image: "nginx:1",
		Volumes: []capability.VolumeMount{{VolumeID: "data", Target: "/var/lib/data"}},
	}}
	require.NoError(t, e.pinVolumes(ctx, ws, tProjectID))
	e.applyVolumePinning(ctx, ws, tProjectID)

	row, err := e.volumes.GetByName(ctx, db.Runner(), tProjectID, "data")
	require.NoError(t, err)
	assert.Equal(t, "01JD0NODE00000000000000000", row.PinnedNodeID, "pinned to the first available node")
	require.Len(t, ws[0].Placement.NodeIDs, 1)
	assert.Equal(t, "01JD0NODE00000000000000000", ws[0].Placement.NodeIDs[0])

	// 二次解析：锚不变（节点 ID 永不复用/不改锚）。
	rt.mu.Lock()
	rt.endpoints = nil
	rt.mu.Unlock()
	require.NoError(t, e.pinVolumes(ctx, ws, tProjectID))
	row, _ = e.volumes.GetByName(ctx, db.Runner(), tProjectID, "data")
	assert.Equal(t, "01JD0NODE00000000000000000", row.PinnedNodeID)
}
