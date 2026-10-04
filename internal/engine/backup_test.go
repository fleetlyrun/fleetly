package engine

// Backup 环测试（ADR-0039 验收锚）：调度（到期铸行）、执行（工具容器
// stdout 流式 Put——digest/size 回执与对象内容）、失败落账、恢复流式/
// 预置卷双形态、preseed 门控（挂起中不 Ensure）、保留滚动、启动清扫。
// ObjectStore 用仓内假面（providers 只准 cmd 装配——import 守卫；真
// Provider 行为由 localobjectstore 自测与 dind 演练承担）；Utility 用
// 函数面假面。

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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state/backup"
	dbrepo "github.com/fleetlyrun/fleetly/internal/state/database"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/outbox"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

// fakeObjectStore 是 ObjectStore 端口的内存假面（Put 流式铸 sha256——
// 与 localobjectstore 同口径，digest 断言语义一致）。
type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string][]byte{}}
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

// fakeUtility 是 RuntimeUtility 假面（记录请求；可编程 stdout/失败）。
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

// newBackupFixture 组装备份环夹具：内存 ObjectStore 假面 + 假 Utility +
// 单库（running、interval 3600s 起步）。
func newBackupFixture(t *testing.T, engineName, connectURL string) (*Engine, *fakeRuntime, *fakeUtility, *fakeObjectStore, *statertest.FakeClock) {
	t.Helper()
	db, clock := statertest.New(t)
	rt := newFakeRuntime()
	ut := &fakeUtility{stdout: []byte("BACKUP-BYTES-0123456789")}
	store := newFakeObjectStore()
	cipher, err := material.LoadCipher(t.TempDir())
	require.NoError(t, err)
	e := New(Deps{DB: db, Runtime: runtimeWithUtility{rt, ut}, ObjectStore: store, Cipher: cipher, Logger: discardLogger()}, Options{})
	ctx := context.Background()

	require.NoError(t, project.New(clock).Create(ctx, db.Runner(), &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	require.NoError(t, networkrepo.New(clock).Create(ctx, db.Runner(), &networkrepo.Network{
		ID: "01JD0NET000000000000000001", ProjectID: tProjectID, Name: "default",
	}))
	secName := DBCredentialSecretName(tDatabaseName)
	ct, err := cipher.Seal([]byte(connectURL))
	require.NoError(t, err)
	require.NoError(t, secret.New(clock).Upsert(ctx, db.Runner(), &secret.Secret{
		ID: "01JD0SEC000000000000000001", ProjectID: tProjectID, Name: secName,
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(connectURL)),
	}))
	require.NoError(t, dbrepo.New(clock).Create(ctx, db.Runner(), &dbrepo.Database{
		ID: tDatabaseID, ProjectID: tProjectID, Name: tDatabaseName,
		Engine: engineName, CredentialsRef: secName,
		BackupIntervalSecs: 3600, BackupRetentionSecs: 604800,
	}))
	require.NoError(t, dbrepo.New(clock).SetStatus(ctx, e.db.Runner(), tDatabaseID, dbrepo.StatusRunning))
	return e, rt, ut, store, clock
}

// seedSucceededBackup 直接落一颗成功备份行 + 对象（上游链路另有测试）。
func seedSucceededBackup(t *testing.T, e *Engine, store *fakeObjectStore, backupID, engineName, payload string) {
	t.Helper()
	ctx := context.Background()
	key := backup.KeyMint(tProjectID, tDatabaseID, backupID, e.clock.Now())
	info, err := store.Put(ctx, key, strings.NewReader(payload))
	require.NoError(t, err)
	require.NoError(t, e.backups.Create(ctx, e.db.Runner(), &backup.Backup{
		ID: backupID, ProjectID: tProjectID, DatabaseID: tDatabaseID,
		Engine: engineName, RetentionSecs: 604800,
	}))
	require.NoError(t, e.backups.MarkRunning(ctx, e.db.Runner(), backupID))
	require.NoError(t, e.backups.FinishSucceeded(ctx, e.db.Runner(), backupID, key, info.Digest, info.Size))
}

// 调度 + 执行 + 事件 + 锚推进全链（到期窗由假钟推进）。
func TestBackupScheduleExecuteSucceeds(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, _, ut, store, clock := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	e.backupStep(ctx) // 未到期：零行零请求
	assert.Empty(t, ut.requests())

	clock.Advance(3601 * time.Second)
	e.backupStep(ctx) // 到期：铸行 + 执行一件

	rows, err := e.backups.ListByDatabase(ctx, e.db.Runner(), tDatabaseID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, backup.StatusSucceeded, rows[0].Status)
	assert.NotEmpty(t, rows[0].ObjectKey)
	assert.NotEmpty(t, rows[0].Digest)
	assert.Equal(t, int64(len("BACKUP-BYTES-0123456789")), rows[0].SizeBytes)

	// 对象内容（工具容器 stdout 落位）。
	obj, err := store.Get(ctx, rows[0].ObjectKey)
	require.NoError(t, err)
	body, err := io.ReadAll(obj)
	require.NoError(t, err)
	require.NoError(t, obj.Close())
	assert.Equal(t, "BACKUP-BYTES-0123456789", string(body))

	// 工具容器请求面：模板镜像 + 项目网 + 材料文件（argv 细节由 dbtemplate
	// 渲染钉板钉死，此处断言装配面）。
	reqs := ut.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "postgres:17-bookworm", reqs[0].Image)
	assert.Equal(t, []string{"default"}, reqs[0].Networks)
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID, Database: tDatabaseID}, reqs[0].Namespace)
	assert.Contains(t, reqs[0].SecretFiles, "database-backup-pgpass")

	// 调度锚推进 + 事件。
	fresh, err := e.databases.Get(ctx, e.db.Runner(), tDatabaseID)
	require.NoError(t, err)
	assert.NotEmpty(t, fresh.LastBackupAt)
	events, err := outbox.New(clock).ListAfter(ctx, e.db.Runner(), 0, 10)
	require.NoError(t, err)
	found := false
	for _, ev := range events {
		if ev.Name == eventBackupSucceeded {
			found = true
			assert.Contains(t, string(ev.Payload), rows[0].ObjectKey)
			assert.Contains(t, string(ev.Payload), rows[0].Digest)
		}
	}
	assert.True(t, found, "backup_succeeded event must land in the outbox")
}

