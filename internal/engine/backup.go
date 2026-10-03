package engine

// Backup 收敛环（F2.2，ADR-0039 决策 2/7）：调度（到期库铸台账行）→
// 执行（工具容器 stdout 流式 Put ObjectStore）→ 恢复挂起执行（流式三
// 引擎 / 预置卷 redis）→ 保留滚动（行驱动，行/对象成对删）。单写者文化
// 一次执行一件备份；执行带界（BackupTimeout 硬上限——单写者环卡死即
// 全部时间看门狗失明，ManagedStepTimeout 同源教训）。

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/engine/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/backup"
	dbrepo "github.com/fleetlyrun/fleetly/internal/state/database"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
)

// KickBackups 唤醒 Backup 环（API 手动触发面消费）。
func (e *Engine) KickBackups() { e.backupLoop.Kick() }

// backupStep 是 Backup 环的单次推进：恢复（用户在等，最高优先）→ 调度
// → 执行一件 → 保留滚动。
func (e *Engine) backupStep(ctx context.Context) {
	e.restorePass(ctx)
	e.schedulePass(ctx)
	e.executeOneBackup(ctx)
	e.prunePass(ctx)
}

// backupChainReady 报告执行链装配面（ObjectStore/Utility 缺席 = 备份停用，
// 装配期已日志；环空转零噪音）。
func (e *Engine) backupChainReady() bool {
	return e.objectStore != nil && e.utility != nil
}

// databasePassword 解密数据库凭证回读密码（备份/恢复渲染共用；单真源
// = Secret 里的完整连接串，ADR-0029 决策 6）。
func (e *Engine) databasePassword(ctx context.Context, row *dbrepo.Database) (string, error) {
	if e.cipher == nil {
		return "", fmt.Errorf("backup: database credentials require the master key")
	}
	sec, err := e.secrets.GetByName(ctx, e.db.Runner(), row.ProjectID, row.CredentialsRef)
	if err != nil {
		return "", fmt.Errorf("backup: lookup credential secret %q: %w", row.CredentialsRef, err)
	}
	plain, err := e.cipher.Open(sec.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("backup: decrypt credential secret %q: %w", row.CredentialsRef, err)
	}
	password, err := dbtemplate.PasswordFromURL(string(plain))
	if err != nil {
		return "", fmt.Errorf("backup: credential secret %q: %w", row.CredentialsRef, err)
	}
	return password, nil
}

// projectTeamOf 读 Project 所属 Team（工具容器 Namespace 锚；不存在的
// Project 返回空 Team——备份执行不因 Team 面缺席而停摆，载体名解析只
// 消费 Project 段）。
func (e *Engine) projectTeamOf(projectID string) string {
	team, err := e.projectTeam(context.Background(), projectID)
	if err != nil {
		return ""
	}
	return team
}

// schedulePass 到期库铸台账行（定时缺省 24h；锚 = last_backup_at，从未
// 成功退回 created_at——新库首个节拍 = 创建后一个 interval，手动触发补
// 即时面）。仅 running 库调度（非在服库的备份只有失败噪音）。
func (e *Engine) schedulePass(ctx context.Context) {
	rows, err := e.databases.List(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("backup step: list databases", "err", err)
		return
	}
	now := e.clock.Now()
	for i := range rows {
		row := &rows[i]
		if row.Status != dbrepo.StatusRunning || row.BackupIntervalSecs <= 0 {
			continue
		}
		active, err := e.backups.HasActiveFor(ctx, e.db.Runner(), row.ID)
		if err != nil {
			e.log.Error("backup step: probe active", "database", row.ID, "err", err)
			continue
		}
		if active {
			continue
		}
		anchor := row.LastBackupAt
		if anchor == "" {
			anchor = row.CreatedAt
		}
		t, err := time.Parse(time.RFC3339, anchor)
		if err != nil {
			e.log.Error("backup step: parse anchor", "database", row.ID, "err", err)
			continue
		}
		if now.Sub(t) < time.Duration(row.BackupIntervalSecs)*time.Second {
			continue
		}
		b := &backup.Backup{
			ID:            ulid.Make().String(),
			ProjectID:     row.ProjectID,
			DatabaseID:    row.ID,
			Engine:        row.Engine,
			RetentionSecs: row.BackupRetentionSecs,
		}
		if err := e.backups.Create(ctx, e.db.Runner(), b); err != nil {
			e.log.Error("backup step: schedule row", "database", row.ID, "err", err)
		}
	}
}

