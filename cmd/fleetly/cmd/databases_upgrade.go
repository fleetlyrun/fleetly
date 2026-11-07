package cmd

// databases upgrade 编排向导（B7，ADR-0094 决策 5 / design §2.4 升级正典；
// 2026-11-05 用户终裁「CLI 侧编排」）：预览（无副作用）→ wide 确认 → 六步
// 串行，断点可重入。**纯客户端编排**——不落任何服务端编排行，状态即平台
// 行（库行/备份行/卷行）；中断重跑从断点续（每步先查目标态、达成即跳过，
// 每步独立幂等）。
//
// 最终步骤表（六步——设计 §2.4 五步 + 清理步，2026-11-07 实施批定形）：
//  ① 取恢复源（源库最新 succeeded 备份；无则 TriggerBackup+等成功+verify）
//     → 建中间库 <name>-upgrade-<ts> --version N --restore-from-backup ①源
//     （受理位 RestoreCompat 门把守版本轴：降级拒、pgvector 分轴）；
//  ② 等中间库 running（恢复完成）→ 强制 TriggerBackup 新库 + 等成功 +
//     VerifyBackup——①的备份到③删除之间的写入否则静默丢失（红队 A1）；
//  ③ 删源库（Secret 保留义不变）→ volumes purge 旧卷（容量回收；wide 档
//     确认经**执行时新鲜票**：PreviewPurgeVolume→redeem 零间隙兑换——预览
//     时点的票活不过备份时长，向导在 ③ 现场重铸）；
//  ④ 同名重建源名 --version N --restore-from-backup ②的新备份（恢复源
//     显式=②，红队 A1）→ 等 running + 恢复清位；
//  ⑤ 验证报告：行 diff（version/host）+ 引用 App 重部署提示；
//  ⑥ 清理中间库：删除 + purge 其卷（安全副本 = ②的已验证备份，中间卷
//     冗余可回收）。
//
// 断连窗披露（③→④ 期间 Secret database:<name> 指向已拆 host——引用 App
// 断连，时长 = ④恢复时长，大库小时级）：预览与报告如实披露。
//
// 断点重入锚：③ 之后源行已 tombstone——重跑须以**名字形态**（--project +
// name）进向导（id 形态找的是已删行）；重入时源名活跃行缺席 → 扫描
// <name>-upgrade-* 中间行续跑，from 版本面取预览缓存外的诚实降级（标
// resumed）。
//
// 确认形态：向导本身无服务端动词（无票可铸）——CLI 本地 wide 门（交互
// y/N / --yes 脚本通道 / --preview 两调用流第 1 步）；破坏性步 ③⑥ 的
// volumes purge 走服务端 wide 票（现场重铸兑换，审计 proof=redeem）。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/lynxland/commands"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	sdk "github.com/fleetlyrun/fleetly/sdk/go/fleetly"
)

// upgradeRPC 是向导消费的窄 RPC 面（既有动词组合——单测以 fake 驱动六步
// 编排与断点重入；生产面由 *sdk.Client 适配）。
type upgradeRPC interface {
	GetDatabase(ctx context.Context, in *structurev1.GetDatabaseRequest) (*structurev1.GetDatabaseResponse, error)
	ListDatabases(ctx context.Context, in *structurev1.ListDatabasesRequest) (*structurev1.ListDatabasesResponse, error)
	CreateDatabase(ctx context.Context, in *structurev1.CreateDatabaseRequest) (*structurev1.CreateDatabaseResponse, error)
	DeleteDatabase(ctx context.Context, in *structurev1.DeleteDatabaseRequest) (*structurev1.DeleteDatabaseResponse, error)
	TriggerBackup(ctx context.Context, in *structurev1.TriggerBackupRequest) (*structurev1.TriggerBackupResponse, error)
	ListBackups(ctx context.Context, in *structurev1.ListBackupsRequest) (*structurev1.ListBackupsResponse, error)
	VerifyBackup(ctx context.Context, in *structurev1.VerifyBackupRequest) (*structurev1.VerifyBackupResponse, error)
	ListVolumes(ctx context.Context, in *structurev1.ListVolumesRequest) (*structurev1.ListVolumesResponse, error)
	PreviewPurgeVolume(ctx context.Context, in *structurev1.PreviewPurgeVolumeRequest) (*structurev1.PreviewPurgeVolumeResponse, error)
	PurgeVolume(ctx context.Context, in *structurev1.PurgeVolumeRequest) (*structurev1.PurgeVolumeResponse, error)
}

