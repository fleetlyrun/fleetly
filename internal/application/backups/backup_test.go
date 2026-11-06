package backups

// Backup 环卫星白盒（F2.2，ADR-0039 验收锚；P5 wave-3 随域拆包自 engine 包
// 迁入，断言面零漂移）：调度（到期铸行）、执行（工具容器 stdout 流式
// Put——digest/size 回执与对象内容）、失败落账、恢复流式形态、保留滚动、
// 启动清扫、Download/Verify 读面。ObjectStore 用内存假面（providers 只准
// cmd 装配——import 守卫；真 Provider 行为由 localobjectstore 自测与 dind
// 演练承担）；Utility 直接注入假面（装配期的 FacesOf 探测在组合根，白盒
// 面不依赖核装配）；facts/emitter 端口为本地闭包（真 repo 读 + 事实 kernel
// 直发——与核绑定等价的 hermetic 形态）。preseed 门控（挂起中
// databaseStep 不 Ensure）是跨域面，留在 engine 包（backupgate_test.go）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engineports"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/domain/telemetry/outbox"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/keyring"
	auditrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/audit"
	backuprepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/backup"
	dbrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/database"
	networkrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/network"
	outboxrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/outbox"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	secretrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/secret"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	volumerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/volume"
)

// 测试常量（engine 包同源夹具 ID——断言面零漂移）。
const (
	tProjectID  = "01JD0PROJ00000000000000000"
	tDatabaseID = "01jd0db0000000000000000000"
	tDatabaseNm = "shop"
	tManagerID  = "01JD0NODE00000000000000000"
)

// fakeObjectStore 是 ObjectStore 端口的内存假面（Put 流式铸 sha256——
// 与 localobjectstore 同口径，digest 断言语义一致；engine 包同源迁入）。
// B4a 扩 manifest 面：modTime 逐键记 Put 时刻（orphanPass 宽限窗断言）、
// failManifestPuts 注入 manifest 键 Put 失败（manifest 最后写/失败清理面）。
type fakeObjectStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	modTime map[string]time.Time
	// putFailNoRead 模拟"未读 reader 即返回错误"的 ObjectStore 形态（断网/
	// 磁盘满/ctx 取消）：写端会阻塞在 io.Pipe 上——H5 的挂死触发面。置位时
	// Put 直接返回错误、不消费 reader。
	putFailNoRead bool
	// failManifestPuts 对 manifest 键（/manifest.json 结尾）注入 Put 失败
	//（不读 reader）——「全成员成功才写 manifest」链路的失败注入面。
	failManifestPuts bool
}

func newFakeObjectStore() *fakeObjectStore {
	return &fakeObjectStore{objects: map[string][]byte{}, modTime: map[string]time.Time{}}
}

func (f *fakeObjectStore) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake", Capability: capability.KindObjectStore, Version: "1"}
}

func (f *fakeObjectStore) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *fakeObjectStore) Put(_ context.Context, key string, r io.Reader) (capability.ObjectInfo, error) {
	f.mu.Lock()
	failNoRead := f.putFailNoRead || (f.failManifestPuts && strings.HasSuffix(key, "/manifest.json"))
	f.mu.Unlock()
	if failNoRead {
		return capability.ObjectInfo{}, errors.New("fake object store: put failed before reading")
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return capability.ObjectInfo{}, err
	}
	sum := sha256.Sum256(body)
	f.mu.Lock()
	f.objects[key] = body
	f.modTime[key] = time.Now().UTC()
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
			out = append(out, capability.ObjectInfo{Key: k, Size: int64(len(v)), ModTime: f.modTime[k]})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (f *fakeObjectStore) Delete(_ context.Context, key string) error {
	f.mu.Lock()
	delete(f.objects, key)
	delete(f.modTime, key)
	f.mu.Unlock()
	return nil
}

// setModTime 回拨对象 ModTime（orphanPass 宽限窗的注入面：假钟走假钟、
// 对象年龄走真实时钟——直接按目标时刻写表）。
func (f *fakeObjectStore) setModTime(key string, at time.Time) {
	f.mu.Lock()
	f.modTime[key] = at
	f.mu.Unlock()
}

// keys 返回在场键快照（排序稳定——「失败后零对象」类断言面）。
func (f *fakeObjectStore) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objects))
	for k := range f.objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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

// buildStores 按 runner 构造 repo 集（备份域消费面：Backups/Databases/
// Volumes/Secrets/Projects/Networks + 事实落库双面；Tx.Do 传入 tx 作用域
// runner——事务内事实扇出锁内同连接，忽略 runner 会与打开中的事务互锁）。
func buildStores(clock state.Clock, run state.Runner) transact.Stores {
	return transact.Stores{
		Projects: projectrepo.New(clock, run), Networks: networkrepo.New(clock, run),
		Secrets: secretrepo.New(clock, run), Volumes: volumerepo.New(clock, run),
		Databases: dbrepo.New(clock, run), Backups: backuprepo.New(clock, run),
		Outbox: outboxrepo.New(clock, run), Audits: auditrepo.New(clock, run),
	}
}

// satelliteStores 是 db 绑定形态（夹具种子行与断言读面）。
func satelliteStores(db state.DB) transact.Stores {
	return buildStores(db.Clock(), db.Runner())
}