// 未到期不铸行。
func TestBackupScheduleNotDue(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, _, ut, _, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	e.backupStep(ctx)
	rows, err := e.backups.ListByDatabase(ctx, e.db.Runner(), tDatabaseID, "", 10)
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.Empty(t, ut.requests())
}

// 工具容器失败：行落 failed（报文带 stderr 尾部）；调度锚不推进（失败
// 不自放大——下一 interval 到期再试）。
func TestBackupUtilityFailureRecorded(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, _, ut, _, clock := newBackupFixture(t, "postgres", url)
	ut.execErr = context.DeadlineExceeded
	ctx := context.Background()

	clock.Advance(3601 * time.Second)
	e.backupStep(ctx)

	rows, err := e.backups.ListByDatabase(ctx, e.db.Runner(), tDatabaseID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, backup.StatusFailed, rows[0].Status)
	assert.Contains(t, rows[0].Error, "backup execution failed")
	assert.Contains(t, rows[0].Error, "boom-detail", "stderr tail rides the failure message")

	fresh, err := e.databases.Get(ctx, e.db.Runner(), tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, fresh.LastBackupAt, "failed backups must not advance the schedule anchor")
}

// 流式恢复：目标库 running → stdin = 对象内容 → 清挂起 + database.restored。
func TestRestoreStreamFlow(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, _, ut, store, clock := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	seedSucceededBackup(t, e, store, "01JD0BKP000000000000000001", "postgres", "BACKUP-BYTES-0123456789")
	require.NoError(t, e.databases.SetRestorePending(ctx, e.db.Runner(), tDatabaseID, "01JD0BKP000000000000000001"))
	e.backupStep(ctx)

	reqs := ut.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, []string{"pg_restore", "-h", DatabaseDNSName(tDatabaseID), "-p", "5432", "-U", "fleetly", "-d", "fleetly", "--no-password", dbtemplate.BackupInputPath}, reqs[0].Argv)
	assert.Equal(t, "BACKUP-BYTES-0123456789", ut.capturedStdins()[0], "restore input file carries the backup object")

	fresh, err := e.databases.Get(ctx, e.db.Runner(), tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, fresh.RestoreFromBackup, "pending flag cleared after successful restore")
	events, err := outbox.New(clock).ListAfter(ctx, e.db.Runner(), 0, 10)
	require.NoError(t, err)
	restored := false
	for _, ev := range events {
		if ev.Name == eventDatabaseRestored {
			restored = true
		}
	}
	assert.True(t, restored, "database.restored event must land")
}