// sdkUpgradeClient 把 *sdk.Client 适配为 upgradeRPC（字段委托）。
type sdkUpgradeClient struct{ c *sdk.Client }

func (s sdkUpgradeClient) GetDatabase(ctx context.Context, in *structurev1.GetDatabaseRequest) (*structurev1.GetDatabaseResponse, error) {
	return s.c.Databases.GetDatabase(ctx, in)
}
func (s sdkUpgradeClient) ListDatabases(ctx context.Context, in *structurev1.ListDatabasesRequest) (*structurev1.ListDatabasesResponse, error) {
	return s.c.Databases.ListDatabases(ctx, in)
}
func (s sdkUpgradeClient) CreateDatabase(ctx context.Context, in *structurev1.CreateDatabaseRequest) (*structurev1.CreateDatabaseResponse, error) {
	return s.c.Databases.CreateDatabase(ctx, in)
}
func (s sdkUpgradeClient) DeleteDatabase(ctx context.Context, in *structurev1.DeleteDatabaseRequest) (*structurev1.DeleteDatabaseResponse, error) {
	return s.c.Databases.DeleteDatabase(ctx, in)
}
func (s sdkUpgradeClient) TriggerBackup(ctx context.Context, in *structurev1.TriggerBackupRequest) (*structurev1.TriggerBackupResponse, error) {
	return s.c.Databases.TriggerBackup(ctx, in)
}
func (s sdkUpgradeClient) ListBackups(ctx context.Context, in *structurev1.ListBackupsRequest) (*structurev1.ListBackupsResponse, error) {
	return s.c.Databases.ListBackups(ctx, in)
}
func (s sdkUpgradeClient) VerifyBackup(ctx context.Context, in *structurev1.VerifyBackupRequest) (*structurev1.VerifyBackupResponse, error) {
	return s.c.Databases.VerifyBackup(ctx, in)
}
func (s sdkUpgradeClient) ListVolumes(ctx context.Context, in *structurev1.ListVolumesRequest) (*structurev1.ListVolumesResponse, error) {
	return s.c.Volumes.ListVolumes(ctx, in)
}
func (s sdkUpgradeClient) PreviewPurgeVolume(ctx context.Context, in *structurev1.PreviewPurgeVolumeRequest) (*structurev1.PreviewPurgeVolumeResponse, error) {
	return s.c.Volumes.PreviewPurgeVolume(ctx, in)
}
func (s sdkUpgradeClient) PurgeVolume(ctx context.Context, in *structurev1.PurgeVolumeRequest) (*structurev1.PurgeVolumeResponse, error) {
	return s.c.Volumes.PurgeVolume(ctx, in)
}

// upgradeWaitBudget 是各等待面的预算（running/备份成功/恢复清位——大库
// 小时级的诚实上界；到点报错带断点续跑指引）。
const (
	upgradeWaitRunning = 30 * time.Minute
	upgradeWaitBackup  = 30 * time.Minute
	upgradeWaitRestore = 30 * time.Minute
)

// upgradeWizard 是一次升级向导运行（六步编排 + 断点重入；零本地状态——
// 每步先查平台行，达成即跳过）。
type upgradeWizard struct {
	rpc       upgradeRPC
	projectID string // 解析后的项目锚（旗标给了 --project 即在位）
	sourceRef string // 用户面源引用（id 或裸名）
	targetVer string
	now       func() time.Time
	pollEvery time.Duration
	printStep func(format string, args ...any)

	// resolveSource 铸就的运行面（execute/composePlan 单源）。
	sourceName   string // 源库名（④ 同名重建的锚；resume 时来自 CLI 名形态）
	sourceEngine string
	fromVersion  string // 预览面 from 版本（resume 时未知则空——报告降级）
	sourceID     string // 源库行 id（resume 态空）
	resumed      bool   // ③ 后重入：源行缺席、经中间行续跑
	// resumedIntermediate 是 resume 态在场中间行的实名（⑥ 清理的卷名锚
	//——新时间戳名会指错清理目标）。
	resumedIntermediate string
}

// upgradePlan 是预览载荷（本地组装——无服务端向导动词；--json 原样输出）。
type upgradePlan struct {
	Database        string   `json:"database"`
	Engine          string   `json:"engine"`
	FromVersion     string   `json:"from_version"`
	ToVersion       string   `json:"to_version"`
	RestoreSource   string   `json:"restore_source_backup,omitempty"`
	SourceUnversion bool     `json:"source_backup_version_unrecorded,omitempty"`
	Resumed         bool     `json:"resumed,omitempty"`
	Intermediate    string   `json:"intermediate_name"`
	PurgeList       []string `json:"purge_list"`
	DisconnectNote  string   `json:"disconnect_window"`
	Steps           []string `json:"steps"`
}