// executeOneBackup 执行最早一件 pending（单飞；同库双在途由 OldestPending
// 的在途闸兜底）。产物 = 工具容器 stdout 流式 Put（digest 由 Put 铸造，
// restore verify 校验锚）。
func (e *Engine) executeOneBackup(ctx context.Context) {
	if !e.backupChainReady() {
		return
	}
	b, ok, err := e.backups.OldestPending(ctx, e.db.Runner())
	if err != nil || !ok {
		return
	}
	execCtx, cancel := context.WithTimeout(ctx, e.opts.BackupTimeout)
	defer cancel()
	fail := func(errMsg string) {
		if ferr := e.backups.FinishFailed(ctx, e.db.Runner(), b.ID, errMsg); ferr != nil {
			e.log.Error("backup step: record failure", "backup", b.ID, "err", ferr)
		}
		e.emitBackupEvent(ctx, eventBackupFailed, b, errMsg)
	}
	db, err := e.databases.Get(ctx, e.db.Runner(), b.DatabaseID)
	if err != nil {
		fail(fmt.Sprintf("database row unavailable: %v", err))
		return
	}
	if db.Deleted() {
		fail("database deleted before backup executed")
		return
	}
	tpl, ok := dbtemplate.For(db.Engine)
	if !ok {
		fail(fmt.Sprintf("engine %q left the template registry", db.Engine))
		return
	}
	password, err := e.databasePassword(execCtx, db)
	if err != nil {
		fail(err.Error())
		return
	}
	spec, err := tpl.Backup(DatabaseDNSName(db.ID), password)
	if err != nil {
		fail(err.Error())
		return
	}
	if err := e.backups.MarkRunning(ctx, e.db.Runner(), b.ID); err != nil {
		e.log.Error("backup step: mark running", "backup", b.ID, "err", err) // 下拍重取件
		return
	}
	key := backup.KeyMint(db.ProjectID, db.ID, b.ID, e.clock.Now())
	// stdout → io.Pipe → Put（流经纪：Put 在调用方读，工具容器在 goroutine
	// 写；任一侧失败经 Pipe 错误传导到对端）。
	pr, pw := io.Pipe()
	stderr := &bytes.Buffer{}
	runErr := make(chan error, 1)
	go func() {
		err := e.utility.RunUtility(execCtx, capability.UtilityRequest{
			ID:          b.ID,
			Namespace:   capability.NamespaceRef{Team: e.projectTeamOf(db.ProjectID), Project: db.ProjectID, Database: db.ID},
			Image:       tpl.Image(),
			Argv:        spec.Argv,
			Env:         spec.Env,
			Networks:    e.projectNetworkNames(execCtx, db.ProjectID),
			SecretFiles: spec.SecretFiles,
		}, pw, stderr)
		// 收尾必关：成功 = 干净 EOF（Put 读尽完整产物）；失败 = 错误传导
		//（不关则 Put 永远阻塞在读面）。
		if err != nil {
			_ = pw.CloseWithError(err)
		} else {
			_ = pw.Close()
		}
		runErr <- err
	}()
	info, putErr := e.objectStore.Put(execCtx, key, pr)
	// 排空运行侧（Put 返回后工具容器可能仍在收尾；超时路径由 execCtx 收口）。
	if rerr := <-runErr; rerr != nil && putErr == nil {
		putErr = rerr
	}
	if putErr != nil {
		msg := fmt.Sprintf("backup execution failed: %v", putErr)
		if tail := stderrTail(stderr.String()); tail != "" {
			msg += ": " + tail
		}
		fail(msg)
		return
	}
	if err := e.backups.FinishSucceeded(ctx, e.db.Runner(), b.ID, key, info.Digest, info.Size); err != nil {
		e.log.Error("backup step: record success", "backup", b.ID, "err", err)
		return
	}
	if err := e.databases.SetLastBackupAt(ctx, e.db.Runner(), db.ID, state.FormatTime(e.clock.Now())); err != nil {
		e.log.Error("backup step: advance anchor", "database", db.ID, "err", err)
	}
	succ := *b
	succ.ObjectKey, succ.Digest, succ.SizeBytes = key, info.Digest, info.Size
	e.emitBackupEvent(ctx, eventBackupSucceeded, &succ, "")
}

