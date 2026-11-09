package backups

// pubsub 类型备份/恢复执行面（B4b，ADR-0096 决策 6 统一转储契约的首个
// object_set 实现；design §4.5 L4 特化表 nats 行 + 物理约束三条）：
//
//	备份 = 流清单枚举（`nats stream ls --json`）→ 逐流 `nats stream info`
//	       探 storage（**memory 流拒**——不可快照，整集受理拒并列名）→ 逐流
//	       `nats stream backup <stream> <out-dir>`（产目录不产流，红队 A2①）
//	       → manager 把目录打成单成员 .tgz 流式 Put（sha256 边流边算）→
//	       删本地 → 下一流（磁盘峰值 = 最大单成员）→ manifest 最后写
//	       （B4a 机制零改：成员名 = 流名）。
//	恢复 = manifest 逐成员循环：Get .tgz → 解包进 manager 临时目录 →
//	       UtilityHostDir 只读挂进工具容器 → `nats stream restore <dir>`
//	       （空流前提——目标新建实例）→ 删本地 → 下一成员。
//
// 预算：逐成员独立（BackupTimeout per-member——枚举/探测/单流备份各自带
// 界）。1h 孤儿宽限窗裁定（design §4.5 遗留 / 本文件注记）：orphanPass 按
// **成员各自 ModTime** 判窗——任一成员不足窗即整集等待；逐流串行下相邻成员
// Put 间隔 ≤ 单成员预算（15m）« 1h，故总时长再长也不会误删在途集——窗口
// 维持 1h 不调，判据已天然按成员分别判。
//
// 工具容器 = natsio/nats-box 钉版（pubsubtemplate.NatsBoxImage）；连接凭证经
// NATS_URL/NATS_USER/NATS_PASSWORD env（REDISCLI_AUTH 同款登记例外）。

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/backup"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	domainstream "github.com/fleetlyrun/fleetly/internal/domain/data/stream"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// 工具容器内挂点（备份产物目录 / 恢复输入目录；/run/secrets 命名空间之外
// 的独立挂点——目录级 bind/hostPath 与材料文件面分立）。
const (
	pubsubDumpTarget    = "/fleetly-dump"
	pubsubRestoreTarget = "/fleetly-restore"
)