func newDatabasesUpgradeVerb() commands.Command {
	const name = "upgrade"
	var project string
	var version string
	var preview, yes bool
	return &flaggedVerb{
		name:     name,
		synopsis: "Upgrade a database to a new engine major version (six-step wizard: intermediate restore -> forced backup -> delete+purge old -> recreate by name; resumable at every step)",
		usage:    "databases upgrade [--project PROJECT] DATABASE_NAME_OR_ID --version VERSION [--preview | --yes]",
		setFlags: func(fs *flag.FlagSet) {
			fs.StringVar(&project, "project", "", "project id or name (required to resume by name after step 3; resolves a bare database name)")
			fs.StringVar(&version, "version", "", "target engine major version short name (required; list legal values with 'fleetly engines list')")
			fs.BoolVar(&preview, "preview", false, "print the upgrade plan and exit (no side effects; re-run with --yes or interactively to execute)")
			fs.BoolVar(&yes, "yes", false, "execute the wizard without the interactive prompt (scripted/CI; the destructive purge steps still redeem fresh server-side tickets)")
		},
		run: func(ctx context.Context, env *commands.Environment, args []string, jsonOut bool) error {
			if len(args) != 1 {
				return usageErr(name, "expected exactly one DATABASE_NAME_OR_ID argument")
			}
			if version == "" {
				return usageErr(name, "--version is required (the target major; list legal values with 'fleetly engines list')")
			}
			if preview && yes {
				return usageErr(name, "--preview and --yes are mutually exclusive (pick one form)")
			}
			return runRPC(ctx, nil, func(ctx context.Context, c *sdk.Client) error {
				projectID := ""
				if project != "" {
					pid, err := resolveProjectID(ctx, c, project)
					if err != nil {
						return err
					}
					projectID = pid
				}
				w := &upgradeWizard{
					rpc: sdkUpgradeClient{c}, projectID: projectID, sourceRef: args[0], targetVer: version,
					now: time.Now, pollEvery: 2 * time.Second,
					printStep: func(format string, a ...any) {
						_, _ = fmt.Fprintf(env.Stdout, format+"\n", a...)
					},
				}
				plan, err := w.composePlan(ctx)
				if err != nil {
					return err
				}
				if preview {
					return renderUpgradePlan(env, jsonOut, plan)
				}
				if !yes {
					if !confirmIsTTY() {
						return fmt.Errorf(
							"databases upgrade requires confirmation and stdin is not a terminal: use --preview to review the plan, then --yes to execute")
					}
					if err := renderUpgradePlan(env, false, plan); err != nil {
						return err
					}
					confirmed, cerr := promptConfirm(env, "Run the upgrade wizard (data volumes will be purged mid-flight)")
					if cerr != nil {
						return cerr
					}
					if !confirmed {
						_, _ = fmt.Fprintln(env.Stdout, "aborted (nothing was sent)")
						return nil
					}
				}
				return w.execute(ctx, env, jsonOut, plan)
			})
		},
	}
}

// listDatabases 拉项目库行集（分页上限 200——向导面单项目库数上界足够）。
func (w *upgradeWizard) listDatabases(ctx context.Context) ([]*structurev1.Database, error) {
	resp, err := w.rpc.ListDatabases(ctx, &structurev1.ListDatabasesRequest{ProjectId: w.projectID, Limit: 200})
	if err != nil {
		return nil, err
	}
	return resp.GetDatabases(), nil
}

// findDatabase 按名找活跃库行（nil = 不在——断点重入的跳过判据）。
func (w *upgradeWizard) findDatabase(ctx context.Context, name string) (*structurev1.Database, error) {
	rows, err := w.listDatabases(ctx)
	if err != nil {
		return nil, err
	}
	for _, d := range rows {
		if d.GetName() == name {
			return d, nil
		}
	}
	return nil, nil
}

// intermediatePrefix 是中间库名前缀（<源名>-upgrade-；时间戳后缀使重跑新名
// 不撞已完成轮次，断点重入按前缀+版本扫描）。
func (w *upgradeWizard) intermediatePrefix() string {
	return w.sourceName + "-upgrade-"
}