// VerifyBackup 重算对象 sha256 比对 Put 回执（ADR-0039 决策 8：静态完整
// 性——腐损/截断可检出；API VerifyBackup 与恢复演练的共用单源）。ok=false
// 时 errText 携带可诊断差异（用户可见文本英文）。
func (e *Engine) VerifyBackup(ctx context.Context, backupID string) (bool, string, error) {
	if e.objectStore == nil {
		return false, "", fmt.Errorf("backup: object store is not assembled")
	}
	b, err := e.backups.Get(ctx, e.db.Runner(), backupID)
	if err != nil {
		return false, "", err
	}
	if b.Status != backup.StatusSucceeded || b.ObjectKey == "" {
		return false, "", fmt.Errorf("backup %s is not a succeeded backup with an object", backupID)
	}
	obj, err := e.objectStore.Get(ctx, b.ObjectKey)
	if err != nil {
		return false, "", fmt.Errorf("read backup object %q: %w", b.ObjectKey, err)
	}
	defer func() { _ = obj.Close() }()
	h := sha256.New()
	size, err := io.Copy(h, obj)
	if err != nil {
		return false, "", fmt.Errorf("stream backup object %q: %w", b.ObjectKey, err)
	}
	digest := hex.EncodeToString(h.Sum(nil))
	switch {
	case digest != b.Digest:
		return false, fmt.Sprintf("digest mismatch: object recomputes to %s, ledger records %s", digest, b.Digest), nil
	case size != b.SizeBytes:
		return false, fmt.Sprintf("size mismatch: object reads %d bytes, ledger records %d", size, b.SizeBytes), nil
	default:
		return true, "", nil
	}
}

// stderrTail 截取 stderr 尾部（错误报文面；有界防刷屏）。
func stderrTail(s string) string {
	const max = 512
	if len(s) <= max {
		return trimTrailing(s)
	}
	return trimTrailing(s[len(s)-max:])
}

