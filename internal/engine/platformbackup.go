package engine

// Platform Backup 执行链（F2.2，ADR-0039 决策 9）：restic 快照控制面
// 数据根（SQLite VACUUM INTO 一致快照 + keys/ + config + backups/ +
// uploads/；排除 platform-backups/ 自身与仓密钥——仓密进仓自锁的循环
// 依赖，runbook 提示离机保管）。本地仓恒在；s3 在场追加外置仓（restic
// 原生 S3 backend）。保留 = restic forget --keep-within（双仓同策略）；
// 校验 = 每次成功后 restic check。restic 二进制缺席 = Platform Backup
// 停用（railpack"失败不阻断"先例），数据库备份轨不受影响。
//
// 节拍锚 = DataRoot/platform-backups/last-run（文件事实，重启安全）；
// 手动触发（F2.3 升级序消费的 API 动词随该批落地）直接执行不走锚。

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// PinnedResticVersion 是 restic 钉版（ADR-0021 口径；2026-10 现行稳定，
// install.sh 同版本下载——guards TestResticPinConstantAndInstallerAgree
// 静态执法两处一致）。
const PinnedResticVersion = "0.19.1"

// Platform Backup 的数据根内布局（platformBackupDir 相对 DataRoot）。
const (
	platformBackupDir    = "platform-backups"
	platformRepoSubdir   = "restic"              // 本地仓（restic repo）
	platformSnapshotName = "state-snapshot.db"   // SQLite VACUUM INTO 落点
	platformLastRunFile  = "last-run"            // 节拍锚（RFC3339）
	platformRepoKeyFile  = "platform-backup.key" // 仓密（keys/ 下，0600，排除备份集）
)

// PlatformBackupConfig 是执行链的配置快照（assembly 从 AppConfig 注入；
// interval/retention 缺省在 config 访问器，s3 nil = 仅本地仓）。
type PlatformBackupConfig struct {
	Interval  time.Duration
	Retention time.Duration
	S3        *S3RepoConfig
}

// S3RepoConfig 是外置仓端点面。
type S3RepoConfig struct {
	Endpoint        string
	Bucket          string
	Prefix          string
	AccessKeyID     string
	SecretAccessKey string
}

// resticBinary 探测可执行文件（函数值缝：测试注入假 restic；nil = 生产
// exec.LookPath——缺席即停用的探测面）。
func resticBinary() (string, error) {
	return exec.LookPath("restic")
}

// resticExec 是 restic 子进程执行缝（nil = 生产 exec；测试注入假面记录
// argv/env 断言链路——跨平台可测，railpack 二进制探测 seam 同款理由）。
var resticExec = func(ctx context.Context, bin string, args []string, env []string) ([]byte, error) {
	// nolint:gosec // G204 变量子进程的信任域在钉版链：bin 是 PinnedResticVersion
	// 对应的安装产物（install.sh 钉版下载 + guards 静态对账），args 全部平台铸造。
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	return cmd.CombinedOutput()
}

// PlatformBackupAvailable 报告 restic 二进制在场（doctor/装配期观测面）。
func (e *Engine) PlatformBackupAvailable() bool {
	if e.opts.ResticPath != "" {
		return true
	}
	_, err := resticBinary()
	return err == nil
}

// platformBackupDue 报告节拍到点（锚文件缺席 = 首拍立即）。
func (e *Engine) platformBackupDue(cfg PlatformBackupConfig) bool {
	if !e.PlatformBackupAvailable() {
		return false
	}
	anchor, err := os.ReadFile(e.platformBackupPath(platformLastRunFile))
	if err != nil {
		return true // 无锚 = 从未跑过 → 立即
	}
	t, err := time.Parse(time.RFC3339, string(anchor))
	if err != nil {
		return true // 锚损坏 = 诚实重跑（幂等：快照去重）
	}
	return e.clock.Now().Sub(t) >= cfg.Interval
}