// cipherOf 是夹具级主键面（每测试独立密钥目录；回读与解封共用单源）。
func cipherOf(t *testing.T) *material.Cipher {
	t.Helper()
	c, err := keyring.LoadCipher(t.TempDir())
	require.NoError(t, err)
	return c
}

// slogDiscard 是静音日志面（hermetic 夹具）。
func slogDiscard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// satelliteDeps 组装 hermetic 依赖集（真 SQLite repo + 事实 kernel 直发；
// facts 端口为与核 resolve.go 等价的本地闭包——白盒面不依赖核装配。
// cipher 由调用方持有同一实例（铸/读单源——夹具行与端口解封共用）。
func satelliteDeps(t *testing.T, db state.DB, cipher *material.Cipher, ut *fakeUtility, store *fakeObjectStore, opts Options) Deps {
	t.Helper()
	stores := satelliteStores(db)
	deps := Deps{
		// Tx 的 binder 按 tx 作用域 runner 重建 repo 集（s.Outbox/s.Audits
		// 与事务同连接——writer 端口的事实扇出死锁面由此封死）。
		ObjectStore: store,
		Utility:     ut,
		Repos:       stores,
		DB:          db,
		Tx:          transact.NewTx(db, func(run state.Runner) transact.Stores { return buildStores(db.Clock(), run) }),
		Options:     opts,
		Log:         slogDiscard(),
		Clock:       db.Clock(),
		LockMaintenance: func() func() {
			return func() {}
		},
		CommitWrite: func(ctx context.Context, s transact.Stores, f engineports.WriteFact) error {
			for _, check := range f.Checks {
				if err := check(ctx, s); err != nil {
					return err
				}
			}
			if f.Write != nil {
				if err := f.Write(ctx, s); err != nil {
					return err
				}
			}
			// 事实扇出走 tx 作用域 s（核 commitWrite 同款——锁内同连接，
			// 用夹具级 stores 会与打开中的事务互锁）。
			if err := writes.Sequence(ctx, writes.Repos{Outbox: s.Outbox, Audits: s.Audits}, f.Facts...); err != nil {
				return err
			}
			for _, st := range f.Steps {
				if st.Write != nil {
					if err := st.Write(ctx, s); err != nil {
						return err
					}
				}
				if err := writes.Sequence(ctx, writes.Repos{Outbox: s.Outbox, Audits: s.Audits}, st.Facts...); err != nil {
					return err
				}
			}
			return nil
		},
		EmitObservation: func(ctx context.Context, team, name, aggregate, id string, payload []byte) error {
			return writes.Emit(ctx, writes.Repos{Outbox: stores.Outbox, Audits: stores.Audits}, writes.FactRow{
				Name: name, Aggregate: aggregate, ID: id,
				Payload: payload, TeamID: team, StreamOnly: true, // 观测事实：仅流面（best-effort，无事务伴生——hermetic 端口与核 emitObservation 同口径）
			})
		},
		EventTeamOfProject: func(ctx context.Context, s transact.Stores, projectID string) string {
			p, err := s.Projects.Get(ctx, projectID)
			if err != nil {
				return ""
			}
			return p.TeamID
		},
		ProjectTeamOf: func(string) string { return "default" },
		ProjectNetworkFactsNamesOnly: func(_ context.Context, _ string) []string {
			return []string{"default"}
		},
		ControlPlaneNode: func(context.Context) (string, error) { return tManagerID, nil },
		EnsureDatabaseVolume: func(ctx context.Context, row *database.Database) (*volume.Volume, error) {
			if existing, err := stores.Volumes.GetByName(ctx, row.ProjectID, row.Name); err == nil {
				return existing, nil
			}
			created := &volume.Volume{ProjectID: row.ProjectID, Name: row.Name}
			if err := stores.Volumes.Create(ctx, created); err != nil {
				return nil, err
			}
			return created, nil
		},
		DatabasePassword: func(ctx context.Context, row *database.Database) (string, error) {
			sec, err := stores.Secrets.GetByName(ctx, row.ProjectID, row.CredentialsRef)
			if err != nil {
				return "", err
			}
			plain, err := cipher.Open(sec.Ciphertext)
			if err != nil {
				return "", err
			}
			return dbtemplate.PasswordFromURL(string(plain))
		},
		KickDatabases: func() {},
	}
	return deps
}

// newBackupFixture 组装备份环夹具：内存 ObjectStore 假面 + 假 Utility +
// 单库（running、interval 3600s 起步）。
func newBackupFixture(t *testing.T, engineName, connectURL string) (*Backups, *fakeUtility, *fakeObjectStore, *statetest.FakeClock, transact.Stores) {
	t.Helper()
	db, clock := statetest.New(t)
	ut := &fakeUtility{stdout: []byte("BACKUP-BYTES-0123456789")}
	store := newFakeObjectStore()
	cipher := cipherOf(t)
	bk := New(satelliteDeps(t, db, cipher, ut, store, Options{BackupTimeout: 15 * time.Minute}))
	ctx := context.Background()

	require.NoError(t, projectrepo.New(clock, db.Runner()).Create(ctx, &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	require.NoError(t, networkrepo.New(clock, db.Runner()).Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000001", ProjectID: tProjectID, Name: "default",
	}))
	secName := engineports.DBCredentialSecretName(tDatabaseNm)
	ct, err := cipher.Seal([]byte(connectURL))
	require.NoError(t, err)
	require.NoError(t, secretrepo.New(clock, db.Runner()).Upsert(ctx, &secret.Secret{
		ID: "01JD0SEC000000000000000001", ProjectID: tProjectID, Name: secName,
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(connectURL)),
	}))
	require.NoError(t, dbrepo.New(clock, db.Runner()).Create(ctx, &database.Database{
		ID: tDatabaseID, ProjectID: tProjectID, Name: tDatabaseNm,
		Engine: engineName, CredentialsRef: secName,
		BackupIntervalSecs: 3600, BackupRetentionSecs: 604800,
	}))
	require.NoError(t, dbrepo.New(clock, db.Runner()).SetStatus(ctx, tDatabaseID, database.StatusRunning))
	return bk, ut, store, clock, bk.repos
}

