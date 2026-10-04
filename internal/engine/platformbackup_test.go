package engine

// Platform Backup 测试（F2.2，ADR-0039 决策 9 验收锚）：节拍锚语义、
// SQLite VACUUM INTO 一致快照真跑、仓密铸造/复用与排除语义（密钥在
// keys/ 下而备份集含 keys/——排除规则在 argv 断言）、双仓 argv/env 面、
// forget 保留口径、缺席降级（restic 二进制缺席 = 停用非报错）。restic
// 子进程经 resticExec 函数值缝注入假面（跨平台可测；真机全链由 dind
// 演练承担）。

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/state/statertest"
)

// fakeRestic 是 restic 子进程假面（记录调用；snapshots 可编程）。
type fakeRestic struct {
	mu       sync.Mutex
	calls    [][]string
	envs     []map[string]string
	execErr  error
	snapsOut string
}

func withFakeRestic(t *testing.T, cfg *PlatformBackupConfig) (*Engine, *fakeRestic, *statertest.FakeClock) {
	t.Helper()
	fr := &fakeRestic{snapsOut: "[]"}
	orig := resticExec
	resticExec = func(_ context.Context, _ string, args []string, env []string) ([]byte, error) {
		fr.mu.Lock()
		defer fr.mu.Unlock()
		fr.calls = append(fr.calls, args)
		envMap := map[string]string{}
		for _, kv := range env {
			if k, v, ok := strings.Cut(kv, "="); ok {
				envMap[k] = v
			}
		}
		fr.envs = append(fr.envs, envMap)
		if fr.execErr != nil {
			return []byte("fake restic failure detail"), fr.execErr
		}
		for _, a := range args {
			if a == "snapshots" {
				return []byte(fr.snapsOut), nil
			}
		}
		return []byte("ok"), nil
	}
	t.Cleanup(func() { resticExec = orig })

	db, clock := statertest.New(t)
	dataRoot := t.TempDir()
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Logger: discardLogger()}, Options{
		DataRoot:       dataRoot,
		ResticPath:     "fake-restic", // 缝在场：二进制探测面恒可用
		PlatformBackup: cfg,
	})
	return e, fr, clock
}

func TestPlatformBackupDueAnchor(t *testing.T) {
	e, _, clock := withFakeRestic(t, &PlatformBackupConfig{
		Interval: time.Hour, Retention: 7 * 24 * time.Hour,
	})
	require.True(t, e.platformBackupDue(*e.opts.PlatformBackup), "no anchor yet: first run is due")

	require.NoError(t, os.MkdirAll(e.platformBackupPath(""), 0o750))
	require.NoError(t, os.WriteFile(e.platformBackupPath(platformLastRunFile),
		[]byte(clock.Now().Format(time.RFC3339)), 0o600))
	require.False(t, e.platformBackupDue(*e.opts.PlatformBackup), "fresh anchor: not due")

	clock.Advance(2 * time.Hour)
	require.True(t, e.platformBackupDue(*e.opts.PlatformBackup), "past the interval: due again")
}

// 全链：快照 → 本地仓 backup/check/forget → 锚推进 + 成功事件。
func TestPlatformBackupRunSucceeds(t *testing.T) {
	e, fr, _ := withFakeRestic(t, &PlatformBackupConfig{
		Interval: time.Hour, Retention: 7 * 24 * time.Hour,
	})
	ctx := context.Background()

	require.NoError(t, e.runPlatformBackup(ctx, *e.opts.PlatformBackup))

	// SQLite 一致快照真落盘（VACUUM INTO 非空文件）。
	snapshot := e.platformBackupPath(platformSnapshotName)
	st, err := os.Stat(snapshot)
	require.NoError(t, err)
	assert.Greater(t, st.Size(), int64(0), "VACUUM INTO must produce a real snapshot file")

	calls := fr.snapshotCalls()
	require.Len(t, calls, 4, "local repo: init + backup + check + forget")
	// argv 前缀恒 ["-r", repo]；动作段在 [2:]。
	assert.Equal(t, []string{"init"}, calls[0][2:], "first use initializes the repository")
	assert.Equal(t, []string{"backup", e.opts.DataRoot}, calls[1][2:4], "backup targets the data root")
	assert.Contains(t, strings.Join(calls[1], " "), "--exclude")
	assert.Contains(t, strings.Join(calls[1], " "), "fleetly.db", "live sqlite files are excluded in favor of the snapshot")
	assert.Contains(t, strings.Join(calls[1], " "), "platform-backups", "the repo directory itself is excluded")
	assert.Contains(t, strings.Join(calls[1], " "), platformRepoKeyFile, "the repo password key is excluded (self-lock circularity, ADR-0039 decision 9)")
	assert.Equal(t, []string{"check"}, calls[2][2:], "verify after every snapshot")
	assert.Equal(t, []string{"forget", "--keep-within", "168h", "--prune"}, calls[3][2:], "retention rides forget --keep-within with prune")

	// 节拍锚推进。
	anchor, err := os.ReadFile(e.platformBackupPath(platformLastRunFile))
	require.NoError(t, err)
	_, err = time.Parse(time.RFC3339, string(anchor))
	require.NoError(t, err)

	// 仓密铸造 + env 承载（RESTIC_PASSWORD 不落 argv）。
	keyPath := filepath.Join(e.opts.DataRoot, "keys", platformRepoKeyFile)
	raw, err := os.ReadFile(keyPath) //nolint:gosec // G304：测试夹具路径
	require.NoError(t, err)
	assert.Len(t, string(raw), 64, "repo password is 32-byte hex")
	assert.Equal(t, string(raw), fr.envs[0]["RESTIC_PASSWORD"], "repo password rides subprocess env, never argv")
	for _, args := range fr.calls {
		assert.NotContains(t, strings.Join(args, " "), string(raw), "password never appears in argv")
	}
}