// runPlatformBackup 执行一次 Platform Backup（快照 → 双仓 backup →
// check → forget；s3 仓失败不阻断本地仓完成事实——事件按整体成败落）。
func (e *Engine) runPlatformBackup(ctx context.Context, cfg PlatformBackupConfig) error {
	if !e.PlatformBackupAvailable() {
		return fmt.Errorf("platform backup: restic binary not found (pin %s); install it or disable platform backup", PinnedResticVersion)
	}
	if err := e.snapshotState(ctx); err != nil {
		return fmt.Errorf("platform backup: sqlite snapshot: %w", err)
	}
	repos, err := e.platformRepos()
	if err != nil {
		return err
	}
	var firstErr error
	for _, repo := range repos {
		if err := e.resticRun(ctx, repo, "backup", e.opts.DataRoot,
			"--exclude", filepath.Join(e.opts.DataRoot, "fleetly.db"),
			"--exclude", filepath.Join(e.opts.DataRoot, "fleetly.db-wal"),
			"--exclude", filepath.Join(e.opts.DataRoot, "fleetly.db-shm"),
			"--exclude", e.platformBackupPath(platformRepoSubdir)); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := e.resticRun(ctx, repo, "check"); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := e.resticRun(ctx, repo, "forget", "--keep-within",
			cfg.Retention.String(), "--prune"); err != nil {
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if firstErr != nil {
		return firstErr
	}
	// 节拍锚只在全链成功推进（失败下拍重试）。
	return os.WriteFile(e.platformBackupPath(platformLastRunFile),
		[]byte(e.clock.Now().UTC().Format(time.RFC3339)), 0o600)
}

// snapshotState 铸 SQLite 一致快照（VACUUM INTO——在线一致，WAL 安全；
// 快照入库、live 库三件排除在备份集外，恢复面以快照为准）。
func (e *Engine) snapshotState(ctx context.Context) error {
	if err := os.MkdirAll(e.platformBackupPath(""), 0o750); err != nil {
		return err
	}
	target := e.platformBackupPath(platformSnapshotName)
	_ = os.Remove(target) // VACUUM INTO 不覆写既有文件
	return e.db.VacuumInto(ctx, target)
}

// platformBackupPath 解析 platform-backups 域内路径。
func (e *Engine) platformBackupPath(rel string) string {
	return filepath.Join(e.opts.DataRoot, platformBackupDir, rel)
}

// resticRepo 是一个 restic 仓的端点面（env 形态经子进程环境传递——
// 凭证不落 argv/日志）。
type resticRepo struct {
	repo string
	env  map[string]string
}

// platformRepos 组装仓清单（本地恒在；s3 在场追加）+ 仓密（首用铸造）。
func (e *Engine) platformRepos() ([]resticRepo, error) {
	pass, err := e.platformRepoPassword()
	if err != nil {
		return nil, err
	}
	local := resticRepo{
		repo: e.platformBackupPath(platformRepoSubdir),
		env:  map[string]string{"RESTIC_PASSWORD": pass},
	}
	if err := os.MkdirAll(local.repo, 0o700); err != nil {
		return nil, err
	}
	s3 := e.opts.PlatformBackup.S3
	if s3 == nil {
		return []resticRepo{local}, nil
	}
	prefix := s3.Prefix
	if prefix != "" {
		prefix = "/" + prefix
	}
	return []resticRepo{local, {
		repo: fmt.Sprintf("s3:%s/%s%s", s3.Endpoint, s3.Bucket, prefix),
		env: map[string]string{
			"RESTIC_PASSWORD":       pass,
			"AWS_ACCESS_KEY_ID":     s3.AccessKeyID,
			"AWS_SECRET_ACCESS_KEY": s3.SecretAccessKey,
		},
	}}, nil
}

// platformRepoPassword 铸/读仓密（32 字节 hex；0600；首启生成）。仓密在
// 备份集外（keys/ 排除见 runPlatformBackup 的 exclude 集——仓密进仓自锁）。
func (e *Engine) platformRepoPassword() (string, error) {
	keyPath := filepath.Join(e.opts.DataRoot, "keys", platformRepoKeyFile)
	//nolint:gosec // G304：路径段全部平台常量拼接（DataRoot 装配注入）
	if existing, err := os.ReadFile(keyPath); err == nil && len(existing) == 64 {
		return string(existing), nil
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("platform backup: repo password entropy: %w", err)
	}
	pass := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(keyPath, []byte(pass), 0o600); err != nil {
		return "", err
	}
	return pass, nil
}

// resticRun 执行一次 restic 子进程（args 数组、零 shell——shellguard 射
// 程内延续；env 合并 os.Environ + 仓面凭证）。
func (e *Engine) resticRun(ctx context.Context, repo resticRepo, args ...string) error {
	bin := e.opts.ResticPath
	if bin == "" {
		var err error
		if bin, err = resticBinary(); err != nil {
			return err
		}
	}
	out, err := resticExec(ctx, bin, append([]string{"-r", repo.repo}, args...), append(os.Environ(), envPairs(repo.env)...))
	if err != nil {
		tail := stderrTail(string(out))
		if tail == "" {
			tail = err.Error()
		}
		return fmt.Errorf("restic %s (repo %s): %s", args[0], repo.repo, tail)
	}
	return nil
}

func envPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// PlatformSnapshot 是 restic snapshots --json 的一行（列举面）。
type PlatformSnapshot struct {
	ID       string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Tags     []string  `json:"tags"`
}

// ListPlatformSnapshots 列举本地仓快照（restic snapshots --json 直读，
// 零状态行——仓库自身即事实源，ADR-0039 决策 10）。
func (e *Engine) ListPlatformSnapshots(ctx context.Context) ([]PlatformSnapshot, error) {
	if !e.PlatformBackupAvailable() {
		return nil, fmt.Errorf("platform backup: restic binary not found")
	}
	repos, err := e.platformRepos()
	if err != nil {
		return nil, err
	}
	local := repos[0]
	bin := e.opts.ResticPath
	if bin == "" {
		var err error
		if bin, err = resticBinary(); err != nil {
			return nil, err
		}
	}
	out, err := resticExec(ctx, bin, []string{"-r", local.repo, "snapshots", "--json"},
		append(os.Environ(), envPairs(local.env)...))
	if err != nil {
		return nil, fmt.Errorf("restic snapshots (repo %s): %w", local.repo, err)
	}
	var snaps []PlatformSnapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, fmt.Errorf("restic snapshots parse: %w", err)
	}
	return snaps, nil
}