// findIntermediate 扫描本升级的中间库（前缀 <源名>-upgrade- 且版本=目标；
// nil = ①未做）。
func (w *upgradeWizard) findIntermediate(ctx context.Context) (*structurev1.Database, error) {
	rows, err := w.listDatabases(ctx)
	if err != nil {
		return nil, err
	}
	prefix := w.intermediatePrefix()
	for _, d := range rows {
		if strings.HasPrefix(d.GetName(), prefix) && d.GetVersion() == w.targetVer && d.GetEngine() == w.sourceEngine {
			return d, nil
		}
	}
	return nil, nil
}

// latestSucceededBackup 取库最新 succeeded 备份（nil = 无）。
func (w *upgradeWizard) latestSucceededBackup(ctx context.Context, dbID string) (*structurev1.Backup, error) {
	resp, err := w.rpc.ListBackups(ctx, &structurev1.ListBackupsRequest{DatabaseId: dbID, Limit: 50})
	if err != nil {
		return nil, err
	}
	for _, b := range resp.GetBackups() {
		if b.GetStatus() == "succeeded" {
			return b, nil
		}
	}
	return nil, nil
}

// waitDatabaseState 等库达 running 且恢复清位（断连窗的主体 = ④ 的恢复
// 时长；restore_from_backup 在场 = 恢复在途）。
func (w *upgradeWizard) waitDatabaseState(ctx context.Context, dbID string, budget time.Duration) (*structurev1.Database, error) {
	deadline := w.now().Add(budget)
	for {
		resp, err := w.rpc.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: dbID})
		if err != nil {
			return nil, err
		}
		d := resp.GetDatabase()
		if d.GetRestoreFromBackup() == "" && d.GetStatus() == "running" {
			return d, nil
		}
		switch d.GetStatus() {
		case "degraded", "stopped":
			return nil, fmt.Errorf("database %s reached %s mid-upgrade (restore_error: %q) - fix or delete it, then re-run the wizard to resume", d.GetName(), d.GetStatus(), d.GetRestoreError())
		}
		if w.now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for database %s to settle (status %s, restore %q) - re-run the wizard to resume from this step", d.GetName(), d.GetStatus(), d.GetRestoreFromBackup())
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(w.pollEvery):
		}
	}
}

// waitBackupSucceeded 等库出现 succeeded 备份（触发后的完成事实面）。
func (w *upgradeWizard) waitBackupSucceeded(ctx context.Context, dbID string, budget time.Duration) (*structurev1.Backup, error) {
	deadline := w.now().Add(budget)
	for {
		b, err := w.latestSucceededBackup(ctx, dbID)
		if err != nil {
			return nil, err
		}
		if b != nil {
			return b, nil
		}
		if w.now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for a succeeded backup of %s - re-run the wizard to resume from this step", dbID)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(w.pollEvery):
		}
	}
}

// purgeVolumeWithFreshTicket 执行一次 volumes purge：现场重铸 wide 票并零
// 间隙兑换（预览时点的票活不过 ② 的备份时长——向导 ③⑥ 的服务端 wide 门
// 由本调用承载；审计 proof=redeem）。
//
// 卷释放重试窗（staging 记录·六十二实咬）：DeleteDatabase/TeardownDatabase
// 的载体拆除是异步收尾——卷引用在容器退出后短窗内仍在 docker 侧挂靠，
// purge 打「volume is in use」即败（两次实咬：step3 源卷/step6 中间卷，
// 60–90s 后自然可删）。向导在 [in-use] 形态上做有界重试（5s×36=3min，
// 复用 w.wait 的节拍），其余错误原样上抛——断点重入兜底不变（重跑续跑）。
func (w *upgradeWizard) purgeVolumeWithFreshTicket(ctx context.Context, volumeID string) (*structurev1.PurgeVolumeResponse, error) {
	var lastErr error
	for attempt := 0; attempt < 36; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
		preview, err := w.rpc.PreviewPurgeVolume(ctx, &structurev1.PreviewPurgeVolumeRequest{Id: volumeID})
		if err != nil {
			return nil, fmt.Errorf("purge preview for volume %s: %w", volumeID, err)
		}
		resp, err := w.rpc.PurgeVolume(ctx, &structurev1.PurgeVolumeRequest{
			Id:           volumeID,
			Confirmation: &deliveryv1.Confirmation{Ticket: preview.GetTicket()},
		})
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "volume is in use") {
			return nil, err // 非 in-use 形态：不重试（服务端已精确失败）
		}
	}
	return nil, fmt.Errorf("volume %s still in use after bounded retries (3min): %w - re-run the wizard to resume from this step", volumeID, lastErr)
}