// seedSucceededBackup 直接落一颗成功备份行 + manifest 锚定对象集（上游
// 链路另有测试）：单成员 data + manifest 最后写——与 executeOneBackup 成功
// 面同构。回传 (manifestKey, memberKey)（断言面）。engine_version 用种子
// 值 "18"（manifest 形状锚非消费面——恢复链只对 engine 名做同库断言）。
func seedSucceededBackup(t *testing.T, bk *Backups, store *fakeObjectStore, backupID, engineName, payload string) (manifestKey, memberKey string) {
	t.Helper()
	ctx := context.Background()
	set := backup.KeyMint(tProjectID, tDatabaseID, backupID, bk.clock.Now())
	var err error
	memberKey, err = backup.MemberKeyOf(set, backup.DefaultMemberName)
	require.NoError(t, err)
	info, err := store.Put(ctx, memberKey, strings.NewReader(payload))
	require.NoError(t, err)
	manifest := &backup.Manifest{
		Family: enginetemplate.FamilyDatabase, Engine: engineName, EngineVersion: "18",
		Members:   []backup.ManifestMember{{Key: memberKey, SHA256: info.Digest, SizeBytes: info.Size}},
		CreatedAt: state.FormatTime(bk.clock.Now()),
	}
	manifest.TotalDigest = backup.TotalDigest(manifest.Members)
	manifestBytes, err := manifest.Encode()
	require.NoError(t, err)
	manifestKey = backup.ManifestKeyOf(set)
	_, err = store.Put(ctx, manifestKey, bytes.NewReader(manifestBytes))
	require.NoError(t, err)
	require.NoError(t, bk.repos.Backups.Create(ctx, &backup.Backup{
		ID: backupID, ProjectID: tProjectID, DatabaseID: tDatabaseID,
		Engine: engineName, RetentionSecs: 604800,
	}))
	require.NoError(t, bk.repos.Backups.MarkRunning(ctx, backupID))
	require.NoError(t, bk.repos.Backups.FinishSucceeded(ctx, backupID, manifestKey, manifest.TotalDigest, manifest.TotalSize()))
	return manifestKey, memberKey
}

// eventNamesOf 读 outbox 事件名（aggregate 可选过滤）。
func eventNamesOf(t *testing.T, bk *Backups, aggregateID string) []string {
	t.Helper()
	evs, err := bk.repos.Outbox.ListAfter(context.Background(), 0, 1000, "")
	require.NoError(t, err)
	var names []string
	for _, ev := range evs {
		if aggregateID == "" || ev.AggregateID == aggregateID {
			names = append(names, ev.Name)
		}
	}
	return names
}

// auditActionsOf 读审计行动作面（resource 前缀过滤）。
func auditActionsOf(t *testing.T, bk *Backups, resourcePrefix string) []string {
	t.Helper()
	rows, err := bk.repos.Audits.List(context.Background(), 500)
	require.NoError(t, err)
	var actions []string
	for _, r := range rows {
		if strings.HasPrefix(r.Resource, resourcePrefix) {
			actions = append(actions, r.Action)
		}
	}
	return actions
}