func trimTrailing(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// restorePass 执行恢复挂起（ADR-0039 决策 6/8）：流式形态（pg/mysql/
// mongo——目标库 running 后 stdin 注入）与预置卷形态（redis——载体首启
// 前把 RDB 落进数据卷，databaseLoop 对挂起中的 preseed 库跳过 Ensure）。
func (e *Engine) restorePass(ctx context.Context) {
	if !e.backupChainReady() {
		return
	}
	rows, err := e.databases.ListRestorePending(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("restore pass: list pending", "err", err)
		return
	}
	for i := range rows {
		if ctx.Err() != nil {
			return
		}
		e.restoreDatabase(ctx, &rows[i])
	}
}

func (e *Engine) restoreDatabase(ctx context.Context, db *dbrepo.Database) {
	fail := func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		if err := e.databases.SetRestoreError(ctx, e.db.Runner(), db.ID, msg); err != nil {
			e.log.Error("restore: record failure", "database", db.ID, "err", err)
		}
		e.log.Error("restore failed", "database", db.ID, "err", msg)
	}
	src, err := e.backups.Get(ctx, e.db.Runner(), db.RestoreFromBackup)
	if err != nil {
		fail("source backup unavailable: %v", err)
		return
	}
	if src.Status != backup.StatusSucceeded || src.ObjectKey == "" {
		fail("source backup %s is not a succeeded backup with an object", src.ID)
		return
	}
	if src.Engine != db.Engine {
		fail("source backup engine %q does not match target engine %q", src.Engine, db.Engine)
		return
	}
	tpl, ok := dbtemplate.For(db.Engine)
	if !ok {
		fail("engine %q left the template registry", db.Engine)
		return
	}
	password, err := e.databasePassword(ctx, db)
	if err != nil {
		fail("%v", err)
		return
	}
	spec, err := tpl.Restore(DatabaseDNSName(db.ID), password)
	if err != nil {
		fail("%v", err)
		return
	}
	execCtx, cancel := context.WithTimeout(ctx, e.opts.BackupTimeout)
	defer cancel()
	object, err := e.objectStore.Get(execCtx, src.ObjectKey)
	if err != nil {
		fail("read backup object %q: %v", src.ObjectKey, err)
		return
	}
	defer func() { _ = object.Close() }()
	stderr := &bytes.Buffer{}
	req := capability.UtilityRequest{
		ID:          "restore-" + db.ID,
		Namespace:   capability.NamespaceRef{Team: e.projectTeamOf(db.ProjectID), Project: db.ProjectID, Database: db.ID},
		Image:       tpl.Image(),
		Argv:        spec.Argv,
		Env:         spec.Env,
		Networks:    e.projectNetworkNames(execCtx, db.ProjectID),
		SecretFiles: spec.SecretFiles,
		Stdin:       object,
	}
	switch spec.Mode {
	case dbtemplate.RestoreStream:
		if db.Status != dbrepo.StatusRunning {
			return // 目标库未在服：等下一拍（收敛环推进 status 后再来）
		}
	case dbtemplate.RestorePreseed:
		if db.Generation != 0 {
			fail("cannot preseed a database that has already started; delete and re-create it, then restore again")
			return
		}
		vol, err := e.prepareSeedVolume(execCtx, db)
		if err != nil {
			fail("%v", err)
			return
		}
		// VolumeID 与 Workload 投影同锚（平台卷名——Provider 卷载体名解析
		// 的唯一标识面；database_test 的 Volumes[0].VolumeID 同款事实）。
		req.Volume = &capability.UtilityVolumeMount{VolumeID: vol.Name, Target: dbtemplate.SeedMountPoint}
	default:
		fail("engine %q declares unknown restore mode %q", db.Engine, spec.Mode)
		return
	}
	if err := e.utility.RunUtility(execCtx, req, io.Discard, stderr); err != nil {
		msg := err.Error()
		if tail := stderrTail(stderr.String()); tail != "" {
			msg += ": " + tail
		}
		fail("restore execution failed: %s", msg)
		return
	}
	if err := e.databases.ClearRestoreSucceeded(ctx, e.db.Runner(), db.ID); err != nil {
		e.log.Error("restore: clear pending", "database", db.ID, "err", err)
		return
	}
	e.KickDatabases() // preseed 完成 → 立即收敛首启（装载预置数据）
	if _, err := e.outbox.Append(ctx, e.db.Runner(), eventDatabaseRestored, "database", db.ID,
		databaseRestoredEventJSON(db, src)); err != nil {
		e.log.Error("restore: emit event", "database", db.ID, "err", err)
	}
}