// volumeRowByName 取项目内同名卷行（数据库挂靠卷名 = 库名；nil = 行不在
// ——已被 purge/手动删除的断点续判据）。
func (w *upgradeWizard) volumeRowByName(ctx context.Context, name string) (*structurev1.Volume, error) {
	resp, err := w.rpc.ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: w.projectID})
	if err != nil {
		return nil, err
	}
	for _, v := range resp.GetVolumes() {
		if v.GetName() == name {
			return v, nil
		}
	}
	return nil, nil
}

// purgeIntermediateVolumes 按前缀清光全部中间卷（<源名>-upgrade-*：本 run
// 的 + 历史 run 残留的——断点重入跨时间戳名的收口），回显已清卷名集。
func (w *upgradeWizard) purgeIntermediateVolumes(ctx context.Context) ([]string, error) {
	resp, err := w.rpc.ListVolumes(ctx, &structurev1.ListVolumesRequest{ProjectId: w.projectID})
	if err != nil {
		return nil, err
	}
	prefix := w.intermediatePrefix()
	var purged []string
	for _, v := range resp.GetVolumes() {
		if !strings.HasPrefix(v.GetName(), prefix) {
			continue
		}
		if _, err := w.purgeVolumeWithFreshTicket(ctx, v.GetId()); err != nil {
			return purged, fmt.Errorf("purge intermediate volume %s: %w", v.GetName(), err)
		}
		purged = append(purged, v.GetName())
	}
	return purged, nil
}

// resolveSource 铸就运行面（composePlan 前置）：名/ id 双形态解析源行；
// 源行缺席且名形态在场 → 扫中间行续跑（resume）；id 形态找已删行 = 诚实
// 指引重跑名形态。
func (w *upgradeWizard) resolveSource(ctx context.Context) error {
	if w.projectID == "" {
		// id 形态：项目锚从源行取（行删 = 无锚，指引名形态重跑）。
		resp, err := w.rpc.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: w.sourceRef})
		if err != nil {
			return fmt.Errorf("resolve source database %s: %w (if resuming after step 3 the source row is tombstoned - re-run by name with --project)", w.sourceRef, err)
		}
		d := resp.GetDatabase()
		w.projectID, w.sourceName, w.sourceEngine = d.GetProjectId(), d.GetName(), d.GetEngine()
		w.fromVersion, w.sourceID = d.GetVersion(), d.GetId()
		return nil
	}
	if mintedIDRe.MatchString(w.sourceRef) {
		resp, err := w.rpc.GetDatabase(ctx, &structurev1.GetDatabaseRequest{Id: w.sourceRef})
		if err == nil {
			d := resp.GetDatabase()
			if d.GetProjectId() != w.projectID {
				return fmt.Errorf("database %s belongs to a different project", w.sourceRef)
			}
			w.sourceName, w.sourceEngine = d.GetName(), d.GetEngine()
			w.fromVersion, w.sourceID = d.GetVersion(), d.GetId()
			return nil
		}
		// 行缺席：落名形态前提是 ref 本身是名——id 形态删行无中间锚可续。
		if !looksLikeUpgradeResumeName(w.sourceRef) {
			return fmt.Errorf("database %s: %v (if resuming after step 3 the source row is tombstoned - re-run by the database NAME with --project)", w.sourceRef, err)
		}
	}
	src, err := w.findDatabase(ctx, w.sourceRef)
	if err != nil {
		return err
	}
	if src != nil {
		w.sourceName, w.sourceEngine = src.GetName(), src.GetEngine()
		w.fromVersion, w.sourceID = src.GetVersion(), src.GetId()
		return nil
	}
	// resume：源行缺席——扫中间行（引擎面暂空，找到中间行后回填）。
	w.sourceName = w.sourceRef
	rows, err := w.listDatabases(ctx)
	if err != nil {
		return err
	}
	prefix := w.intermediatePrefix()
	for _, d := range rows {
		if strings.HasPrefix(d.GetName(), prefix) && d.GetVersion() == w.targetVer {
			w.sourceEngine = d.GetEngine()
			w.resumed = true
			w.resumedIntermediate = d.GetName()
			return nil
		}
	}
	return fmt.Errorf("database %q not found in the project (and no %s-upgrade-* intermediate on version %s to resume)", w.sourceRef, w.sourceRef, w.targetVer)
}

