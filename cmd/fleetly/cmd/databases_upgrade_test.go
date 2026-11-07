package cmd

// databases upgrade 向导单测（B7，ADR-0094 决策 5 / design §2.4）：六步编排
// 的断点重入与独立幂等——upgradeRPC 窄面 fake 驱动（服务端编排行不存在的
// 立法兑现：状态即平台行，fake 即平台行集）。覆盖：全跑序 / ③ 后重入
//（源行缺席、中间行在场）/ ② 验证失败止步（源库零触碰）/ 已在目标版本
// 拒。CLI 人类面 golden 另测（databases_upgrade_golden_test.go）。

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/lynxland/commands"
	"google.golang.org/protobuf/proto"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
)

// fakeUpgradeRPC 是 upgradeRPC 的平台行集 fake：库/备份/卷行 + 票店 +
// 调用流水。Create 即 running（状态推进由收敛环承载，不在向导射程——
// fake 直接给终态）；TriggerBackup 即铸 succeeded 行。
type fakeUpgradeRPC struct {
	projectID    string
	nextID       int
	dbs          []*structurev1.Database // 活跃行集
	backups      map[string][]*structurev1.Backup
	volumes      []*structurev1.Volume
	pending      map[string]bool // 恢复挂起中的库（create 时一次性注入再清）
	tickets      map[string]string
	calls        []string
	failVerif    string // VerifyBackup 对该备份报 not ok
	failVerifAny bool   // VerifyBackup 全失败（② 止步腿——备份 id 预知不可得）
}

func newFakeUpgradeRPC(projectID string) *fakeUpgradeRPC {
	return &fakeUpgradeRPC{
		projectID: projectID, nextID: 100,
		backups: map[string][]*structurev1.Backup{},
		pending: map[string]bool{},
		tickets: map[string]string{},
	}
}

func (f *fakeUpgradeRPC) log(format string, a ...any) {
	f.calls = append(f.calls, fmt.Sprintf(format, a...))
}

func (f *fakeUpgradeRPC) mint() string {
	f.nextID++
	return fmt.Sprintf("01kfake%019d", f.nextID) // 7+19=26 位 crockford 小写形（resolveSource 的 id 直通判据——i/l/o/u 不入字母表）
}