// s3 在场：双仓（本地 + s3），凭证经 AWS_* env。
func TestPlatformBackupS3SecondRepo(t *testing.T) {
	e, fr, _ := withFakeRestic(t, &PlatformBackupConfig{
		Interval: time.Hour, Retention: 24 * time.Hour,
		S3: &S3RepoConfig{Endpoint: "s3.example.com", Bucket: "fleetly", Prefix: "platform",
			AccessKeyID: "ak", SecretAccessKey: "sk"}, //nolint:gosec // 测试样本值
	})
	ctx := context.Background()
	require.NoError(t, e.runPlatformBackup(ctx, *e.opts.PlatformBackup))

	fr.mu.Lock()
	defer fr.mu.Unlock()
	require.Len(t, fr.calls, 8, "two repos x (init + backup + check + forget)")
	assert.Contains(t, fr.calls[4][1], "s3:s3.example.com/fleetly/platform", "s3 repo endpoint from config")
	assert.Equal(t, "ak", fr.envs[4]["AWS_ACCESS_KEY_ID"])
	assert.Equal(t, "sk", fr.envs[4]["AWS_SECRET_ACCESS_KEY"])
}

// 失败链路：事件 + 锚不推进（失败下拍重试）。
func TestPlatformBackupFailureRecorded(t *testing.T) {
	e, fr, _ := withFakeRestic(t, &PlatformBackupConfig{
		Interval: time.Hour, Retention: 24 * time.Hour,
	})
	fr.execErr = context.DeadlineExceeded
	ctx := context.Background()

	err := e.runPlatformBackup(ctx, *e.opts.PlatformBackup)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fake restic failure detail")
	_, aerr := os.ReadFile(e.platformBackupPath(platformLastRunFile))
	assert.Error(t, aerr, "anchor must not advance on failure")
}

// 缺席降级：restic 二进制不在（ResticPath 空 + LookPath 失败环境）→
// Available=false，due 恒假（停用非报错——数据库轨不受影响）。
func TestPlatformBackupUnavailableDegrades(t *testing.T) {
	db, _ := statertest.New(t)
	e := New(Deps{DB: db, Runtime: newFakeRuntime(), Logger: discardLogger()}, Options{
		DataRoot:       t.TempDir(),
		ResticPath:     "", // 无缝 = 走 LookPath；测试环境无 restic
		PlatformBackup: &PlatformBackupConfig{Interval: time.Hour, Retention: 24 * time.Hour},
	})
	if _, err := resticBinary(); err == nil {
		t.Skip("restic present on this machine; degradation path needs its absence")
	}
	assert.False(t, e.PlatformBackupAvailable())
	assert.False(t, e.platformBackupDue(*e.opts.PlatformBackup), "unavailable binary: never due")
}