// 预置卷恢复（redis）：挂起中 databaseLoop 不 Ensure；backupStep 预置
// （stdin 落卷 + 卷钉住控制面节点）→ 清挂起 → 下一拍 databaseStep 正常
// 收敛首启。
func TestRestorePreseedFlow(t *testing.T) {
	const url = "redis://:redispw@db-01jd0db000000000000000000:6379/0" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, rt, ut, store, _ := newBackupFixture(t, "redis", url)
	ctx := context.Background()

	seedSucceededBackup(t, e, store, "01JD0BKP000000000000000002", "redis", "REDIS0011-rdb-bytes")
	require.NoError(t, e.databases.SetRestorePending(ctx, e.db.Runner(), tDatabaseID, "01JD0BKP000000000000000002"))

	// 门控：挂起中 databaseStep 不 Ensure（空卷首启 = 数据丢失）。
	before := len(rt.calls())
	e.databaseStep(ctx)
	assert.Len(t, rt.calls(), before, "preseed-pending database must not be ensured")

	e.backupStep(ctx)
	reqs := ut.requests()
	require.Len(t, reqs, 1)
	require.NotNil(t, reqs[0].Volume, "preseed mounts the data volume")
	assert.Equal(t, tDatabaseName, reqs[0].Volume.VolumeID, "volume by platform name formula")
	assert.Equal(t, "REDIS0011-rdb-bytes", ut.capturedStdins()[0])

	// 卷钉住控制面节点（fake 集群唯一 manager）。
	vol, err := e.volumes.GetByName(ctx, e.db.Runner(), tProjectID, tDatabaseName)
	require.NoError(t, err)
	assert.Equal(t, "01JD0NODE00000000000000000", vol.PinnedNodeID, "seed volume pinned to the control-plane node")

	// 清挂起后 databaseStep 恢复收敛（首启装载预置数据）。
	e.databaseStep(ctx)
	assert.Greater(t, len(rt.calls()), before, "cleared pending lets the database converge")

	fresh, err := e.databases.Get(ctx, e.db.Runner(), tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, fresh.RestoreFromBackup)
}

// 恢复失败：挂起清位 + restore_error 落行（半恢复态重试不可幂等——诚实
// 留给用户重建，ADR-0039 决策 6）。
func TestRestoreFailureRecorded(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, _, ut, store, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	seedSucceededBackup(t, e, store, "01JD0BKP000000000000000003", "postgres", "BYTES")
	require.NoError(t, e.databases.SetRestorePending(ctx, e.db.Runner(), tDatabaseID, "01JD0BKP000000000000000003"))
	ut.execErr = context.DeadlineExceeded

	e.backupStep(ctx)

	fresh, err := e.databases.Get(ctx, e.db.Runner(), tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, fresh.RestoreFromBackup, "pending cleared even on failure (no half-retry)")
	assert.Contains(t, fresh.RestoreError, "restore execution failed")
}