// 调度 + 执行 + 事件 + 锚推进全链（到期窗由假钟推进）。B4a 起断言面 =
// manifest 锚定对象集：ObjectKey=manifest 键 / Digest=total_digest /
// SizeBytes=成员合计；成员对象承载产物字节；manifest 最后写（在场且内容
// 锚定 family/engine/成员回执）。
func TestBackupScheduleExecuteSucceeds(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, ut, store, clock, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	bk.Step(ctx) // 未到期：零行零请求
	assert.Empty(t, ut.requests())

	clock.Advance(3601 * time.Second)
	bk.Step(ctx) // 到期：铸行 + 执行一件

	rows, err := bk.repos.Backups.ListByDatabase(ctx, tDatabaseID, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, backup.StatusSucceeded, rows[0].Status)
	// manifest 锚三元组：键形 = 集前缀 + manifest.json；size = 成员合计
	//（不含 manifest 自身）；digest = total_digest。
	require.NotEmpty(t, rows[0].ObjectKey)
	assert.True(t, strings.HasSuffix(rows[0].ObjectKey, "/manifest.json"),
		"ledger ObjectKey must be the manifest key, got %q", rows[0].ObjectKey)
	assert.Equal(t, int64(len("BACKUP-BYTES-0123456789")), rows[0].SizeBytes)
	wantMemberSum := sha256.Sum256([]byte("BACKUP-BYTES-0123456789"))
	wantTotal := sha256.Sum256([]byte(hex.EncodeToString(wantMemberSum[:])))
	assert.Equal(t, hex.EncodeToString(wantTotal[:]), rows[0].Digest,
		"total_digest = sha256 of the ordered member digest concatenation")

	// 成员对象内容（工具容器 stdout 落位；单流引擎成员名 = data）。
	memberKey := strings.TrimSuffix(rows[0].ObjectKey, "/manifest.json") + "/" + backup.DefaultMemberName
	obj, err := store.Get(ctx, memberKey)
	require.NoError(t, err)
	body, err := io.ReadAll(obj)
	require.NoError(t, err)
	require.NoError(t, obj.Close())
	assert.Equal(t, "BACKUP-BYTES-0123456789", string(body))

	// manifest 对象在场且内容锚（family/engine/engine_version/成员回执）。
	mobj, err := store.Get(ctx, rows[0].ObjectKey)
	require.NoError(t, err)
	mbody, err := io.ReadAll(mobj)
	require.NoError(t, err)
	require.NoError(t, mobj.Close())
	assert.Contains(t, string(mbody), `"family":"database"`)
	assert.Contains(t, string(mbody), `"engine":"postgres"`)
	assert.Contains(t, string(mbody), memberKey)
	assert.Contains(t, string(mbody), hex.EncodeToString(wantMemberSum[:]))

	// 工具容器请求面：模板镜像 + 项目网 + 材料文件（argv 细节由 dbtemplate
	// 渲染钉板钉死，此处断言装配面）。
	reqs := ut.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, "postgres:18-trixie@sha256:bd5dfa494a5a259704365813dbfa5fa6b3e69be7348d43b547f540c730478583", reqs[0].Image)
	assert.Equal(t, []string{"default"}, reqs[0].Networks)
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID, Database: tDatabaseID}, reqs[0].Namespace)
	assert.Contains(t, reqs[0].SecretFiles, "database-backup-pgpass")

	// 调度锚推进 + 事件。
	fresh, err := bk.repos.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.NotEmpty(t, fresh.LastBackupAt)
	names := eventNamesOf(t, bk, tDatabaseID)
	assert.Contains(t, names, EventBackupSucceeded, "backup_succeeded event must land in the outbox")
	// 事件载荷含 manifest 锚回执。
	objSeen := false
	for _, ev := range outboxRows(t, bk) {
		if ev.Name == EventBackupSucceeded {
			objSeen = strings.Contains(string(ev.Payload), rows[0].ObjectKey)
		}
	}
	assert.True(t, objSeen, "backup_succeeded payload must carry the manifest receipt")
}

// outboxRows 直读 outbox 行（载荷断言面）。
func outboxRows(t *testing.T, bk *Backups) []outbox.Event {
	t.Helper()
	evs, err := bk.repos.Outbox.ListAfter(context.Background(), 0, 1000, "")
	require.NoError(t, err)
	return evs
}

// 未到期不铸行。
func TestBackupScheduleNotDue(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, ut, _, _, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	bk.Step(ctx)
	rows, err := bk.repos.Backups.ListByDatabase(ctx, tDatabaseID, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, rows)
	assert.Empty(t, ut.requests())
}

// 工具容器失败：行落 failed（报文带 stderr 尾部）；调度锚不推进（失败
// 不自放大——下一 interval 到期再试）。B4a：失败行无 ObjectKey、零对象
// 残留（成员 Put 未成功 + 显式清理幂等收口）。
func TestBackupUtilityFailureRecorded(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, ut, store, clock, _ := newBackupFixture(t, "postgres", url)
	ut.execErr = context.DeadlineExceeded
	ctx := context.Background()

	clock.Advance(3601 * time.Second)
	bk.Step(ctx)

	rows, err := bk.repos.Backups.ListByDatabase(ctx, tDatabaseID, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, backup.StatusFailed, rows[0].Status)
	assert.Empty(t, rows[0].ObjectKey, "failed rows never carry an object key")
	assert.Contains(t, rows[0].Error, "backup execution failed")
	assert.Contains(t, rows[0].Error, "boom-detail", "stderr tail rides the failure message")
	assert.Empty(t, store.keys(), "a failed member leaves no objects behind")

	fresh, err := bk.repos.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, fresh.LastBackupAt, "failed backups must not advance the schedule anchor")
}

// ObjectStore 未读 reader 即返回错误（断网/磁盘满/ctx 取消的真实 Provider
// 形态）：executeOneBackup 必须秒级返回并把行落 failed，不得在 io.Pipe 上
// 与工具容器互等（H5：备份环单写者挂死 + maintenanceMu 读锁连坐）。
func TestBackupPutFailureBeforeReadDoesNotHang(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, _, store, clock, _ := newBackupFixture(t, "postgres", url)
	store.putFailNoRead = true
	clock.Advance(3601 * time.Second)

	done := make(chan struct{})
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		bk.Step(ctx)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Step hung on a Put failure that returned without reading — the pipe reader is not being closed")
	}

	ctx := context.Background()
	rows, err := bk.repos.Backups.ListByDatabase(ctx, tDatabaseID, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, backup.StatusFailed, rows[0].Status)
	assert.Contains(t, rows[0].Error, "put failed before reading")

	// 失败不产对象、不推进调度锚（与既有失败语义一致）。
	store.mu.Lock()
	objectCount := len(store.objects)
	store.mu.Unlock()
	assert.Zero(t, objectCount)
	fresh, err := bk.repos.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, fresh.LastBackupAt)
}

