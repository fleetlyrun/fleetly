package engine

// Backup↔Database 跨域集成面（P5 wave-3 拆包留存核测试；P5 wave-4 起
// databases.Step 同款经卫星指针）：恢复挂起的 preseed 门控——挂起中
// databases.Step 不 Ensure（空卷首启 = 数据丢失），backups.Step 预置清
// 挂起后 databases.Step 恢复收敛。域内行为面（调度/执行/保留/Platform
// Backup/双路由/grant 族）随域迁入 backups/browses/databases 卫星包白盒
//（通则⑥）；本文件持跨域序列与 fakeObjectStore/fakeUtility 隔离副本。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/keyring"
	dbrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/database"
	networkrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/network"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	secretrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/secret"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
)

// fakeObjectStore 是 ObjectStore 端口的内存假面（backups 卫星包测试同款
// 的核侧隔离副本——通则⑥；断言面零漂移）。
type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string][]byte{}}
}

// fakeUtility 是 RuntimeUtility 假面（记录请求；可编程 stdout/失败）。
// P5 wave-4：databases 域白盒随域迁出后，跨域序列（本文件）持隔离副本
// （通则⑥——两侧断言面零漂移）。
type fakeUtility struct {
	mu      sync.Mutex
	reqs    []capability.UtilityRequest
	stdins  []string
	stdout  []byte
	execErr error
}

func (f *fakeUtility) RunUtility(_ context.Context, req capability.UtilityRequest, stdout, stderr io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	if req.Input != nil {
		b, _ := io.ReadAll(req.Input.Content)
		f.stdins = append(f.stdins, string(b))
	}
	if f.execErr != nil {
		_, _ = io.WriteString(stderr, "boom-detail")
		return f.execErr
	}
	_, _ = stdout.Write(f.stdout)
	return nil
}

func (f *fakeUtility) requests() []capability.UtilityRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.UtilityRequest, len(f.reqs))
	copy(out, f.reqs)
	return out
}

func (f *fakeUtility) capturedStdins() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.stdins))
	copy(out, f.stdins)
	return out
}

// runtimeWithUtility 组合假 Runtime 与假 Utility（FacesOf 经组合面探测）。
type runtimeWithUtility struct {
	*fakeRuntime
	*fakeUtility
}

func (f *fakeObjectStore) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake", Capability: capability.KindObjectStore, Version: "1"}
}

func (f *fakeObjectStore) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *fakeObjectStore) Put(_ context.Context, key string, r io.Reader) (capability.ObjectInfo, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return capability.ObjectInfo{}, err
	}
	sum := sha256.Sum256(body)
	f.mu.Lock()
	f.objects[key] = body
	f.mu.Unlock()
	return capability.ObjectInfo{Key: key, Size: int64(len(body)), Digest: hex.EncodeToString(sum[:])}, nil
}

func (f *fakeObjectStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	body, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("fake object store: object not found")
	}
	return io.NopCloser(strings.NewReader(string(body))), nil
}

func (f *fakeObjectStore) Stat(_ context.Context, key string) (capability.ObjectInfo, error) {
	f.mu.Lock()
	body, ok := f.objects[key]
	f.mu.Unlock()
	if !ok {
		return capability.ObjectInfo{}, errors.New("fake object store: object not found")
	}
	return capability.ObjectInfo{Key: key, Size: int64(len(body))}, nil
}