// 引擎不匹配：诚实拒绝（pg 备份进 mysql 库是无意义操作）。
func TestRestoreEngineMismatch(t *testing.T) {
	const url = "mysql://fleetly:secretpw@db-01jd0db000000000000000000:3306/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, _, ut, store, _ := newBackupFixture(t, "mysql", url)
	ctx := context.Background()

	seedSucceededBackup(t, e, store, "01JD0BKP000000000000000004", "postgres", "BYTES") // 源引擎与目标行不符
	require.NoError(t, e.databases.SetRestorePending(ctx, e.db.Runner(), tDatabaseID, "01JD0BKP000000000000000004"))

	e.backupStep(ctx)
	assert.Empty(t, ut.requests(), "mismatched engines must not run a utility container")
	fresh, err := e.databases.Get(ctx, e.db.Runner(), tDatabaseID)
	require.NoError(t, err)
	assert.Contains(t, fresh.RestoreError, "does not match target engine")
}

// 保留滚动：窗外的行/对象成对删；窗内保留。
func TestBackupPruneRollsRetentionWindow(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, _, _, store, clock := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	seedSucceededBackup(t, e, store, "01JD0BKPA0000000000000001", "postgres", "BYTES-old")
	oldKey := backup.KeyMint(tProjectID, tDatabaseID, "01JD0BKPA0000000000000001", e.clock.Now())
	clock.Advance(8 * 24 * time.Hour) // 旧行进入窗外（retention 7d）
	seedSucceededBackup(t, e, store, "01JD0BKPB0000000000000002", "postgres", "BYTES-new")
	newKey := backup.KeyMint(tProjectID, tDatabaseID, "01JD0BKPB0000000000000002", e.clock.Now())

	e.backupStep(ctx) // 注：库此刻已到期，调度面会另铸一行——按 ID 定向断言

	_, err := e.backups.Get(ctx, e.db.Runner(), "01JD0BKPA0000000000000001")
	assert.Error(t, err, "expired row must be pruned")
	_, err = store.Stat(ctx, oldKey)
	assert.Error(t, err, "expired object must be pruned alongside its row")

	kept, err := e.backups.Get(ctx, e.db.Runner(), "01JD0BKPB0000000000000002")
	require.NoError(t, err)
	assert.Equal(t, backup.StatusSucceeded, kept.Status)
	_, err = store.Stat(ctx, newKey)
	assert.NoError(t, err, "fresh object kept")
}

// 启动清扫：running 行落 failed（重启打断的执行不可续传）。
func TestBackupSweepInterrupted(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	e, _, _, _, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	require.NoError(t, e.backups.Create(ctx, e.db.Runner(), &backup.Backup{
		ID: "01JD0BKPC0000000000000003", ProjectID: tProjectID, DatabaseID: tDatabaseID,
		Engine: "postgres", RetentionSecs: 3600,
	}))
	require.NoError(t, e.backups.MarkRunning(ctx, e.db.Runner(), "01JD0BKPC0000000000000003"))

	e.sweepInterruptedBackups(ctx)

	row, err := e.backups.Get(ctx, e.db.Runner(), "01JD0BKPC0000000000000003")
	require.NoError(t, err)
	assert.Equal(t, backup.StatusFailed, row.Status)
	assert.Equal(t, "interrupted by platform restart", row.Error)
}

// 对象键形态：无冒号紧凑 UTC（Windows 控制面纪律）+ 段序。
func TestBackupKeyMint(t *testing.T) {
	key := backup.KeyMint("01JPROJ", "01JDB", "01JBKP", statertest.MustParse("2026-10-04T12:34:56Z"))
	assert.Equal(t, "backups/01JPROJ/01JDB/20261004T123456Z-01JBKP", key)
	assert.NotContains(t, key, ":", "key must stay colon-free for windows control planes")
}
