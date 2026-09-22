package dbtemplate

import "context"

// EngineAdapter 是库引擎备份/恢复/轮换的适配器端口（managed-databases
// 设计 §2.6 代码块逐字——每引擎一个实现；conformance 套件随 MySQL/Mongo
// 接入时补齐，首两引擎单实现、先钉接口不建套件）。实现随 S5 备份恢复票据
// 落地；本包只钉接口与输入/产出类型。
//
// 备份基础设施（E3 restic + 外部 S3 / opt-in RustFS）全量复用：repo 内
// 独立命名空间 db/<instance>/（与控制面快照分立）；执行体 = 一次性 Swarm
// job（同放置约束钉节点——远端节点 local 卷不可经 manager 读是既有硬
// 约束），凭据与 S3 目标经 Swarm secret 注入。
type EngineAdapter interface {
	// Backup 逻辑备份：一次性 Swarm job（钉实例绑定节点、挂实例卷）内
	// 流式导出 → restic 入库（repo 内路径 db/<instance>/<snapshot>）。
	Backup(ctx context.Context, in BackupInput) (BackupOutcome, error)
	// Restore 原地恢复：实例 scale 0 → job 挂卷 rw 重放 → 重部署。
	Restore(ctx context.Context, in RestoreInput) error
	// Verify 回读校验：restic 读回 + 引擎级头部校验（pg_restore --list /
	// RDB magic），台账 verify_status 置位——「备份假成功」零容忍。
	Verify(ctx context.Context, in BackupOutcome) error
	// RotateCredential 引擎侧热轮换（§2.5）。
	RotateCredential(ctx context.Context, in RotateInput) error
}

// BackupKind 是备份类别词表（db_backups.kind 列；与 state 侧词表同串——
// 本包不 import state，字符串形态经接口传递）。
type BackupKind string

const (
	// BackupDaily 定时备份（每日计划）。
	BackupDaily BackupKind = "daily"
	// BackupManual 手动触发。
	BackupManual BackupKind = "manual"
	// BackupPreUpgrade 升级前门禁备份（verify 通过才继续升级）。
	BackupPreUpgrade BackupKind = "pre_upgrade"
)

// BackupInput 是一次逻辑备份的输入（S5 收窄定型：编排层解析全部事实与
// 材料——repo 目标、放置绑定、解密后的凭据明文——适配器只拼装 job 载荷；
// 凭据明文字段只存活于内存 → job env，零落日志/事件/错误）。
type BackupInput struct {
	// Instance/TemplateID 是目标实例名与模板 ID（引擎工具集与端口由模板定）。
	Instance   string
	TemplateID string
	// Kind 取 Backup* 词表。
	Kind BackupKind
	// VolumeName 是实例数据卷 docker 名（job 钉绑定节点、挂卷执行）。
	VolumeName string
	// BindNodeID 是放置绑定节点（job 的放置约束锚——远端卷不可经 manager
	// 读的硬约束）。
	BindNodeID string
	// Repository 是 restic 仓库目标（E3 基础设施：外部 S3 端点或 opt-in
	// RustFS）；repo 内命名空间 db/<instance>/ 由适配器拼装。
	Repository string
	// Password 是实例引擎凭据明文（pg_dump PGPASSWORD / redis REDISCLI_AUTH
	// ——只进 job env）。
	Password string
	// ResticPassword 是 repo 口令明文（与控制面上传轨同一把——只进 job env）。
	ResticPassword string
	// S3AccessKeyID/S3SecretKey/S3Region 是解密后的端点凭证（只进 job env）。
	S3AccessKeyID string
	S3SecretKey   string
	S3Region      string
	// S3PathStyle 是 path-style 寻址位（true → restic 扩展选项
	// `-o s3.bucket-lookup=path`——statebackup 上传轨同口径）。
	S3PathStyle bool
	// AttachRustfsNetwork 表示 job 需挂接 fleetly-rustfs-net（rustfs 模式
	// 的托管端点只在该网可解析）。
	AttachRustfsNetwork bool
}

// BackupOutcome 是一次备份的产出（落 db_backups 台账：restic_snapshot 寻址
// 非文件路径）。S5 扩充：快照的执行上下文随行——Verify 的回读在**同一**
// repo/节点/网络重放（校验是备份的影子作业，材料不二次解析）。
type BackupOutcome struct {
	// SnapshotID 是 restic repo 内 snapshot 标识。
	SnapshotID string
	// SizeBytes 是导出流字节量。
	SizeBytes int64
	// ── Verify 回读上下文（备份时已裁决的事实）──
	Instance            string
	TemplateID          string
	BindNodeID          string
	Repository          string
	ResticPassword      string
	S3AccessKeyID       string
	S3SecretKey         string
	S3Region            string
	S3PathStyle         bool
	AttachRustfsNetwork bool
}

// RestoreInput 是一次原地恢复的输入（confirm 破坏性确认在 API 层，S2/S4；
// S5 收窄定型：VolumeTarget 是数据卷挂载点（模板 VolumeMountPath——恢复
// job 挂卷 rw 重放），材料字段语义同 BackupInput）。
type RestoreInput struct {
	Instance   string
	TemplateID string
	VolumeName string
	BindNodeID string
	Repository string
	// SnapshotID 是恢复目标（restic snapshot 标识）。
	SnapshotID string
	// VolumeTarget 是数据卷在 job 容器内的挂载点。
	VolumeTarget string
	// ResticPassword 是 repo 口令明文（只进 job env）。
	ResticPassword string
	// S3AccessKeyID/S3SecretKey/S3Region 是解密后的端点凭证（只进 job env）。
	S3AccessKeyID string
	S3SecretKey   string
	S3Region      string
	S3PathStyle         bool
	AttachRustfsNetwork bool
}

// RotateInput 是一次凭据热轮换的输入（§2.5：PG = 一次性 job 执行 ALTER
// USER 热轮换；Redis = 密文列更新 + 受控重启 requirepass 重读）。新凭据
// 明文只存活于内存（不进日志/事件/错误）。
type RotateInput struct {
	Instance   string
	TemplateID string
	VolumeName string
	BindNodeID string
	// NewPassword 是新凭据明文。
	NewPassword string
}