// executePubSubBackup 执行一件 pubsub 类型备份（executeOneBackup 的 kind
// 分支；fail 回调由调用方传入——台账失败回执单源）。
func (bk *Backups) executePubSubBackup(ctx context.Context, b *backup.Backup, fail func(string)) {
	row, err := bk.repos.PubSubs.Get(ctx, b.Subject.ID)
	if err != nil {
		fail(fmt.Sprintf("pubsub row unavailable: %v", err))
		return
	}
	if row.Deleted() {
		fail("pubsub deleted before backup executed")
		return
	}
	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
	if err != nil {
		fail(fmt.Sprintf("engine %q left the template registry: %v", row.Engine, err))
		return
	}
	face, ok := tpl.(pubsubtemplate.StreamDumpFace)
	if !ok {
		fail(fmt.Sprintf("engine %q carries no stream dump face", row.Engine))
		return
	}
	password, err := bk.pubsubPassword(ctx, row)
	if err != nil {
		fail(err.Error())
		return
	}
	host := contract.PubSubDNSName(row.ID)
	if err := bk.repos.Backups.MarkRunning(ctx, b.ID); err != nil {
		bk.log.Error("backup step: mark running", "backup", b.ID, "err", err)
		return
	}
	image := pubsubtemplate.NatsBoxImage()
	ns := capability.NamespaceRef{Team: bk.projectTeamOf(row.ProjectID), Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name}
	networks := bk.projectNetworkFactsNamesOnly(ctx, row.ProjectID)

	// ① 流清单枚举（独立预算）。
	listSpec, err := face.ListStreams(host, password)
	if err != nil {
		fail(err.Error())
		return
	}
	listOut, err := bk.runUtilityCapture(ctx, b.ID+"-ls", ns, image, listSpec, networks, nil)
	if err != nil {
		fail(fmt.Sprintf("backup execution failed: list streams: %v", err))
		return
	}
	streams, err := parseStreamList(listOut)
	if err != nil {
		fail(fmt.Sprintf("backup execution failed: parse stream list: %v", err))
		return
	}
	// ② 逐流探测 storage（memory 流拒——整集受理拒，列名；红队 C-AT-06）。
	var memoryStreams []string
	for _, stream := range streams {
		infoSpec, err := face.InspectStream(host, password, stream)
		if err != nil {
			fail(err.Error())
			return
		}
		infoOut, err := bk.runUtilityCapture(ctx, b.ID+"-info", ns, image, infoSpec, networks, nil)
		if err != nil {
			fail(fmt.Sprintf("backup execution failed: inspect stream %q: %v", stream, err))
			return
		}
		storage, err := parseStreamStorage(infoOut)
		if err != nil {
			fail(fmt.Sprintf("backup execution failed: inspect stream %q: %v", stream, err))
			return
		}
		if storage == "memory" {
			memoryStreams = append(memoryStreams, stream)
		}
	}
	if len(memoryStreams) > 0 {
		fail(fmt.Sprintf("backup refused: memory-storage streams cannot be snapshotted (%s); switch them to file storage or exclude them before backing up", strings.Join(memoryStreams, ", ")))
		return
	}
	// ③ 逐流备份 → 打包 → Put（成员名 = 流名；manifest 最后写）。
	set := backup.KeyMint(row.ProjectID, b.Subject.ID, b.ID, bk.clock.Now())
	putKeys := make([]string, 0, len(streams)+1)
	members := make([]backup.ManifestMember, 0, len(streams))
	for _, stream := range streams {
		key, kerr := backup.MemberKeyOf(set, stream)
		if kerr != nil {
			bk.deleteObjectsQuiet(ctx, putKeys)
			fail(fmt.Sprintf("backup execution failed: %v", kerr))
			return
		}
		info, rerr := bk.dumpStreamPut(ctx, b.ID, ns, image, face, host, password, stream, networks, key)
		if rerr != nil {
			bk.deleteObjectsQuiet(ctx, append(append([]string(nil), putKeys...), key))
			fail(rerr.Error())
			return
		}
		putKeys = append(putKeys, key)
		members = append(members, backup.ManifestMember{Key: key, SHA256: info.Digest, SizeBytes: info.Size})
	}
	manifest := &backup.Manifest{
		Kind:          b.Subject.Kind,
		Engine:        row.Engine,
		EngineVersion: row.Version,
		Members:       members,
		TotalDigest:   backup.TotalDigest(members),
		CreatedAt:     state.FormatTime(bk.clock.Now()),
	}
	manifestKey := backup.ManifestKeyOf(set)
	manifestBytes, merr := manifest.Encode()
	if merr != nil {
		bk.deleteObjectsQuiet(ctx, putKeys)
		fail(fmt.Sprintf("backup execution failed: encode manifest: %v", merr))
		return
	}
	if _, perr := bk.objectStore.Put(ctx, manifestKey, bytes.NewReader(manifestBytes)); perr != nil {
		bk.deleteObjectsQuiet(ctx, append(putKeys, manifestKey))
		fail(fmt.Sprintf("backup execution failed: put manifest: %v", perr))
		return
	}
	putKeys = append(putKeys, manifestKey)
	if err := bk.repos.Backups.FinishSucceeded(ctx, b.ID, manifestKey, manifest.TotalDigest, manifest.TotalSize()); err != nil {
		if errors.Is(err, state.ErrConflict) {
			bk.log.Warn("backup step: late success receipt dropped, backup already terminal", "backup", b.ID)
			bk.deleteObjectsQuiet(ctx, putKeys)
			return
		}
		bk.log.Error("backup step: record success", "backup", b.ID, "err", err)
		return
	}
	if err := bk.repos.PubSubs.SetLastBackupAt(ctx, row.ID, state.FormatTime(bk.clock.Now())); err != nil {
		bk.log.Error("backup step: advance anchor", "pubsub", row.ID, "err", err)
	}
	succ := *b
	succ.ObjectKey, succ.Digest, succ.SizeBytes = manifestKey, manifest.TotalDigest, manifest.TotalSize()
	bk.emitBackupEvent(ctx, EventBackupSucceeded, &succ, "")
}