// 流式恢复：目标库 running → stdin = 对象内容 → 清挂起 + database.restored。
func TestRestoreStreamFlow(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, ut, store, _, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	seedSucceededBackup(t, bk, store, "01JD0BKP000000000000000001", "postgres", "BACKUP-BYTES-0123456789")
	require.NoError(t, bk.repos.Databases.SetRestorePending(ctx, tDatabaseID, "01JD0BKP000000000000000001"))
	bk.Step(ctx)

	reqs := ut.requests()
	require.Len(t, reqs, 1)
	assert.Equal(t, []string{"pg_restore", "-h", engineports.DatabaseDNSName(tDatabaseID), "-p", "5432", "-U", "fleetly", "-d", "fleetly", "--no-password", dbtemplate.BackupInputPath}, reqs[0].Argv)
	assert.Equal(t, "BACKUP-BYTES-0123456789", ut.capturedStdins()[0], "restore input file carries the backup object")

	fresh, err := bk.repos.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, fresh.RestoreFromBackup, "pending flag cleared after successful restore")
	assert.Contains(t, eventNamesOf(t, bk, tDatabaseID), EventDatabaseRestored, "database.restored event must land")
}

// 恢复失败：挂起清位 + restore_error 落行（半恢复态重试不可幂等——诚实
// 留给用户重建，ADR-0039 决策 6）。
func TestRestoreFailureRecorded(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, ut, store, _, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	seedSucceededBackup(t, bk, store, "01JD0BKP000000000000000003", "postgres", "BYTES")
	require.NoError(t, bk.repos.Databases.SetRestorePending(ctx, tDatabaseID, "01JD0BKP000000000000000003"))
	ut.execErr = context.DeadlineExceeded

	bk.Step(ctx)

	fresh, err := bk.repos.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, fresh.RestoreFromBackup, "pending cleared even on failure (no half-retry)")
	assert.Contains(t, fresh.RestoreError, "restore execution failed")
}

// 引擎不匹配：诚实拒绝（pg 备份进 mysql 库是无意义操作）。
func TestRestoreEngineMismatch(t *testing.T) {
	const url = "mysql://fleetly:secretpw@db-01jd0db0000000000000000000:3306/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, ut, store, _, _ := newBackupFixture(t, "mysql", url)
	ctx := context.Background()

	seedSucceededBackup(t, bk, store, "01JD0BKP000000000000000004", "postgres", "BYTES") // 源引擎与目标行不符
	require.NoError(t, bk.repos.Databases.SetRestorePending(ctx, tDatabaseID, "01JD0BKP000000000000000004"))

	bk.Step(ctx)
	assert.Empty(t, ut.requests(), "mismatched engines must not run a utility container")
	fresh, err := bk.repos.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Contains(t, fresh.RestoreError, "does not match target engine")
}

// 预置卷恢复（redis）的卫星面：stdin 落卷 + 卷钉住控制面节点 + 清挂起
// （挂起中 databaseLoop 不 Ensure 的门控断言在 engine 包 backupgate_test）。
func TestRestorePreseedVolumePinned(t *testing.T) {
	const url = "redis://:redispw@db-01jd0db0000000000000000000:6379/0" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, ut, store, _, _ := newBackupFixture(t, "redis", url)
	ctx := context.Background()

	seedSucceededBackup(t, bk, store, "01JD0BKP000000000000000002", "redis", "REDIS0011-rdb-bytes")
	require.NoError(t, bk.repos.Databases.SetRestorePending(ctx, tDatabaseID, "01JD0BKP000000000000000002"))

	bk.Step(ctx)
	reqs := ut.requests()
	require.Len(t, reqs, 1)
	require.NotNil(t, reqs[0].Volume, "preseed mounts the data volume")
	assert.Equal(t, "REDIS0011-rdb-bytes", ut.capturedStdins()[0])

	// 卷钉住控制面节点（夹具 controlPlaneNode 端口恒 manager）；载体键 =
	// 卷行 id（ADR-0097：库名公式解析出行，运行时零 name）。
	vol, err := bk.repos.Volumes.GetByName(ctx, tProjectID, tDatabaseNm)
	require.NoError(t, err)
	assert.Equal(t, vol.ID, reqs[0].Volume.VolumeID, "volume mount keyed by volume row id (ADR-0097)")
	assert.Equal(t, tManagerID, vol.PinnedNodeID, "seed volume pinned to the control-plane node")

	fresh, err := bk.repos.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, fresh.RestoreFromBackup)
}

// 保留滚动（B4a 起整集删除）：窗外的行 + manifest + 全成员成对删；窗内
// 集完整保留。
func TestBackupPruneRollsRetentionWindow(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, _, store, clock, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	oldManifest, oldMember := seedSucceededBackup(t, bk, store, "01JD0BKPA0000000000000001", "postgres", "BYTES-old")
	clock.Advance(8 * 24 * time.Hour) // 旧行进入窗外（retention 7d）
	newManifest, newMember := seedSucceededBackup(t, bk, store, "01JD0BKPB0000000000000002", "postgres", "BYTES-new")

	bk.Step(ctx) // 注：库此刻已到期，调度面会另铸一行——按 ID 定向断言

	_, err := bk.repos.Backups.Get(ctx, "01JD0BKPA0000000000000001")
	assert.Error(t, err, "expired row must be pruned")
	_, err = store.Stat(ctx, oldManifest)
	assert.Error(t, err, "expired manifest must be pruned alongside its row")
	_, err = store.Stat(ctx, oldMember)
	assert.Error(t, err, "expired member object must be pruned alongside its row (whole-set delete)")

	kept, err := bk.repos.Backups.Get(ctx, "01JD0BKPB0000000000000002")
	require.NoError(t, err)
	assert.Equal(t, backup.StatusSucceeded, kept.Status)
	_, err = store.Stat(ctx, newManifest)
	assert.NoError(t, err, "fresh manifest kept")
	_, err = store.Stat(ctx, newMember)
	assert.NoError(t, err, "fresh member object kept")
}