// looksLikeUpgradeResumeName 报告引用是否名形态（非 26 位 ULID——id 形态
// 删行无中间锚，须指引名重跑；名形态落 resume 扫描）。
func looksLikeUpgradeResumeName(ref string) bool {
	return !mintedIDRe.MatchString(ref)
}

// composePlan 组装预览（只读）：源行 + 恢复源备份 + purge 清单 + 断连窗 +
// 六步表。
func (w *upgradeWizard) composePlan(ctx context.Context) (*upgradePlan, error) {
	if err := w.resolveSource(ctx); err != nil {
		return nil, err
	}
	if !w.resumed && w.fromVersion == w.targetVer {
		return nil, fmt.Errorf("database %s already runs version %s (nothing to upgrade)", w.sourceName, w.targetVer)
	}
	interName := fmt.Sprintf("%s-upgrade-%d", w.sourceName, w.now().Unix())
	if w.resumed {
		interName = w.resumedIntermediate // ⑥ 按在场中间行实名清理。
	}
	plan := &upgradePlan{
		Database: w.sourceName, Engine: w.sourceEngine,
		FromVersion: w.fromVersion, ToVersion: w.targetVer,
		Resumed:      w.resumed,
		Intermediate: interName,
		DisconnectNote: "during step 4 the credential secret database:" + w.sourceName +
			" points at the torn-down host; referencing apps disconnect for the restore duration (potentially hours for large datasets)",
	}
	if !w.resumed {
		if b, err := w.latestSucceededBackup(ctx, w.sourceID); err != nil {
			return nil, err
		} else if b != nil {
			plan.RestoreSource = b.GetId()
			plan.SourceUnversion = b.GetEngineVersion() == ""
		}
		plan.PurgeList = []string{
			fmt.Sprintf("volume %s (data volume of database %s) at step 3 - unrecoverable", w.sourceName, w.sourceName),
			fmt.Sprintf("volume %s (intermediate data volume) at step 6 - unrecoverable", plan.Intermediate),
		}
	} else {
		plan.PurgeList = []string{
			fmt.Sprintf("volume %s (intermediate data volume) at step 6 - unrecoverable", plan.Intermediate),
		}
	}
	plan.Steps = []string{
		"create intermediate database " + plan.Intermediate + " on version " + w.targetVer + " restoring the step-1 source backup",
		"force a fresh backup of the intermediate (TriggerBackup + VerifyBackup)",
		"delete the source database and purge its old data volume (capacity recovery; unrecoverable)",
		"recreate " + plan.Database + " by name on version " + w.targetVer + " restoring the step-2 backup",
		"verify and report (referencing apps must be redeployed)",
		"delete the intermediate database and purge its volume",
	}
	return plan, nil
}

// renderUpgradePlan 渲染计划（人类/机器双形态）。
func renderUpgradePlan(env *commands.Environment, jsonOut bool, plan *upgradePlan) error {
	if jsonOut {
		data, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(env.Stdout, string(data))
		return err
	}
	lines := []string{
		fmt.Sprintf("upgrade plan for database %s", plan.Database),
		fmt.Sprintf("engine %s: version %s -> %s", plan.Engine, plan.FromVersion, plan.ToVersion),
	}
	if plan.Resumed {
		lines = append(lines, "mode: resuming an interrupted run (the source row is already gone; the wizard continues from the intermediate)")
	}
	if plan.RestoreSource != "" {
		note := ""
		if plan.SourceUnversion {
			note = " (source backup carries no recorded engine version - compatibility unverified, downgrade risk is yours)"
		}
		lines = append(lines, "step-1 restore source: latest backup "+plan.RestoreSource+note)
	} else {
		lines = append(lines, "step-1 restore source: none yet - the wizard triggers a fresh backup first")
	}
	lines = append(lines, "intermediate database: "+plan.Intermediate)
	lines = append(lines, "purge list (data destroyed, unrecoverable):")
	for _, p := range plan.PurgeList {
		lines = append(lines, "  - "+p)
	}
	lines = append(lines, "disconnect window: "+plan.DisconnectNote)
	lines = append(lines, "steps:")
	for i, s := range plan.Steps {
		lines = append(lines, fmt.Sprintf("  %d. %s", i+1, s))
	}
	_, err := fmt.Fprintln(env.Stdout, strings.Join(lines, "\n"))
	return err
}

// upgradeStepResult 是单步结果（--json 汇总的原料）。
type upgradeStepResult struct {
	Step   int    `json:"step"`
	Name   string `json:"name"`
	Status string `json:"status"` // done | skipped | info
	Detail string `json:"detail,omitempty"`
}