// dumpStreamPut 单流「落盘→打包→Put→删本地」（per-member 预算）：工具容器
// 把备份目录写进 manager 临时目录（UtilityHostDir rw），manager 以 tar+gzip
// 流式打包进 Put（sha256 由 Put 回执），函数返回前整目录移除。
func (bk *Backups) dumpStreamPut(ctx context.Context, backupID string, ns capability.NamespaceRef, image string, face pubsubtemplate.StreamDumpFace, host, password, stream string, networks []string, key string) (capability.ObjectInfo, error) {
	execCtx, cancel := context.WithTimeout(ctx, bk.opts.BackupTimeout)
	defer cancel()
	hostDir, err := os.MkdirTemp("", "fleetly-pubsub-dump-")
	if err != nil {
		return capability.ObjectInfo{}, fmt.Errorf("backup execution failed: temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(hostDir) }()
	// 工具容器以非 root 身份写入时宿主目录须可写（nats-box 镜像 nonroot
	// 变体先例）；0o777 只在一次性临时目录生效，函数返回即删。
	if err := os.Chmod(hostDir, 0o777); err != nil { //nolint:gosec // 一次性临时目录，函数返回即删
		return capability.ObjectInfo{}, fmt.Errorf("backup execution failed: temp dir mode: %w", err)
	}
	outDir := pubsubDumpTarget + "/" + stream
	spec, err := face.DumpStream(host, password, stream, outDir)
	if err != nil {
		return capability.ObjectInfo{}, err
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	req := capability.UtilityRequest{
		ID:          backupID + "-" + sanitizeUtilityID(stream),
		Namespace:   ns,
		Image:       image,
		Argv:        spec.Argv,
		Env:         spec.Env,
		Networks:    networks,
		SecretFiles: spec.SecretFiles,
		HostDir:     &capability.UtilityHostDir{HostPath: hostDir, Target: pubsubDumpTarget},
	}
	if err := bk.utility.RunUtility(execCtx, req, stdout, stderr); err != nil {
		msg := fmt.Sprintf("backup execution failed: stream %q: %v", stream, err)
		if et := contract.StderrTail(stderr.String()); et != "" {
			msg += ": " + et
		} else if ot := contract.StderrTail(stdout.String()); ot != "" {
			msg += ": " + ot
		}
		return capability.ObjectInfo{}, errors.New(msg)
	}
	produced := filepath.Join(hostDir, stream)
	if st, serr := os.Stat(produced); serr != nil || !st.IsDir() {
		return capability.ObjectInfo{}, fmt.Errorf("backup execution failed: stream %q produced no backup directory at %s", stream, outDir)
	}
	// tar+gzip 流式打包进 Put（io.Pipe 经纪：写端 goroutine 打包，读端 Put）。
	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	packErr := make(chan error, 1)
	go func() {
		err := tarGzDir(pw, produced)
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		packErr <- err
	}()
	info, putErr := bk.objectStore.Put(execCtx, key, pr)
	if putErr != nil {
		_ = pr.Close()
	}
	var perr error
	select {
	case perr = <-packErr:
	case <-execCtx.Done():
		_ = pr.Close()
		perr = execCtx.Err()
	}
	if perr != nil && putErr == nil {
		putErr = perr
	}
	if putErr != nil {
		return capability.ObjectInfo{}, fmt.Errorf("backup execution failed: stream %q: %w", stream, putErr)
	}
	return info, nil
}

// restorePubSub 执行 pubsub 类型恢复在途行（restorePass 的类型分支）：目标
// 在服后 manifest 逐成员循环注入（空流前提——新建实例无同名流）。终态
// 语义与 database 同律：成功 → succeeded；任一失败 → failed（不再重试）；
// 目标未在服是等待态，保留 pending/running。
func (bk *Backups) restorePubSub(ctx context.Context, row *pubsub.PubSub) {
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		if err := bk.repos.PubSubs.SetRestoreFailed(ctx, row.ID, msg); err != nil {
			bk.log.Error("restore: record failure", "pubsub", row.ID, "err", err)
		}
		bk.log.Error("restore failed", "pubsub", row.ID, "err", msg)
	}
	src, err := bk.repos.Backups.Get(ctx, row.RestoreFromBackup)
	if err != nil {
		fail("source backup unavailable: %v", err)
		return
	}
	if src.Status != backup.StatusSucceeded || src.ObjectKey == "" {
		fail("source backup %s is not a succeeded backup with an object", src.ID)
		return
	}
	if src.Subject.Kind != dataservicetemplate.KindPubSub {
		fail("source backup %s is a %q kind artifact, not a pubsub backup", src.ID, src.Subject.Kind)
		return
	}
	if src.Engine != row.Engine {
		fail("source backup engine %q does not match target engine %q", src.Engine, row.Engine)
		return
	}
	if row.Status != pubsub.StatusRunning {
		return // 目标未在服：等待态（不落 failed，下一拍再试）
	}
	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
	if err != nil {
		fail("engine %q left the template registry: %v", row.Engine, err)
		return
	}
	face, ok := tpl.(pubsubtemplate.StreamDumpFace)
	if !ok {
		fail("engine %q carries no stream dump face", row.Engine)
		return
	}
	password, err := bk.pubsubPassword(ctx, row)
	if err != nil {
		fail("%v", err)
		return
	}
	unlockMaintenance := bk.lockMaintenance()
	defer unlockMaintenance()
	m, err := bk.loadBackupManifest(ctx, src.ObjectKey)
	if err != nil {
		fail("%v", err)
		return
	}
	if m.Kind != dataservicetemplate.KindPubSub {
		fail("source backup %s is a %q kind artifact, not a pubsub object set", src.ID, m.Kind)
		return
	}
	host := contract.PubSubDNSName(row.ID)
	ns := capability.NamespaceRef{Team: bk.projectTeamOf(row.ProjectID), Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name}
	networks := bk.projectNetworkFactsNamesOnly(ctx, row.ProjectID)
	image := pubsubtemplate.NatsBoxImage()
	// 执行前落 running（pending → running → succeeded/failed 显式状态机）。
	if err := bk.repos.PubSubs.SetRestoreRunning(ctx, row.ID); err != nil {
		bk.log.Error("restore: mark running", "pubsub", row.ID, "err", err) // 下拍重取
		return
	}
	for i := range m.Members {
		member := &m.Members[i]
		_, name, ok := backup.SplitSetKey(member.Key)
		if !ok {
			fail("member key %q is not a set member", member.Key)
			return
		}
		if err := bk.restoreStreamMember(ctx, row.ID, ns, image, face, host, password, name, networks, member.Key); err != nil {
			fail("restore execution failed: %v", err)
			return
		}
	}
	// 回填子资源行（staging 记录·六十二实咬：恢复只注入 engine 侧流，
	// 平台零行 = ListStreams 空 = 收敛段零动作——「恢复成功但用户看不见」。
	// 行值从成员 backup.json 的 config 取（subjects/retention/storage/
	// max_bytes 单源在备份元数据），status ready（engine 侧已存在=匹配）。
	for i := range m.Members {
		member := &m.Members[i]
		_, name, ok := backup.SplitSetKey(member.Key)
		if !ok {
			fail("member key %q is not a set member", member.Key)
			return
		}
		if err := bk.adoptRestoredStream(ctx, row, member.Key, name); err != nil {
			fail("adopt restored stream %q: %v", name, err)
			return
		}
	}
	if err := bk.repos.PubSubs.SetRestoreSucceeded(ctx, row.ID); err != nil {
		bk.log.Error("restore: record success", "pubsub", row.ID, "err", err)
		return
	}
	if err := bk.emitObservation(ctx, bk.eventTeamOfProject(ctx, bk.repos, row.ProjectID),
		EventBackupRestored, dataservicetemplate.KindPubSub, row.ID,
		backupRestoredEventJSON(dataservicetemplate.KindPubSub, row.ID, row.ProjectID, src)); err != nil {
		bk.log.Error("restore: emit event", "pubsub", row.ID, "err", err)
	}
}

// streamBackupConfig 是成员 backup.json 的 config 子集（回填行的值单源）。
type streamBackupConfig struct {
	Name      string   `json:"name"`
	Subjects  []string `json:"subjects"`
	Retention string   `json:"retention"`
	Storage   string   `json:"storage"`
	MaxBytes  int64    `json:"max_bytes"`
}

// parseStreamBackupConfig 解出流配置（nats backup.json 形：顶层 config 对象）。
func parseStreamBackupConfig(path string) (streamBackupConfig, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // 一次性临时目录内的平台产物
	if err != nil {
		return streamBackupConfig{}, err
	}
	var wrap struct {
		Config streamBackupConfig `json:"config"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return streamBackupConfig{}, err
	}
	if wrap.Config.Name == "" || len(wrap.Config.Subjects) == 0 {
		return streamBackupConfig{}, fmt.Errorf("backup config incomplete (name/subjects)")
	}
	if wrap.Config.Retention == "" {
		wrap.Config.Retention = "limits"
	}
	if wrap.Config.Storage == "" {
		wrap.Config.Storage = "file"
	}
	return wrap.Config, nil
}

// adoptRestoredStream 把恢复完成的 engine 侧流收编为平台 Stream 行（值
// 单源=成员 backup.json config；Get 流对象 → Get 对象 → 解包取 config →
// Mint 行）。幂等：同名行在场即跳过（断点重入/双恢复窗）。
func (bk *Backups) adoptRestoredStream(ctx context.Context, row *pubsub.PubSub, key, name string) error {
	if rows, err := bk.repos.Streams.ListByPubSub(ctx, row.ID); err == nil {
		for i := range rows {
			if rows[i].Name == name && !rows[i].Deleted() {
				return nil // 幂等：行已在（断点重入/双恢复窗）
			}
		}
	}
	obj, err := bk.objectStore.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read backup member %q: %w", key, err)
	}
	defer func() { _ = obj.Close() }()
	hostDir, err := os.MkdirTemp("", "fleetly-pubsub-adopt-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(hostDir) }()
	target := filepath.Join(hostDir, name)
	if err := os.MkdirAll(target, 0o755); err != nil { //nolint:gosec // 一次性临时目录
		return fmt.Errorf("temp dir: %w", err)
	}
	if err := untarGz(obj, target); err != nil {
		return fmt.Errorf("unpack member %q: %w", key, err)
	}
	cfg, err := parseStreamBackupConfig(filepath.Join(target, "backup.json"))
	if err != nil {
		return fmt.Errorf("parse backup.json: %w", err)
	}
	var maxBytes *int64
	if cfg.MaxBytes > 0 {
		maxBytes = &cfg.MaxBytes
	}
	return bk.repos.Streams.Create(ctx, &domainstream.Stream{
		ProjectID: row.ProjectID,
		PubSubID:  row.ID,
		Name:      name,
		Subjects:  cfg.Subjects,
		Retention: cfg.Retention,
		Storage:   cfg.Storage,
		MaxBytes:  maxBytes,
		Status:    domainstream.StatusReady,
	})
}

// restoreStreamMember 单成员恢复（per-member 预算）：Get .tgz → 解包进
// manager 临时目录 → 只读挂进工具容器 → `nats stream restore <dir>` →
// 删本地。
func (bk *Backups) restoreStreamMember(ctx context.Context, pubsubID string, ns capability.NamespaceRef, image string, face pubsubtemplate.StreamDumpFace, host, password, stream string, networks []string, key string) error {
	execCtx, cancel := context.WithTimeout(ctx, bk.opts.BackupTimeout)
	defer cancel()
	obj, err := bk.objectStore.Get(execCtx, key)
	if err != nil {
		return fmt.Errorf("read backup member %q: %w", key, err)
	}
	defer func() { _ = obj.Close() }()
	hostDir, err := os.MkdirTemp("", "fleetly-pubsub-restore-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(hostDir) }()
	target := filepath.Join(hostDir, stream)
	if err := os.MkdirAll(target, 0o755); err != nil { //nolint:gosec // 一次性临时目录
		return fmt.Errorf("temp dir: %w", err)
	}
	if err := untarGz(obj, target); err != nil {
		return fmt.Errorf("unpack member %q: %w", key, err)
	}
	// 目录须对非 root 工具进程可读（0o755 已满足；文件由 untarGz 落 0o644）。
	inDir := pubsubRestoreTarget + "/" + stream
	spec, err := face.RestoreStream(host, password, stream, inDir)
	if err != nil {
		return err
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	req := capability.UtilityRequest{
		ID:          "restore-" + pubsubID + "-" + sanitizeUtilityID(stream),
		Namespace:   ns,
		Image:       image,
		Argv:        spec.Argv,
		Env:         spec.Env,
		Networks:    networks,
		SecretFiles: spec.SecretFiles,
		HostDir:     &capability.UtilityHostDir{HostPath: hostDir, Target: pubsubRestoreTarget, ReadOnly: true},
	}
	if err := bk.utility.RunUtility(execCtx, req, stdout, stderr); err != nil {
		msg := fmt.Sprintf("stream %q: %v", stream, err)
		if et := contract.StderrTail(stderr.String()); et != "" {
			msg += ": " + et
		}
		if ot := contract.StderrTail(stdout.String()); ot != "" {
			msg += ": " + ot
		}
		return errors.New(msg)
	}
	return nil
}

// runUtilityCapture 执行工具容器并捕获 stdout（流清单/流探测的 JSON 面；
// 独立 per-member 预算）。
func (bk *Backups) runUtilityCapture(ctx context.Context, id string, ns capability.NamespaceRef, image string, spec dataservicetemplate.BackupSpec, networks []string, hostDir *capability.UtilityHostDir) ([]byte, error) {
	execCtx, cancel := context.WithTimeout(ctx, bk.opts.BackupTimeout)
	defer cancel()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	req := capability.UtilityRequest{
		ID: id, Namespace: ns, Image: image,
		Argv: spec.Argv, Env: spec.Env, Networks: networks, SecretFiles: spec.SecretFiles,
		HostDir: hostDir,
	}
	if err := bk.utility.RunUtility(execCtx, req, stdout, stderr); err != nil {
		msg := err.Error()
		if et := contract.StderrTail(stderr.String()); et != "" {
			msg += ": " + et
		}
		return nil, errors.New(msg)
	}
	return stdout.Bytes(), nil
}

// parseStreamList 解析 `nats stream ls --json` 输出：字符串数组（流名），
// 或对象数组（新版 CLI 带 config.name）。排序稳定（成员序确定性）。
func parseStreamList(out []byte) ([]string, error) {
	trimmed := bytes.TrimSpace(out)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	var names []string
	if err := json.Unmarshal(trimmed, &names); err == nil {
		sort.Strings(names)
		return names, nil
	}
	var objs []struct {
		Config struct {
			Name string `json:"name"`
		} `json:"config"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(trimmed, &objs); err != nil {
		return nil, fmt.Errorf("stream list is neither a name array nor an info array: %w", err)
	}
	for _, o := range objs {
		n := o.Config.Name
		if n == "" {
			n = o.Name
		}
		if n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names, nil
}

// parseStreamStorage 解析 `nats stream info --json` 的 config.storage
// （"file" | "memory"）。
func parseStreamStorage(out []byte) (string, error) {
	var info struct {
		Config struct {
			Storage string `json:"storage"`
		} `json:"config"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &info); err != nil {
		return "", fmt.Errorf("stream info is not JSON: %w", err)
	}
	if info.Config.Storage == "" {
		return "", fmt.Errorf("stream info carries no config.storage")
	}
	return strings.ToLower(info.Config.Storage), nil
}

// tarGzDir 把目录打成 tar.gz 流（成员路径相对 dir；确定性遍历序）。
func tarGzDir(w io.Writer, dir string) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil {
			return werr
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return nil
		}
		hdr, herr := tar.FileInfoHeader(info, "")
		if herr != nil {
			return herr
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		f, oerr := os.Open(path) //nolint:gosec // G304：路径来自私有临时目录遍历
		if oerr != nil {
			return oerr
		}
		_, cerr := io.Copy(tw, f)
		_ = f.Close()
		return cerr
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// untarGz 把 tar.gz 流解包进 dir（路径穿越拒；仅目录与普通文件）。
func untarGz(r io.Reader, dir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(filepath.FromSlash(hdr.Name))
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("member %q escapes the restore directory", hdr.Name)
		}
		dest := filepath.Join(dir, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil { //nolint:gosec // 一次性临时目录
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil { //nolint:gosec // 一次性临时目录
				return err
			}
			f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644) //nolint:gosec // G302/G304：私有临时目录内落档
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // G110：备份成员自平台 ObjectStore，大小有界
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		default:
			// 符号链接/设备等非常规项：备份目录不含此类，拒绝即诚实。
			return fmt.Errorf("member %q has unsupported type %d", hdr.Name, hdr.Typeflag)
		}
	}
}

// sanitizeUtilityID 把流名压到工具容器 ID 安全集（载体名段）。
func sanitizeUtilityID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// schedulePubSubs 是 schedulePass 的 pubsub 类型行源（B4b，ADR-0096 决策 6：
// backups 应用模块跨类型单源——同过能力位门 + 同退避/同锚语义）。B6a 起能力位
// 按**引擎级**过滤（类型门保留为快路径——类型缺省 false 时零行扫描）：无
// 备份面的引擎（kafka——无官方 dump 工具，挂账）不铸台账行，红队 A9
// 「kafka 每 24h 铸必败行」防复发。
func (bk *Backups) schedulePubSubs(ctx context.Context, now time.Time) {
	if !dataservicetemplate.KindSupportsBackup(dataservicetemplate.KindPubSub) {
		return
	}
	rows, err := bk.repos.PubSubs.List(ctx)
	if err != nil {
		bk.log.Error("backup step: list pubsubs", "err", err)
		return
	}
	for i := range rows {
		row := &rows[i]
		if row.Status != pubsub.StatusRunning || row.BackupIntervalSecs <= 0 {
			continue
		}
		if !pubsubtemplate.EngineBacksUp(row.Engine) {
			continue // 引擎级无备份面：零行铸造（受理位 TriggerBackup 同拒）
		}
		active, err := bk.repos.Backups.HasActiveFor(ctx, row.ID)
		if err != nil {
			bk.log.Error("backup step: probe active", "pubsub", row.ID, "err", err)
			continue
		}
		if active {
			continue
		}
		if latest, ok, ferr := bk.repos.Backups.LatestFailedFor(ctx, row.ID); ferr != nil {
			bk.log.Error("backup step: probe last failure", "pubsub", row.ID, "err", ferr)
			continue
		} else if ok {
			if t, terr := time.Parse(time.RFC3339, latest.FinishedAt); terr == nil && now.Sub(t) < backupFailureBackoff {
				continue
			}
		}
		anchor := row.LastBackupAt
		if anchor == "" {
			anchor = row.CreatedAt
		}
		t, err := time.Parse(time.RFC3339, anchor)
		if err != nil {
			bk.log.Error("backup step: parse anchor", "pubsub", row.ID, "err", err)
			continue
		}
		if now.Sub(t) < time.Duration(row.BackupIntervalSecs)*time.Second {
			continue
		}
		b := &backup.Backup{
			ProjectID: row.ProjectID,
			Subject: backup.Subject{
				Kind: dataservicetemplate.KindPubSub,
				ID:   row.ID,
			},
			Engine: row.Engine, EngineVersion: row.Version, RetentionSecs: row.BackupRetentionSecs,
		}
		if err := bk.repos.Backups.Create(ctx, b); err != nil {
			bk.log.Error("backup step: schedule row", "pubsub", row.ID, "err", err)
		}
	}
}
