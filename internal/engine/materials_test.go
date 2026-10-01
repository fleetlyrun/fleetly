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
	spec, err := e.loadSpec(ctx, freezeSpec(t, e, 1, ghcrSpec))
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
	spec, err := e.loadSpec(context.Background(), freezeSpec(t, e, 2, `{"schema_version":1,`+
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
	require.NoError(t, e.applyVolumePinning(ctx, ws, tProjectID))

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

// Q-8 回归：registry Secret 查询的存储错误必须上抛（部署失败带原因），
// 不得静默降级为匿名拉取——私有镜像会死在无诊断的拉取失败上。ErrNotFound
// （无凭证）仍是合法匿名形态（TestResolveMaterials 已覆盖）。
func TestResolveMaterialsRegistryLookupErrorPropagates(t *testing.T) {
	db, _ := statetest.New(t)
	cipher, err := material.LoadCipher(t.TempDir())
	require.NoError(t, err)
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Cipher: cipher, Logger: discardLogger()}, Options{})
	spec, err := e.loadSpec(context.Background(), freezeSpec(t, e, 3, `{"schema_version":1,`+
		`"app":{"id":"`+tAppID+`","project":"`+tProjectID+`"},`+
		`"source":{"image":{"ref":"ghcr.io/acme/web:1"}},"processes":[`+
		`{"name":"web","image":"ghcr.io/acme/web:1","replicas":1}]}`))
	require.NoError(t, err)

	// 存储故障注入：关库（与 authn Q-24 单测同款夹具形态）。
	require.NoError(t, db.Close())
	_, err = e.resolveMaterials(context.Background(), spec, tProjectID)
	require.Error(t, err, "a registry credential lookup failure must fail the deployment, not degrade to anonymous pull")
	assert.ErrorContains(t, err, "registry credential")
	assert.ErrorContains(t, err, "ghcr.io")
}

// Q-8 回归：applyVolumePinning 的 GetByName 错误改硬失败——漏合并钉住 =
// 无钉住调度（卷可能落到别的节点），比部署失败更糟。
func TestApplyVolumePinningLookupErrorFails(t *testing.T) {
	db, _ := statetest.New(t)
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Logger: discardLogger()}, Options{})

	// 引用不存在的卷：GetByName → ErrNotFound（此前被静默 continue）。
	ws := []capability.Workload{{
		ID: tAppID + "-web", Process: "web", Image: "nginx:1",
		Volumes: []capability.VolumeMount{{VolumeID: "ghost", Target: "/var/lib/data"}},
	}}
	err := e.applyVolumePinning(context.Background(), ws, tProjectID)
	require.Error(t, err, "a volume lookup failure must fail instead of scheduling unpinned")
	assert.ErrorContains(t, err, `volume "ghost"`)

	// 存储故障注入：关库后已登记卷同样硬失败（不静默跳过合并）。
	require.NoError(t, e.volumes.Create(context.Background(), db.Runner(), &volume.Volume{
		ID: "01JD0VOL00000000000000001", ProjectID: tProjectID, Name: "data",
		PinnedNodeID: "01JD0NODE00000000000000000",
	}))
	ws2 := []capability.Workload{{
		ID: tAppID + "-web", Process: "web", Image: "nginx:1",
		Volumes: []capability.VolumeMount{{VolumeID: "data", Target: "/var/lib/data"}},
	}}
	require.NoError(t, e.applyVolumePinning(context.Background(), ws2, tProjectID))
	require.NoError(t, db.Close())
	err = e.applyVolumePinning(context.Background(), ws2, tProjectID)
	require.Error(t, err, "a storage failure must fail the merge instead of dropping the pin")
}