// 启动清扫：running 行落 failed（重启打断的执行不可续传）。
func TestBackupSweepInterrupted(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, _, _, _, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	require.NoError(t, bk.repos.Backups.Create(ctx, &backup.Backup{
		ID: "01JD0BKPC0000000000000003", ProjectID: tProjectID, DatabaseID: tDatabaseID,
		Engine: "postgres", RetentionSecs: 3600,
	}))
	require.NoError(t, bk.repos.Backups.MarkRunning(ctx, "01JD0BKPC0000000000000003"))

	bk.SweepInterrupted(ctx)

	row, err := bk.repos.Backups.Get(ctx, "01JD0BKPC0000000000000003")
	require.NoError(t, err)
	assert.Equal(t, backup.StatusFailed, row.Status)
	assert.Equal(t, "interrupted by platform restart", row.Error)
}

// 对象键形态：无冒号紧凑 UTC（Windows 控制面纪律）+ 段序。
func TestBackupKeyMint(t *testing.T) {
	key := backup.KeyMint("01JPROJ", "01JDB", "01JBKP", statetest.MustParse("2026-10-04T12:34:56Z"))
	assert.Equal(t, "backups/01JPROJ/01JDB/20261004T123456Z-01JBKP", key)
	assert.NotContains(t, key, ":", "key must stay colon-free for windows control planes")
}

// 失败退避窗（2026-10-05 staging 实录收口）：失败后 backupFailureBackoff
// 内调度面不重铸行——1s tick × 无退避 = 每秒重试风暴（33h/11.4 万行台账
// 与事件垃圾）。
func TestBackupFailureBackoffWindow(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, ut, _, clock, _ := newBackupFixture(t, "postgres", url)
	ut.execErr = errors.New("boom")
	ctx := context.Background()

	clock.Advance(2 * time.Hour) // 过 interval：到期铸行 + 执行失败
	bk.Step(ctx)
	rows, err := bk.repos.Backups.ListByDatabase(ctx, tDatabaseID, 0, 50)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, backup.StatusFailed, rows[0].Status)
	assert.Len(t, ut.requests(), 1)

	clock.Advance(2 * time.Minute) // 仍到期（锚未推进），但失败未满退避窗
	bk.Step(ctx)
	rows, err = bk.repos.Backups.ListByDatabase(ctx, tDatabaseID, 0, 50)
	require.NoError(t, err)
	assert.Len(t, rows, 1, "backoff window must suppress recasting")
	assert.Len(t, ut.requests(), 1, "backoff window must suppress execution")

	clock.Advance(4 * time.Minute) // 距失败 ~6min：窗外，重铸重试
	bk.Step(ctx)
	rows, err = bk.repos.Backups.ListByDatabase(ctx, tDatabaseID, 0, 50)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "past the backoff window the scheduler recasts")
	assert.Len(t, ut.requests(), 2)
}

// 失败行保留清扫：失败行 48h 后由 prunePass 同环出清（无对象产物仅删行），
// 成功行不受失败窗影响（保留滚动另管）。
func TestBackupFailedRowSweep(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, _, store, clock, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	seedFailed := func(id string) {
		require.NoError(t, bk.repos.Backups.Create(ctx, &backup.Backup{
			ID: id, ProjectID: tProjectID, DatabaseID: tDatabaseID,
			Engine: "postgres", RetentionSecs: 604800,
		}))
		require.NoError(t, bk.repos.Backups.MarkRunning(ctx, id))
		require.NoError(t, bk.repos.Backups.FinishFailed(ctx, id, "boom"))
	}
	clock.Advance(time.Hour)
	seedFailed("01JD0FLD00000000000000000A")
	seedFailed("01JD0FLD00000000000000000B")
	seedSucceededBackup(t, bk, store, "01JD0FLD00000000000000000C", "postgres", "KEEP-ME")

	clock.Advance(49 * time.Hour)
	bk.Step(ctx) // prunePass 随步执行（库此刻也到期——step 会另铸一颗
	// 真实成功行，断言按语义：失败行必须出清、成功种子行必须在场）

	rows, err := bk.repos.Backups.ListByDatabase(ctx, tDatabaseID, 0, 50)
	require.NoError(t, err)
	ids := make(map[string]bool, len(rows))
	for _, r := range rows {
		ids[r.ID] = true
	}
	assert.False(t, ids["01JD0FLD00000000000000000A"], "stale failed row A must be swept")
	assert.False(t, ids["01JD0FLD00000000000000000B"], "stale failed row B must be swept")
	assert.True(t, ids["01JD0FLD00000000000000000C"], "succeeded row must survive the failed-row sweep")
}