func (f *fakeObjectStore) List(_ context.Context, prefix string) ([]capability.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []capability.ObjectInfo
	for k, v := range f.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, capability.ObjectInfo{Key: k, Size: int64(len(v))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeObjectStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	delete(f.objects, key)
	f.mu.Unlock()
	return nil
}

var _ capability.ObjectStore = (*fakeObjectStore)(nil)

// tDatabaseName/tDatabaseID 是数据库夹具的库名与行 ID（database 域白盒
// 随域迁出后的核侧跨域序列隔离常量，通则⑥——与卫星包夹具同源同值）。
const (
	tDatabaseName = "shop"
	tDatabaseID   = "01JD0DB000000000000000000"
)

// newBackupGateFixture 是跨域夹具：databases 域夹具 + ObjectStore 装配
// （backups 卫星经核 Deps 装配持端口——P5 wave-3 拆包形态，两侧环步都在
// 场；库行 running 起步与 newDatabaseUtilityFixture 同款）。
func newBackupGateFixture(t *testing.T, engineName, connectURL string) (*Engine, *fakeRuntime, *fakeUtility, *fakeObjectStore) {
	t.Helper()
	db, clock := statetest.New(t)
	rt := newFakeRuntime()
	ut := &fakeUtility{stdout: []byte("UTILITY-OK")}
	store := newFakeObjectStore()
	cipher, err := keyring.LoadCipher(t.TempDir())
	require.NoError(t, err)
	e := New(Deps{
		DB: db, Stores: testRepos(db), Tx: transact.NewTx(db, testBinder(db)),
		Runtime: runtimeWithUtility{rt, ut}, ObjectStore: store, Cipher: cipher, Logger: discardLogger(),
	}, Options{})
	ctx := context.Background()

	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	require.NoError(t, networkrepo.New(clock, db.Runner()).Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000001", ProjectID: tProjectID, Name: "default",
	}))
	secName := DBCredentialSecretName(tDatabaseName)
	ct, err := cipher.Seal([]byte(connectURL))
	require.NoError(t, err)
	require.NoError(t, secretrepo.New(clock, db.Runner()).Upsert(ctx, &secret.Secret{
		ID: "01JD0SEC000000000000000001", ProjectID: tProjectID, Name: secName,
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(connectURL)),
	}))
	require.NoError(t, dbrepo.New(clock, db.Runner()).Create(ctx, &database.Database{
		ID: tDatabaseID, ProjectID: tProjectID, Name: tDatabaseName,
		Engine: engineName, CredentialsRef: secName,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}))
	require.NoError(t, dbrepo.New(clock, db.Runner()).SetStatus(ctx, tDatabaseID, database.StatusRunning))
	return e, rt, ut, store
}

// seedGateBackup 直接落一颗成功备份行 + 对象（卫星包 seedSucceededBackup
// 的核侧隔离副本——跨域序列的种子面）。
func seedGateBackup(t *testing.T, e *Engine, store *fakeObjectStore, backupID, engineName, payload string) {
	t.Helper()
	ctx := context.Background()
	key := backup.KeyMint(tProjectID, tDatabaseID, backupID, e.clock.Now())
	info, err := store.Put(ctx, key, strings.NewReader(payload))
	require.NoError(t, err)
	require.NoError(t, e.repos.Backups.Create(ctx, &backup.Backup{
		ID: backupID, ProjectID: tProjectID, DatabaseID: tDatabaseID,
		Engine: engineName, RetentionSecs: 604800,
	}))
	require.NoError(t, e.repos.Backups.MarkRunning(ctx, backupID))
	require.NoError(t, e.repos.Backups.FinishSucceeded(ctx, backupID, key, info.Digest, info.Size))
}

// TestRestorePreseedGateKeepsDatabaseStepIdle：预置卷恢复（redis）的
// 门控序列——挂起中 databaseStep 不 Ensure；backups.Step 预置（stdin 落卷
// + 卷钉住）→ 清挂起 → 下一拍 databaseStep 正常收敛首启。
func TestRestorePreseedGateKeepsDatabaseStepIdle(t *testing.T) {
	const url = "redis://:redispw@db-01jd0db000000000000000000:6379/0" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, rt, ut, store := newBackupGateFixture(t, "redis", url)
	ctx := context.Background()

	seedGateBackup(t, e, store, "01JD0BKP000000000000000002", "redis", "REDIS0011-rdb-bytes")
	require.NoError(t, e.repos.Databases.SetRestorePending(ctx, tDatabaseID, "01JD0BKP000000000000000002"))

	// 门控：挂起中 databaseStep 不 Ensure（空卷首启 = 数据丢失）。
	before := len(rt.calls())
	e.databases.Step(ctx)
	assert.Len(t, rt.calls(), before, "preseed-pending database must not be ensured")

	e.backups.Step(ctx)
	reqs := ut.requests()
	require.Len(t, reqs, 1)
	require.NotNil(t, reqs[0].Volume, "preseed mounts the data volume")
	// 载体键 = 卷行 id（ADR-0097：库名公式解析出行，运行时零 name）。
	volRow, verr := e.repos.Volumes.GetByName(ctx, tProjectID, tDatabaseName)
	require.NoError(t, verr)
	assert.Equal(t, volRow.ID, reqs[0].Volume.VolumeID, "volume mount keyed by volume row id (ADR-0097)")
	assert.Equal(t, "REDIS0011-rdb-bytes", ut.capturedStdins()[0])

	// 清挂起后 databaseStep 恢复收敛（首启装载预置数据）。
	e.databases.Step(ctx)
	assert.Greater(t, len(rt.calls()), before, "cleared pending lets the database converge")

	fresh, err := e.repos.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, fresh.RestoreFromBackup)
}