// 手动触发（F2.3，ADR-0039 决策 9/10）：同步执行 + 最新快照回读 + 成败
// 事件与审计一拍落账；未配置面精确失败。触发不走节拍锚（直接执行）。
func TestTriggerPlatformBackupManual(t *testing.T) {
	ctx := context.Background()
	cfg := &PlatformBackupConfig{Interval: time.Hour, Retention: 24 * time.Hour}

	t.Run("succeeds and returns the newest snapshot", func(t *testing.T) {
		e, fr, _ := withFakeRestic(t, cfg)
		// 仓里已有两条 + 本次新铸一条（假面 snapshots 固定回读集）。
		fr.snapsOut = `[{"short_id":"aaaa1111","time":"2026-10-04T10:00:00Z","hostname":"cp"},
{"short_id":"bbbb2222","time":"2026-10-04T11:00:00Z","hostname":"cp"}]`
		snap, err := e.TriggerPlatformBackup(ctx)
		require.NoError(t, err)
		assert.Equal(t, "bbbb2222", snap.ID, "the newest snapshot rides the trigger response")
		assert.Equal(t, "cp", snap.Hostname)
		// 事件 + 审计一拍落账（成功形态）。
		names := eventNames(t, e, "platform-backup")
		assert.Contains(t, names, eventPlatformBackupOK)
		assert.NotContains(t, names, eventPlatformBackupFail)
		assert.Contains(t, auditActions(t, e, "platform/"), "platform.backup_trigger")
	})

	t.Run("chain failure surfaces the error and the failed event", func(t *testing.T) {
		e, fr, _ := withFakeRestic(t, cfg)
		fr.execErr = context.DeadlineExceeded
		_, err := e.TriggerPlatformBackup(ctx)
		require.Error(t, err)
		names := eventNames(t, e, "platform-backup")
		assert.Contains(t, names, eventPlatformBackupFail, "the failed run is a recorded fact")
		assert.Contains(t, auditActions(t, e, "platform/"), "platform.backup_trigger")
	})

	t.Run("not configured is a precise failure", func(t *testing.T) {
		e, _, _ := withFakeRestic(t, nil)
		_, err := e.TriggerPlatformBackup(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not configured")
	})

	t.Run("empty repo after success is an invariant violation", func(t *testing.T) {
		e, fr, _ := withFakeRestic(t, cfg)
		fr.snapsOut = `[]`
		_, err := e.TriggerPlatformBackup(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no snapshots")
	})
}

// 列举面：新→旧（time 降序——分页游标的确定性前提）。
func TestListPlatformSnapshotsNewestFirst(t *testing.T) {
	e, fr, _ := withFakeRestic(t, &PlatformBackupConfig{Interval: time.Hour, Retention: 24 * time.Hour})
	fr.snapsOut = `[{"short_id":"old00001","time":"2026-10-01T00:00:00Z","hostname":"cp"},
{"short_id":"new00002","time":"2026-10-04T00:00:00Z","hostname":"cp"},
{"short_id":"mid00003","time":"2026-10-02T00:00:00Z","hostname":"cp"}]`
	snaps, err := e.ListPlatformSnapshots(context.Background())
	require.NoError(t, err)
	require.Len(t, snaps, 3)
	assert.Equal(t, "new00002", snaps[0].ID)
	assert.Equal(t, "mid00003", snaps[1].ID)
	assert.Equal(t, "old00001", snaps[2].ID)
}

// 列举面：restic snapshots --json 直读。
func TestPlatformBackupListSnapshots(t *testing.T) {
	e, fr, _ := withFakeRestic(t, &PlatformBackupConfig{
		Interval: time.Hour, Retention: 24 * time.Hour,
	})
	fr.mu.Lock()
	fr.snapsOut = `[{"short_id":"abc12345","time":"2026-10-04T00:00:00Z","hostname":"mgr","tags":["folder:/data"]}]`
	fr.mu.Unlock()
	snaps, err := e.ListPlatformSnapshots(context.Background())
	require.NoError(t, err)
	require.Len(t, snaps, 1)
	assert.Equal(t, "abc12345", snaps[0].ID)

	// JSON 形态与 PlatformSnapshot 字段对账（restic wire 契约钉板）。
	var probes []PlatformSnapshot
	require.NoError(t, json.Unmarshal([]byte(fr.snapsOut), &probes))
	require.Len(t, probes, 1)
	assert.Equal(t, "mgr", probes[0].Hostname)
}

// systemd 形态环境（HOME/XDG_CACHE_HOME 双缺）补丁：XDG_CACHE_HOME 钉进
// 域内缓存目录 + 目录真建 + 缓存路径进备份排除集（2026-10-04 staging
// 实录锚——restic "unable to locate cache directory" 拒跑面）。
func TestResticEnvSystemdCacheDir(t *testing.T) {
	e, fr, _ := withFakeRestic(t, &PlatformBackupConfig{Interval: time.Hour, Retention: 24 * time.Hour})
	repo := resticRepo{repo: "/r", env: map[string]string{"RESTIC_PASSWORD": "pw"}}

	t.Run("homeless env gets XDG_CACHE_HOME pinned into the domain", func(t *testing.T) {
		env := e.resticEnvFrom([]string{"PATH=/usr/bin"}, repo)
		want := "XDG_CACHE_HOME=" + e.platformBackupPath(resticCacheSubdir)
		assert.Contains(t, env, want)
		st, err := os.Stat(e.platformBackupPath(resticCacheSubdir))
		require.NoError(t, err)
		assert.True(t, st.IsDir(), "cache directory is really created")
	})

	t.Run("env carrying HOME is passed through untouched", func(t *testing.T) {
		env := e.resticEnvFrom([]string{"PATH=/usr/bin", "HOME=/root"}, repo)
		for _, kv := range env {
			assert.NotContains(t, kv, "XDG_CACHE_HOME=", "HOME already locates a cache dir; no override")
		}
	})

	t.Run("backup excludes the cache directory", func(t *testing.T) {
		ctx := context.Background()
		require.NoError(t, e.runPlatformBackup(ctx, *e.opts.PlatformBackup))
		calls := fr.snapshotCalls()
		require.Len(t, calls, 4)
		assert.Contains(t, strings.Join(calls[1], " "), e.platformBackupPath(resticCacheSubdir),
			"cache dir must ride the backup exclusion set")
	})
}

// snapshotCalls 返回假面调用快照（argv 数组拷贝）。
func (f *fakeRestic) snapshotCalls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.calls))
	copy(out, f.calls)
	return out
}