// DownloadBackup 单测（IA v3 二期⑤）：只读流与台账校验——内容字节一致、
// 归属错配/非 succeeded/未装配对象仓均精确拒绝（API 授权链在其上）。
func TestDownloadBackupStreamsObjectBytes(t *testing.T) {
	bk, _, store, _, _ := newBackupFixture(t, "postgres", "postgres://u:p@db:5432/app")
	const payload = "PGDUMP-BYTES-0123456789"
	const backupID = "01JD0BKP000000000000000002"
	seedSucceededBackup(t, bk, store, backupID, "postgres", payload)

	b, reader, err := bk.DownloadBackup(context.Background(), tDatabaseID, backupID)
	require.NoError(t, err)
	defer func() { _ = reader.Close() }()
	assert.Equal(t, backupID, b.ID)

	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	assert.Equal(t, payload, string(data))
}

func TestDownloadBackupPreciseRejections(t *testing.T) {
	bk, _, store, _, stores := newBackupFixture(t, "postgres", "postgres://u:p@db:5432/app")
	const backupID = "01JD0BKP000000000000000003"
	seedSucceededBackup(t, bk, store, backupID, "postgres", "payload")

	// 归属错配按 not found（不泄漏跨库存在性）。
	_, _, err := bk.DownloadBackup(context.Background(), "01JD0DBWRONG0000000000001", backupID)
	assert.ErrorIs(t, err, state.ErrNotFound)

	// 非成功台账行不可下载。
	_, _, err = bk.DownloadBackup(context.Background(), tDatabaseID, "01JD0BKPMISSING00000001")
	assert.ErrorIs(t, err, state.ErrNotFound)

	// 对象仓未装配 = 精确失败（同一 repo 集——行在场，环境缺口是唯一差异；
	// ObjectStore 显式置 nil 接口零值——面缺席的装配形态）。
	bareDeps := satelliteDeps(t, bk.db, cipherOf(t), &fakeUtility{}, newFakeObjectStore(), Options{})
	bareDeps.ObjectStore = nil
	bare := New(bareDeps)
	bare.repos = stores
	_, _, err = bare.DownloadBackup(context.Background(), tDatabaseID, backupID)
	assert.Contains(t, err.Error(), "object store is not assembled")
}

// manifest 写序（B4a，红队 A3③）：全成员成功才写 manifest——manifest Put
// 失败时已 Put 成员被显式清理（幂等容错），台账 failed 无 ObjectKey、零
// 对象残留；「失败行清扫仅删行」假设在显式清理后成立。
func TestBackupManifestPutFailureCleansMembers(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, _, store, clock, _ := newBackupFixture(t, "postgres", url)
	store.failManifestPuts = true // 成员 Put 成功、manifest Put 失败（不读 reader）
	ctx := context.Background()

	clock.Advance(3601 * time.Second)
	bk.Step(ctx)

	rows, err := bk.repos.Backups.ListByDatabase(ctx, tDatabaseID, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, backup.StatusFailed, rows[0].Status)
	assert.Empty(t, rows[0].ObjectKey)
	assert.Contains(t, rows[0].Error, "put manifest", "failure message must point at the manifest write")
	assert.Empty(t, store.keys(), "the already-put member must be cleaned up explicitly — no orphan objects")
	fresh, err := bk.repos.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Empty(t, fresh.LastBackupAt)
}

// verify manifest 驱动逐件（B4a）：健康集 ok；成员被改写 → digest 差异
// 可诊断；成员失踪 → 读面诚实报错。
func TestVerifyBackupManifestPerMember(t *testing.T) {
	bk, _, store, _, _ := newBackupFixture(t, "postgres", "postgres://u:p@db:5432/app")
	const backupID = "01JD0BKP000000000000000005"
	manifestKey, memberKey := seedSucceededBackup(t, bk, store, backupID, "postgres", "VERIFY-PAYLOAD")

	ok, detail, err := bk.VerifyBackup(context.Background(), backupID)
	require.NoError(t, err)
	assert.True(t, ok, "a healthy manifest-anchored set verifies: %s", detail)

	// 成员改写（digest 与清单不符）。
	_, err = store.Put(context.Background(), memberKey, strings.NewReader("TAMPERED"))
	require.NoError(t, err)
	ok, detail, err = bk.VerifyBackup(context.Background(), backupID)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Contains(t, detail, "digest mismatch")
	assert.Contains(t, detail, memberKey, "diagnostics must name the mismatching member")

	// 成员失踪（读面错误——环境事实非完整性判定）。
	require.NoError(t, store.Delete(context.Background(), memberKey))
	_, _, err = bk.VerifyBackup(context.Background(), backupID)
	assert.ErrorContains(t, err, "read backup member")

	// manifest 失踪/损坏：legacy 单对象键（B4a 前旧行）走同一条诚实失败
	// 面——内容不是 manifest，parse 拒。
	_, err = store.Put(context.Background(), manifestKey, strings.NewReader("RAW-LEGACY-PAYLOAD"))
	require.NoError(t, err)
	_, _, err = bk.VerifyBackup(context.Background(), backupID)
	assert.ErrorContains(t, err, "parse backup manifest")
}