// prepareSeedVolume 保证挂靠卷行在场并钉住控制面节点（工具容器恒在控制
// 面 daemon 跑——预置落点与库首启调度点必须同节点，swarm 无卷感知调度，
// 平台钉住是既有唯一机制）。已钉到别处的卷 = 预置会落在错误节点，诚实
// 拒绝（用户先解除钉住或换名重建）。
func (e *Engine) prepareSeedVolume(ctx context.Context, db *dbrepo.Database) (*volume.Volume, error) {
	if err := e.ensureDatabaseVolume(ctx, db); err != nil {
		return nil, err
	}
	vol, err := e.volumes.GetByName(ctx, e.db.Runner(), db.ProjectID, db.Name)
	if err != nil {
		return nil, err
	}
	managerID, err := e.controlPlaneNode(ctx)
	if err != nil {
		return nil, err
	}
	if vol.PinnedNodeID == "" {
		if err := e.volumes.Pin(ctx, e.db.Runner(), db.ProjectID, vol.Name, managerID); err != nil {
			return nil, err
		}
	} else if vol.PinnedNodeID != managerID {
		return nil, fmt.Errorf("volume %q is pinned to node %s; restore pre-seeding requires the control-plane node %s", vol.Name, vol.PinnedNodeID, managerID)
	}
	return vol, nil
}

// controlPlaneNode 返回控制面节点的平台 ID（首个可用 manager——v1 单
// manager 形态下即 fleetlyd 所在节点；观测缓存，钉住动作幂等）。
func (e *Engine) controlPlaneNode(ctx context.Context) (string, error) {
	view, err := e.runtime.DescribeCluster(ctx)
	if err != nil {
		return "", fmt.Errorf("describe cluster for restore pre-seeding: %w", err)
	}
	for _, n := range view.Nodes {
		if n.Available && n.NodeID != "" && n.Role == "manager" {
			return n.NodeID, nil
		}
	}
	return "", fmt.Errorf("no available manager node for restore pre-seeding")
}

// prunePass 保留滚动（行驱动：created_at + 行上 retention 快照；行/对象
// 成对删，单键迟到缺失不阻断——Delete 幂等口径）。
func (e *Engine) prunePass(ctx context.Context) {
	due, err := e.backups.DueForPrune(ctx, e.db.Runner(), e.clock.Now(), 100)
	if err != nil {
		e.log.Error("prune pass: due rows", "err", err)
		return
	}
	for i := range due {
		b := &due[i]
		if b.ObjectKey != "" {
			if err := e.objectStore.Delete(ctx, b.ObjectKey); err != nil {
				e.log.Error("prune pass: delete object", "key", b.ObjectKey, "err", err)
				continue // 对象删除失败保行（下拍重试，行/对象不拆对）
			}
		}
		if err := e.backups.Delete(ctx, e.db.Runner(), b.ID); err != nil {
			e.log.Error("prune pass: delete row", "backup", b.ID, "err", err)
		}
	}
}

// emitBackupEvent 落备份事件（成功/失败统一形态）。
func (e *Engine) emitBackupEvent(ctx context.Context, name string, b *backup.Backup, errMsg string) {
	if _, err := e.outbox.Append(ctx, e.db.Runner(), name, "database", b.DatabaseID,
		backupEventPayloadJSON(b, errMsg)); err != nil {
		e.log.Error("backup: emit event", "backup", b.ID, "err", err)
	}
}

// sweepInterruptedBackups 启动清扫（重启打断的 running 行落 failed——
// resetOrphanBuilds 先例）。
func (e *Engine) sweepInterruptedBackups(ctx context.Context) {
	n, err := e.backups.SweepInterrupted(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("backup: sweep interrupted rows", "err", err)
		return
	}
	if n > 0 {
		e.log.Warn("backup: interrupted rows marked failed after restart", "count", n)
	}
}

// mustMarshalBackupPayload 序列化备份事件载荷（纯标量结构，Marshal 不
// 失败；失败即编程错误）。
func backupEventPayloadJSON(b *backup.Backup, errMsg string) []byte {
	out, _ := json.Marshal(backupEventPayload{
		BackupID:   b.ID,
		DatabaseID: b.DatabaseID,
		ProjectID:  b.ProjectID,
		Engine:     b.Engine,
		ObjectKey:  b.ObjectKey,
		Digest:     b.Digest,
		SizeBytes:  b.SizeBytes,
		Error:      errMsg,
	})
	return out
}