func (f *fakeUpgradeRPC) GetDatabase(_ context.Context, in *structurev1.GetDatabaseRequest) (*structurev1.GetDatabaseResponse, error) {
	for _, d := range f.dbs {
		if d.GetId() == in.GetId() {
			clone := proto.Clone(d).(*structurev1.Database) // proto 消息持锁——浅拷贝违 copylocks。
			if f.pending[d.GetId()] {
				clone.RestoreFromBackup = "pending"
				clone.Status = "pending"
				f.pending[d.GetId()] = false // 下一拍恢复完成（收敛推进的 fake 面）。
			}
			return &structurev1.GetDatabaseResponse{Database: clone}, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", in.GetId())
}

func (f *fakeUpgradeRPC) ListDatabases(_ context.Context, in *structurev1.ListDatabasesRequest) (*structurev1.ListDatabasesResponse, error) {
	out := &structurev1.ListDatabasesResponse{}
	for _, d := range f.dbs {
		if d.GetProjectId() == in.GetProjectId() {
			out.Databases = append(out.Databases, proto.Clone(d).(*structurev1.Database)) // proto 消息持锁——浅拷贝违 copylocks。
		}
	}
	return out, nil
}

func (f *fakeUpgradeRPC) CreateDatabase(_ context.Context, in *structurev1.CreateDatabaseRequest) (*structurev1.CreateDatabaseResponse, error) {
	f.log("create %s engine=%s version=%s restore=%s", in.GetName(), in.GetEngine(), in.GetVersion(), in.GetRestoreFromBackup())
	id := f.mint()
	d := &structurev1.Database{
		Id: id, ProjectId: in.GetProjectId(), Name: in.GetName(),
		Engine: in.GetEngine(), Version: in.GetVersion(),
		Status: "running", Host: "db-" + strings.ToLower(id), Port: 5432,
	}
	if in.GetRestoreFromBackup() != "" {
		f.pending[id] = true // 首拍恢复在途，GetDatabase 下一拍清位。
	}
	f.dbs = append(f.dbs, d)
	// 数据库挂靠卷随行出生（ensureDatabaseVolume 的 fake 面）。
	f.volumes = append(f.volumes, &structurev1.Volume{Id: f.mint(), ProjectId: in.GetProjectId(), Name: in.GetName()})
	return &structurev1.CreateDatabaseResponse{Database: d}, nil
}

func (f *fakeUpgradeRPC) DeleteDatabase(_ context.Context, in *structurev1.DeleteDatabaseRequest) (*structurev1.DeleteDatabaseResponse, error) {
	f.log("delete %s", in.GetId())
	for i, d := range f.dbs {
		if d.GetId() == in.GetId() {
			f.dbs = append(f.dbs[:i], f.dbs[i+1:]...)
			return &structurev1.DeleteDatabaseResponse{}, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", in.GetId())
}

func (f *fakeUpgradeRPC) TriggerBackup(_ context.Context, in *structurev1.TriggerBackupRequest) (*structurev1.TriggerBackupResponse, error) {
	f.log("trigger-backup %s", in.GetDatabaseId())
	b := &structurev1.Backup{Id: "bk" + f.mint(), DatabaseId: in.GetDatabaseId(), Status: "succeeded", EngineVersion: f.versionOf(in.GetDatabaseId())}
	f.backups[in.GetDatabaseId()] = append([]*structurev1.Backup{b}, f.backups[in.GetDatabaseId()]...)
	return &structurev1.TriggerBackupResponse{Backup: b}, nil
}

func (f *fakeUpgradeRPC) versionOf(dbID string) string {
	for _, d := range f.dbs {
		if d.GetId() == dbID {
			return d.GetVersion()
		}
	}
	return ""
}

func (f *fakeUpgradeRPC) ListBackups(_ context.Context, in *structurev1.ListBackupsRequest) (*structurev1.ListBackupsResponse, error) {
	out := &structurev1.ListBackupsResponse{}
	out.Backups = append(out.Backups, f.backups[in.GetDatabaseId()]...)
	return out, nil
}

func (f *fakeUpgradeRPC) VerifyBackup(_ context.Context, in *structurev1.VerifyBackupRequest) (*structurev1.VerifyBackupResponse, error) {
	f.log("verify %s", in.GetBackupId())
	if f.failVerifAny || in.GetBackupId() == f.failVerif {
		return &structurev1.VerifyBackupResponse{Ok: false, Error: "digest mismatch"}, nil
	}
	return &structurev1.VerifyBackupResponse{Ok: true}, nil
}

func (f *fakeUpgradeRPC) ListVolumes(_ context.Context, in *structurev1.ListVolumesRequest) (*structurev1.ListVolumesResponse, error) {
	out := &structurev1.ListVolumesResponse{}
	for _, v := range f.volumes {
		if v.GetProjectId() == in.GetProjectId() {
			out.Volumes = append(out.Volumes, proto.Clone(v).(*structurev1.Volume)) // proto 消息持锁——浅拷贝违 copylocks。
		}
	}
	return out, nil
}

func (f *fakeUpgradeRPC) PreviewPurgeVolume(_ context.Context, in *structurev1.PreviewPurgeVolumeRequest) (*structurev1.PreviewPurgeVolumeResponse, error) {
	for _, v := range f.volumes {
		if v.GetId() == in.GetId() {
			ticket := "tk" + f.mint()
			f.tickets[ticket] = v.GetId()
			return &structurev1.PreviewPurgeVolumeResponse{Name: v.GetName(), CarrierKey: v.GetId(), Ticket: ticket}, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", in.GetId())
}

func (f *fakeUpgradeRPC) PurgeVolume(_ context.Context, in *structurev1.PurgeVolumeRequest) (*structurev1.PurgeVolumeResponse, error) {
	volID, ok := f.tickets[in.GetConfirmation().GetTicket()]
	if !ok || volID != in.GetId() {
		return nil, fmt.Errorf("E_CONFIRMATION_MISMATCH")
	}
	f.log("purge %s", in.GetId())
	for i, v := range f.volumes {
		if v.GetId() == in.GetId() {
			f.volumes = append(f.volumes[:i], f.volumes[i+1:]...)
			return &structurev1.PurgeVolumeResponse{Name: v.GetName(), CarrierKey: v.GetId()}, nil
		}
	}
	return nil, fmt.Errorf("not found: %s", in.GetId())
}

// seedSource 播源库 + 挂靠卷 + 一颗 succeeded 备份（版本记录在册）。
// 回显 (源行, 旧卷行)。
func (f *fakeUpgradeRPC) seedSource(name, version string) (*structurev1.Database, *structurev1.Volume) {
	d := &structurev1.Database{
		Id: f.mint(), ProjectId: f.projectID, Name: name,
		Engine: "postgres", Version: version, Status: "running",
		Host: "db-" + name, Port: 5432,
	}
	f.dbs = append(f.dbs, d)
	vol := &structurev1.Volume{Id: f.mint(), ProjectId: f.projectID, Name: name}
	f.volumes = append(f.volumes, vol)
	f.backups[d.GetId()] = []*structurev1.Backup{
		{Id: "bk-old", DatabaseId: d.GetId(), Status: "succeeded", EngineVersion: version},
	}
	return d, vol
}

// newTestWizard 组装向导（毫秒级轮询）。
func newTestWizard(rpc upgradeRPC, projectID, ref, target string) *upgradeWizard {
	return &upgradeWizard{
		rpc: rpc, projectID: projectID, sourceRef: ref, targetVer: target,
		now: time.Now, pollEvery: time.Millisecond,
		printStep: func(string, ...any) {},
	}
}

// captureEnv 是 execute 的最小环境（丢弃输出——断言面在 fake 调用流水）。
func captureEnv() *commands.Environment {
	return &commands.Environment{Stdout: io.Discard, Stderr: io.Discard}
}

func TestUpgradeWizardFullRun(t *testing.T) {
	f := newFakeUpgradeRPC("p1")
	src, oldVol := f.seedSource("pgshop", "17")
	w := newTestWizard(f, "p1", src.GetId(), "18")

	plan, err := w.composePlan(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "17", plan.FromVersion)
	assert.Equal(t, "18", plan.ToVersion)
	assert.Equal(t, "bk-old", plan.RestoreSource)
	assert.False(t, plan.Resumed)
	require.Len(t, plan.PurgeList, 2)
	require.Len(t, plan.Steps, 6)

	require.NoError(t, w.execute(context.Background(), captureEnv(), false, plan))

	// 平台行终态：源名重建行在目标版本；源行与中间行均不在；两卷行均已
	// purge（容量回收）。
	var final *structurev1.Database
	for _, d := range f.dbs {
		assert.NotEqual(t, src.GetId(), d.GetId(), "the source row id must not survive - recreate mints a new row")
		if d.GetName() == "pgshop" {
			final = d
		}
	}
	require.NotNil(t, final, "the recreated row must exist")
	assert.Equal(t, "18", final.GetVersion())
	for _, v := range f.volumes {
		assert.NotEqual(t, oldVol.GetId(), v.GetId(), "the old data volume row must be purged (the recreated row mints a fresh volume)")
		assert.NotContains(t, v.GetName(), "-upgrade-", "the intermediate volume row must be purged")
	}
	// 调用序锚：先建中间（带 ① 源备份）→ 强制备份（中间）→ verify → 删源
	// → purge → 重建（带 ② 备份）→ 删中间 → purge。
	joined := strings.Join(f.calls, "\n")
	assert.Contains(t, joined, "restore=bk-old")
	recreate := strings.Index(joined, "create pgshop engine=postgres version=18")
	forced := strings.Index(joined, "trigger-backup")
	require.GreaterOrEqual(t, recreate, 0)
	assert.Greater(t, recreate, forced, "the recreate must follow the forced backup (explicit step-2 source)")
	assert.Contains(t, joined, "delete "+src.GetId())
	assert.Equal(t, 2, strings.Count(joined, "purge "), "two volume purges (source at step 3 + intermediate at step 6)")
}

func TestUpgradeWizardResumeAfterStep3(t *testing.T) {
	f := newFakeUpgradeRPC("p1")
	f.seedSource("pgshop", "17")
	// 手工跑到 ③ 后：源行删、源卷 purge、中间行在（版本 18）+ 其 ② 备份。
	inter := &structurev1.Database{
		Id: f.mint(), ProjectId: "p1", Name: "pgshop-upgrade-1000",
		Engine: "postgres", Version: "18", Status: "running", Host: "db-x", Port: 5432,
	}
	f.dbs = append(f.dbs, inter)
	f.volumes = append(f.volumes, &structurev1.Volume{Id: f.mint(), ProjectId: "p1", Name: inter.GetName()})
	f.backups[inter.GetId()] = []*structurev1.Backup{
		{Id: "bk-step2", DatabaseId: inter.GetId(), Status: "succeeded", EngineVersion: "18"},
	}
	f.dbs = f.dbs[:0]
	f.volumes = f.volumes[1:] // 源行 + 源卷清场（只留中间行/卷）。
	f.dbs = append(f.dbs, inter)

	// 重入锚：名形态 + --project（源行已 tombstone，id 形态无锚）。
	w := newTestWizard(f, "p1", "pgshop", "18")
	plan, err := w.composePlan(context.Background())
	require.NoError(t, err)
	assert.True(t, plan.Resumed)

	require.NoError(t, w.execute(context.Background(), captureEnv(), false, plan))
	joined := strings.Join(f.calls, "\n")
	assert.NotContains(t, joined, "create pgshop-upgrade-", "step 1 must be skipped on resume")
	assert.NotContains(t, joined, "trigger-backup", "step 2 must be skipped (the succeeded backup exists)")
	assert.Contains(t, joined, "restore=bk-step2", "the recreate must use the step-2 backup explicitly")
	assert.Contains(t, joined, "delete "+inter.GetId(), "step 6 must clean up the intermediate")
	assert.Equal(t, 1, strings.Count(joined, "purge "), "only the intermediate volume purge remains")
}

func TestUpgradeWizardStopsOnVerifyFailure(t *testing.T) {
	f := newFakeUpgradeRPC("p1")
	src, _ := f.seedSource("pgshop", "17")
	f.failVerifAny = true // ② 的强制备份验证失败（止步腿——备份 id 预知不可得，全失败注入）
	w := newTestWizard(f, "p1", src.GetId(), "18")
	plan, err := w.composePlan(context.Background())
	require.NoError(t, err)

	err = w.execute(context.Background(), captureEnv(), false, plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verification FAILED")
	// 源库零触碰（红队 A1 的守护面：验证不过不动源——源行仍在场）。
	alive := false
	for _, d := range f.dbs {
		if d.GetId() == src.GetId() {
			alive = true
		}
	}
	assert.True(t, alive, "the source row must survive a failed verification")
	assert.NotContains(t, strings.Join(f.calls, "\n"), "delete "+src.GetId())
}

func TestUpgradeWizardAlreadyOnTarget(t *testing.T) {
	f := newFakeUpgradeRPC("p1")
	f.seedSource("pgshop", "18")
	w := newTestWizard(f, "p1", "pgshop", "18")
	_, err := w.composePlan(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already runs version 18")
}