// List 孤儿对账（B4a，红队 A3③）：manifest 缺失的成员对象宽限窗（1h）外
// 登记事件 + 删除；窗内（在途执行竞态面）、manifest 在场集、legacy 单对象
// 键不在射程。
func TestOrphanPassSweepsManifestlessSets(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, _, store, _, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	// 田间在册集（manifest 在场）：保留。
	_, anchoredMember := seedSucceededBackup(t, bk, store, "01JD0ORPA0000000000000001", "postgres", "ANCHORED")

	// 孤儿集 A（manifest 缺失、成员已老于宽限窗）：回收 + 事件。
	orphanSetA := backup.KeyMint(tProjectID, tDatabaseID, "01JD0ORPB0000000000000002", bk.clock.Now())
	orphanA, err := backup.MemberKeyOf(orphanSetA, backup.DefaultMemberName)
	require.NoError(t, err)
	_, err = store.Put(ctx, orphanA, strings.NewReader("ORPHAN-OLD"))
	require.NoError(t, err)
	store.setModTime(orphanA, bk.clock.Now().Add(-2*time.Hour))

	// 孤儿集 B（manifest 缺失、成员新鲜——宽限窗内）：整集等待。
	orphanSetB := backup.KeyMint(tProjectID, tDatabaseID, "01JD0ORPC0000000000000003", bk.clock.Now())
	orphanB, err := backup.MemberKeyOf(orphanSetB, backup.DefaultMemberName)
	require.NoError(t, err)
	_, err = store.Put(ctx, orphanB, strings.NewReader("ORPHAN-FRESH"))
	require.NoError(t, err)

	// legacy 单对象键（B4a 前形态，恰四段）：不在对账射程（行驱动保留滚动
	// 的成对律承载）。
	legacyKey := backup.KeyMint(tProjectID, tDatabaseID, "01JD0ORPD0000000000000004", bk.clock.Now())
	_, err = store.Put(ctx, legacyKey, strings.NewReader("LEGACY"))
	require.NoError(t, err)
	store.setModTime(legacyKey, bk.clock.Now().Add(-2*time.Hour))

	bk.Step(ctx) // 未到期（零调度/零执行），orphanPass 随步收口

	_, err = store.Stat(ctx, orphanA)
	assert.Error(t, err, "an aged manifest-less member must be swept")
	_, err = store.Stat(ctx, orphanB)
	assert.NoError(t, err, "a fresh manifest-less member stays inside the grace window (in-flight race guard)")
	_, err = store.Stat(ctx, anchoredMember)
	assert.NoError(t, err, "an anchored set member is never swept")
	_, err = store.Stat(ctx, legacyKey)
	assert.NoError(t, err, "a legacy single-object key stays out of the reconciliation scope")

	names := eventNamesOf(t, bk, tDatabaseID)
	assert.Contains(t, names, EventBackupOrphanSwept, "the sweep must land its event")
	for _, ev := range outboxRows(t, bk) {
		if ev.Name == EventBackupOrphanSwept {
			assert.Contains(t, string(ev.Payload), orphanA, "sweep event payload carries the swept key")
			assert.NotContains(t, string(ev.Payload), orphanB, "fresh members are not part of the sweep fact")
		}
	}
}

// 宽限窗边界：恰好龄满（== 1h）仍在窗内（严格大于才动手——多留不少留的
// 诚实方向）；窗外一秒即回收。
func TestOrphanPassGraceBoundary(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, _, store, _, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	edgeSet := backup.KeyMint(tProjectID, tDatabaseID, "01JD0ORPE0000000000000005", bk.clock.Now())
	edgeKey, err := backup.MemberKeyOf(edgeSet, backup.DefaultMemberName)
	require.NoError(t, err)
	overSet := backup.KeyMint(tProjectID, tDatabaseID, "01JD0ORPF0000000000000006", bk.clock.Now())
	overKey, err := backup.MemberKeyOf(overSet, backup.DefaultMemberName)
	require.NoError(t, err)
	_, err = store.Put(ctx, edgeKey, strings.NewReader("EDGE"))
	require.NoError(t, err)
	_, err = store.Put(ctx, overKey, strings.NewReader("OVER"))
	require.NoError(t, err)
	store.setModTime(edgeKey, bk.clock.Now().Add(-time.Hour))
	store.setModTime(overKey, bk.clock.Now().Add(-time.Hour-time.Second))

	bk.Step(ctx)

	_, err = store.Stat(ctx, edgeKey)
	assert.NoError(t, err, "exactly at the grace boundary the set still waits")
	_, err = store.Stat(ctx, overKey)
	assert.Error(t, err, "one second past the grace window the set is swept")
}

// 孤儿回收的幂等重试面：删除失败不阻断（下拍 List 重扫再命中——集仍无
// manifest）。
func TestOrphanPassRetriesAfterPartialDeleteFailure(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	bk, _, store, _, _ := newBackupFixture(t, "postgres", url)
	ctx := context.Background()

	orphanSet := backup.KeyMint(tProjectID, tDatabaseID, "01JD0ORPG0000000000000007", bk.clock.Now())
	memberA, err := backup.MemberKeyOf(orphanSet, backup.DefaultMemberName)
	require.NoError(t, err)
	memberB, err := backup.MemberKeyOf(orphanSet, "extra-stream.tgz") // 多成员集（B4b 形态预演）
	require.NoError(t, err)
	for _, key := range []string{memberA, memberB} {
		_, err = store.Put(ctx, key, strings.NewReader("ORPHAN-MEMBER"))
		require.NoError(t, err)
		store.setModTime(key, bk.clock.Now().Add(-2*time.Hour))
	}

	bk.Step(ctx)
	_, err = store.Stat(ctx, memberA)
	assert.Error(t, err, "both members of the aged orphan set are swept")
	_, err = store.Stat(ctx, memberB)
	assert.Error(t, err)
}