// execute 跑六步（每步先查目标态：达成即 skipped——断点可重入的兑现面）。
func (w *upgradeWizard) execute(ctx context.Context, env *commands.Environment, jsonOut bool, plan *upgradePlan) error {
	var results []upgradeStepResult
	record := func(step int, name, status, detail string) {
		results = append(results, upgradeStepResult{Step: step, Name: name, Status: status, Detail: detail})
		if !jsonOut {
			w.printStep("step %d/6 %s: %s (%s)", step, name, detail, status)
		}
	}

	interName := plan.Intermediate

	// ① 中间库（达成判据：同名前缀 + 目标版本的活跃行在场）。
	inter, err := w.findIntermediate(ctx)
	if err != nil {
		return err
	}
	if inter != nil {
		record(1, "create intermediate", "skipped", inter.GetName()+" already exists on version "+w.targetVer)
	} else {
		src, err := w.findDatabase(ctx, w.sourceName)
		if err != nil {
			return err
		}
		if src == nil {
			return fmt.Errorf("source database %q is gone before step 1 - nothing to upgrade (resume scan found no intermediate either)", w.sourceName)
		}
		b, err := w.latestSucceededBackup(ctx, src.GetId())
		if err != nil {
			return err
		}
		if b == nil {
			record(1, "create intermediate", "info", "no succeeded backup - triggering one on the source first")
			if _, err := w.rpc.TriggerBackup(ctx, &structurev1.TriggerBackupRequest{DatabaseId: src.GetId()}); err != nil {
				return err
			}
			if b, err = w.waitBackupSucceeded(ctx, src.GetId(), upgradeWaitBackup); err != nil {
				return err
			}
		}
		resp, err := w.rpc.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
			ProjectId: w.projectID, Name: interName, Engine: src.GetEngine(),
			Version: w.targetVer, RestoreFromBackup: b.GetId(),
		})
		if err != nil {
			return fmt.Errorf("step 1 create intermediate: %w (the restore-compat gate admits upgrades only)", err)
		}
		inter = resp.GetDatabase()
		record(1, "create intermediate", "done", inter.GetName()+" (id "+inter.GetId()+") restoring "+b.GetId())
	}

	// ② 强制新备份（达成判据：中间库有 succeeded 备份）。
	if _, err := w.waitDatabaseState(ctx, inter.GetId(), upgradeWaitRestore); err != nil {
		return fmt.Errorf("step 2: %w", err)
	}
	fresh, err := w.latestSucceededBackup(ctx, inter.GetId())
	if err != nil {
		return err
	}
	if fresh != nil {
		record(2, "forced backup", "skipped", fresh.GetId()+" already succeeded")
	} else {
		if _, err := w.rpc.TriggerBackup(ctx, &structurev1.TriggerBackupRequest{DatabaseId: inter.GetId()}); err != nil {
			return fmt.Errorf("step 2 trigger backup: %w", err)
		}
		if fresh, err = w.waitBackupSucceeded(ctx, inter.GetId(), upgradeWaitBackup); err != nil {
			return fmt.Errorf("step 2: %w", err)
		}
		record(2, "forced backup", "done", fresh.GetId())
	}
	vr, err := w.rpc.VerifyBackup(ctx, &structurev1.VerifyBackupRequest{BackupId: fresh.GetId()})
	if err != nil {
		return fmt.Errorf("step 2 verify backup %s: %w", fresh.GetId(), err)
	}
	if !vr.GetOk() {
		return fmt.Errorf("step 2 verification FAILED for backup %s: %s - the wizard stops before touching the source (re-run after fixing backups)", fresh.GetId(), vr.GetError())
	}

	// ③ 删源库 + purge 旧卷（达成判据：源行缺席 + 卷行缺席）。
	if src, serr := w.findDatabase(ctx, w.sourceName); serr == nil && src != nil {
		if _, err := w.rpc.DeleteDatabase(ctx, &structurev1.DeleteDatabaseRequest{Id: src.GetId()}); err != nil {
			return fmt.Errorf("step 3 delete source: %w", err)
		}
		record(3, "delete source", "done", w.sourceName+" (id "+src.GetId()+")")
	} else if serr != nil {
		return serr
	} else {
		record(3, "delete source", "skipped", w.sourceName+" already deleted")
	}
	if vol, verr := w.volumeRowByName(ctx, w.sourceName); verr != nil {
		return verr
	} else if vol != nil {
		if _, err := w.purgeVolumeWithFreshTicket(ctx, vol.GetId()); err != nil {
			return fmt.Errorf("step 3 purge volume %s: %w", w.sourceName, err)
		}
		record(3, "purge old volume", "done", w.sourceName+" (carrier key "+vol.GetId()+")")
	} else {
		record(3, "purge old volume", "skipped", "volume row "+w.sourceName+" already gone")
	}

	// ④ 同名重建（达成判据：源名活跃行在且版本=目标）。
	final, err := w.findDatabase(ctx, w.sourceName)
	if err != nil {
		return err
	}
	if final != nil && final.GetVersion() == w.targetVer {
		record(4, "recreate by name", "skipped", w.sourceName+" already on version "+w.targetVer)
	} else if final != nil {
		return fmt.Errorf("step 4: database %s exists on version %s (want %s) - resolve manually before resuming", w.sourceName, final.GetVersion(), w.targetVer)
	} else {
		resp, cerr := w.rpc.CreateDatabase(ctx, &structurev1.CreateDatabaseRequest{
			ProjectId: w.projectID, Name: w.sourceName, Engine: inter.GetEngine(),
			Version: w.targetVer, RestoreFromBackup: fresh.GetId(),
		})
		if cerr != nil {
			return fmt.Errorf("step 4 recreate %s: %w", w.sourceName, cerr)
		}
		final = resp.GetDatabase()
		record(4, "recreate by name", "done", w.sourceName+" (id "+final.GetId()+") restoring "+fresh.GetId())
	}
	settled, err := w.waitDatabaseState(ctx, final.GetId(), upgradeWaitRestore)
	if err != nil {
		return fmt.Errorf("step 4: %w", err)
	}
	// ⑤ 验证报告（info 步——无副作用）。
	record(5, "verify and report", "done",
		fmt.Sprintf("%s runs %s %s at %s:%d", settled.GetName(), settled.GetEngine(), settled.GetVersion(), settled.GetHost(), settled.GetPort()))

	// ⑥ 清理中间库（达成判据：中间行缺席 + 卷行缺席）。卷清理按**前缀**
	// 扫描（<源名>-upgrade-*）——① 后重入会在新时间戳名下复用旧中间行，
	// 按本 run 的 interName 查卷会漏清上一 run 的中间卷。
	if inter, err = w.findIntermediate(ctx); err != nil {
		return err
	} else if inter != nil {
		if _, err := w.rpc.DeleteDatabase(ctx, &structurev1.DeleteDatabaseRequest{Id: inter.GetId()}); err != nil {
			return fmt.Errorf("step 6 delete intermediate: %w", err)
		}
		record(6, "cleanup intermediate", "done", inter.GetName()+" deleted")
	} else {
		record(6, "cleanup intermediate", "skipped", "intermediate already gone")
	}
	purged, err := w.purgeIntermediateVolumes(ctx)
	if err != nil {
		return fmt.Errorf("step 6: %w", err)
	}
	if len(purged) > 0 {
		record(6, "purge intermediate volume", "done", strings.Join(purged, ", "))
	} else {
		record(6, "purge intermediate volume", "skipped", "no "+w.intermediatePrefix()+"* volume row remains")
	}

	// 收尾报告（人类形态：断连窗兑现提示 + 引用 App 重部署）。
	if jsonOut {
		summary := struct {
			Database    string              `json:"database"`
			Engine      string              `json:"engine"`
			Version     string              `json:"version"`
			FromVersion string              `json:"from_version"`
			Host        string              `json:"host"`
			Steps       []upgradeStepResult `json:"steps"`
			Redeploy    string              `json:"redeploy_note"`
		}{
			Database: settled.GetName(), Engine: settled.GetEngine(), Version: settled.GetVersion(),
			FromVersion: plan.FromVersion, Host: settled.GetHost(), Steps: results,
			Redeploy: "referencing apps keep the old host until redeployed - redeploy them now to pick up " + settled.GetHost(),
		}
		data, merr := json.Marshal(summary)
		if merr != nil {
			return merr
		}
		_, err = fmt.Fprintln(env.Stdout, string(data))
		return err
	}
	w.printStep("upgrade complete: %s now runs %s %s (was %s) at %s:%d",
		settled.GetName(), settled.GetEngine(), settled.GetVersion(), plan.FromVersion, settled.GetHost(), settled.GetPort())
	w.printStep("referencing apps keep the old host until redeployed - redeploy them now (secret database:%s points at %s)",
		settled.GetName(), settled.GetHost())
	return nil
}
