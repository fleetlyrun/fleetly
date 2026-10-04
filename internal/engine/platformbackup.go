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
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PinnedResticVersion 是 restic 钉版（ADR-0021 口径；2026-10 现行稳定，
// install.sh 同版本下载——guards TestResticPinConstantAndInstallerAgree
// 静态执法两处一致）。
const PinnedResticVersion = "0.19.1"

// Platform Backup 的数据根内布局（platformBackupDir 相对 DataRoot）。
const (
	platformBackupDir       = "platform-backups"    // 域目录
	platformRepoSubdir      = "restic"              // 本地仓（restic repo）
	platformSnapshotName    = "state-snapshot.db"   // SQLite VACUUM INTO 落点
	platformLastRunFile     = "last-run"            // 成功锚（RFC3339；interval 语义）
	platformLastAttemptFile = "last-attempt"        // 尝试锚（RFC3339；失败退避语义）
	platformRepoKeyFile     = "platform-backup.key" // 仓密（keys/ 下，0600，排除备份集）
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

// platformBackupDue 报告节拍到点：成功锚（interval 语义）与尝试锚（失败
// 退避语义——无退避则失败后每拍重试，事件/日志风暴）。两锚皆缺席（首启）
// 或损坏 = 立即。
func (e *Engine) platformBackupDue(cfg PlatformBackupConfig) bool {
	if !e.PlatformBackupAvailable() {
		return false
	}
	now := e.clock.Now()
	if anchor, err := os.ReadFile(e.platformBackupPath(platformLastRunFile)); err == nil {
		if t, perr := time.Parse(time.RFC3339, string(anchor)); perr == nil && now.Sub(t) < cfg.Interval {
			return false // 成功锚未到期
		}
	}
	if attempt, err := os.ReadFile(e.platformBackupPath(platformLastAttemptFile)); err == nil {
		if t, perr := time.Parse(time.RFC3339, string(attempt)); perr == nil && now.Sub(t) < platformBackupRetryBackoff {
			return false // 失败退避窗内
		}
	}
	return true
}

// platformBackupRetryBackoff 是失败重试退避（dind 实证：无退避则失败后
// 每拍重试——秒级事件风暴；5min 足够瞬时性故障自愈，持续性故障日志有界）。
const platformBackupRetryBackoff = 5 * time.Minute

// runPlatformBackup 执行一次 Platform Backup（快照 → 仓初始化（幂等）→
// 双仓 backup → check → forget；s3 仓失败不阻断本地仓完成事实——事件按
// 整体成败落）。尝试锚先落（失败退避的计时起点），成功锚仅全链成功推进。
func (e *Engine) runPlatformBackup(ctx context.Context, cfg PlatformBackupConfig) error {
	if !e.PlatformBackupAvailable() {
		return fmt.Errorf("platform backup: restic binary not found (pin %s); install it or disable platform backup", PinnedResticVersion)
	}
	if err := os.MkdirAll(e.platformBackupPath(""), 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(e.platformBackupPath(platformLastAttemptFile),
		[]byte(e.clock.Now().UTC().Format(time.RFC3339)), 0o600); err != nil {
		return err
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
		// 仓初始化（首用）：目录/桶 ≠ 仓——restic 需要 init 建 config。
		// 本地仓以 config 文件在场判定；s3 仓 init 幂等（已初始化的报错
		// 按"已在场"忽略）。
		if err := e.ensureResticRepo(ctx, repo); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := e.resticRun(ctx, repo, "backup", e.opts.DataRoot,
			"--exclude", filepath.Join(e.opts.DataRoot, "fleetly.db"),
			"--exclude", filepath.Join(e.opts.DataRoot, "fleetly.db-wal"),
			"--exclude", filepath.Join(e.opts.DataRoot, "fleetly.db-shm"),
			// 仓密排除（决策 9：仓密进仓自锁的循环依赖——恢复所需的密在
			// 仓外，runbook 提示离机保管）。
			"--exclude", filepath.Join(e.opts.DataRoot, "keys", platformRepoKeyFile),
			"--exclude", e.platformBackupPath(platformRepoSubdir),
			"--exclude", e.platformBackupPath(resticCacheSubdir)); err != nil {
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
			resticKeepWithin(cfg.Retention), "--prune"); err != nil {
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

// resticKeepWithin 把保留窗铸成 restic duration（值域单位 y/m/w/d/h——
// 不收 Go duration 串的 m/s 尾巴；dind 实证 "168h0m0s" 被拒）。小时向上
// 取整、最小 1h（亚小时保留窗在 restic 面无法表达，向上取整 = 多留不
// 少留的诚实方向）。
func resticKeepWithin(retention time.Duration) string {
	hours := int(math.Ceil(retention.Hours()))
	if hours < 1 {
		hours = 1
	}
	return strconv.Itoa(hours) + "h"
}

// ensureResticRepo 幂等初始化一个仓（本地以 config 文件判定；s3 尝试
// init 且把"已初始化"报错按在场处理——restic 对已建仓 init 的报错文本
// 含 config file already exists / already initialized）。
func (e *Engine) ensureResticRepo(ctx context.Context, repo resticRepo) error {
	if strings.HasPrefix(repo.repo, "/") {
		if _, err := os.Stat(filepath.Join(repo.repo, "config")); err == nil {
			return nil
		}
	}
	if err := e.resticRun(ctx, repo, "init"); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "already exists") || strings.Contains(msg, "already initialized") {
			return nil
		}
		return err
	}
	return nil
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
	out, err := resticExec(ctx, bin, append([]string{"-r", repo.repo}, args...), e.resticEnv(repo))
	if err != nil {
		tail := stderrTail(string(out))
		if tail == "" {
			tail = err.Error()
		}
		return fmt.Errorf("restic %s (repo %s): %s", args[0], repo.repo, tail)
	}
	return nil
}

// resticCacheSubdir 是 restic 客户端缓存目录（platform-backups 域内；
// systemd 环境无 HOME/XDG_CACHE_HOME——restic "unable to locate cache
// directory" 拒跑（2026-10-04 staging 实录；dind/e2e 上下文恒有 HOME 故
// CI 不红）。域内自管缓存随备份排除（与仓目录同待遇，见 exclude 集）。
const resticCacheSubdir = "cache"

// resticEnv 组装 restic 子进程环境：os.Environ + 仓面凭证。
func (e *Engine) resticEnv(repo resticRepo) []string {
	return e.resticEnvFrom(os.Environ(), repo)
}

// resticEnvFrom 是 resticEnv 的可测形态（base 注入——systemd 无 HOME 面
// 在测试机不可复现，参数化补测）：HOME 与 XDG_CACHE_HOME 双缺（systemd
// 形态）时补 XDG_CACHE_HOME 钉进域内缓存目录。
func (e *Engine) resticEnvFrom(base []string, repo resticRepo) []string {
	env := append(append([]string{}, base...), envPairs(repo.env)...)
	hasHome := false
	for _, kv := range env {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "XDG_CACHE_HOME=") {
			hasHome = true
			break
		}
	}
	if !hasHome {
		dir := e.platformBackupPath(resticCacheSubdir)
		if err := os.MkdirAll(dir, 0o700); err == nil {
			env = append(env, "XDG_CACHE_HOME="+dir)
		}
		// 建目录失败不阻断：环境原样透传，restic 自行报错（错误链完整）。
	}
	return env
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

// TriggerPlatformBackup 立即同步执行一次 Platform Backup（ADR-0039 决策 9
// 手动面：直接执行不走节拍锚——F2.3 升级序的前置动词执行体，ADR-0015）。
// 执行时长受 BackupTimeout 上界约束；与节拍环的并发互斥由 restic 仓锁兜
// 底（同仓并发 backup 一方持锁一方精确失败，无半写态）。成败事件 + 审计
// 一拍落账（节拍环同款事件面；手动面加操作者审计）。
func (e *Engine) TriggerPlatformBackup(ctx context.Context) (*PlatformSnapshot, error) {
	cfg := e.opts.PlatformBackup
	if cfg == nil {
		return nil, fmt.Errorf("platform backup: not configured on this control plane")
	}
	execCtx, cancel := context.WithTimeout(ctx, e.opts.BackupTimeout)
	defer cancel()
	runErr := e.runPlatformBackup(execCtx, *cfg)

	// 事件 + 审计（失败也落——成败事实与节拍环同面；审计带操作者）。
	eventName, payload := eventPlatformBackupOK, platformBackupEventJSON(cfg.Retention.String(), "")
	if runErr != nil {
		eventName, payload = eventPlatformBackupFail, platformBackupEventJSON("", runErr.Error())
	}
	if err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		return e.commitWrite(ctx, tx, writeFact{
			events: []func() eventFact{func() eventFact {
				return eventFact{name: eventName, aggregate: "platform", id: "platform-backup", payload: payload}
			}},
			audits: []auditFact{{action: "platform.backup_trigger", resource: "platform/platform-backup", actorCtx: true}},
		})
	}); err != nil {
		e.log.Error("platform backup: record manual run", "err", err)
	}
	if runErr != nil {
		return nil, runErr
	}
	// 回读本地仓最新快照（触发响应携带；restic 短 id）。
	snaps, err := e.ListPlatformSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("platform backup: listing the fresh snapshot: %w", err)
	}
	if len(snaps) == 0 {
		return nil, fmt.Errorf("platform backup: succeeded but the local repo lists no snapshots")
	}
	newest := &snaps[0]
	for i := range snaps[1:] {
		if snaps[i+1].Time.After(newest.Time) {
			newest = &snaps[i+1]
		}
	}
	return newest, nil
}

// ListPlatformSnapshots 列举本地仓快照（restic snapshots --json 直读，
// 零状态行——仓库自身即事实源，ADR-0039 决策 10）；新→旧（time 降序，
// 分页游标的确定性前提）。
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
	out, err := resticExec(ctx, bin, []string{"-r", local.repo, "snapshots", "--json"}, e.resticEnv(local))
	if err != nil {
		tail := stderrTail(string(out))
		if tail == "" {
			tail = err.Error()
		}
		return nil, fmt.Errorf("restic snapshots (repo %s): %s", local.repo, tail)
	}
	var snaps []PlatformSnapshot
	if err := json.Unmarshal(out, &snaps); err != nil {
		return nil, fmt.Errorf("restic snapshots parse: %w", err)
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].Time.After(snaps[j].Time) })
	return snaps, nil
}
