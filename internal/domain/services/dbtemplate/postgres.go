package dbtemplate

// postgres 系模板（postgres / pgvector 共用基座；pgvector 嵌入覆盖差分面）。
// 凭证材料走密码文件：POSTGRES_PASSWORD_FILE 只含路径，argv/env 不落明文
// （ADR-0029 决策 6；ADR-0014 材料纪律）。
//
// B2a 版本化（ADR-0094 决策 7 首批矩阵）：16/17/18 在册、18 Default；tag
// 形 16/17-bookworm、18-trixie；digest 钉 index（scripts/digests.sh 拉取
// 回填，2026-11-05 Docker Hub 实测值）。跨版本差异唯一点 = PGDATA 布局：
// ≤17 维持显式 pgdata 子目录；PG18 起镜像默认版本化路径 18/docker（不设
// PGDATA env），DataTarget 全代统一父挂 /var/lib/postgresql（design §2.5）。

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// PasswordFile 是 postgres 系凭证材料文件名（容器内 /run/secrets/<名>）。
const PasswordFile = "database-password"

// pgRepo / pgvectorRepo 是 postgres 系两引擎的镜像仓库名（tag/digest 拼装
// 面；pgvector 嵌入基座但仓库不同——Image 由 pgvector 覆盖）。
const (
	pgRepo       = "postgres"
	pgvectorRepo = "pgvector/pgvector"
)

// pgVersion 是 postgres 系的在册版本条目（内核 VersionSpec + 系内差异轴：
// legacyPGData——≤17 显式 pgdata 子目录 / PG18 走镜像默认 18/docker；adapter
// 绑版分叉的唯一轴，design §2.5「仅 PGDATA 一处由 adapter 绑版分叉」）。
type pgVersion struct {
	enginetemplate.VersionSpec
	legacyPGData bool
}

// postgresVersions / pgvectorVersions 是 postgres 系两引擎的在册版本矩阵
// （ADR-0094 决策 7；声明序旧→新；digest = 2026-11-05 Docker Hub index
// 实测，scripts/digests.sh 对账——追 patch 经该脚本回填 + 门禁钉板同
// commit）。pgvector 二维矩阵（vector minor × pg major）以复合短名表达
// （0.8-pg17/0.8-pg18），组合合法性由本表自持（不建二维注册表结构）。
var (
	postgresVersions = []pgVersion{
		{
			VersionSpec: enginetemplate.VersionSpec{
				ID: "16", Tag: "16-bookworm",
				Digest: "sha256:5264710be20f409750ac6dc4e5706dcef929c87e241605463f0f9aefa932a8d9",
			},
			legacyPGData: true,
		},
		{
			VersionSpec: enginetemplate.VersionSpec{
				ID: "17", Tag: "17-bookworm",
				Digest: "sha256:665dc7b37ac52a17e298511414c21d23ffa4e6a6ef6225b3e5d59c7afb35e994",
			},
			legacyPGData: true,
		},
		{
			VersionSpec: enginetemplate.VersionSpec{
				ID: "18", Tag: "18-trixie",
				Digest:  "sha256:bd5dfa494a5a259704365813dbfa5fa6b3e69be7348d43b547f540c730478583",
				Default: true,
			},
		},
	}
	pgvectorVersions = []pgVersion{
		{
			VersionSpec: enginetemplate.VersionSpec{
				ID: "0.8-pg17", Tag: "0.8.7-pg17-bookworm",
				Digest: "sha256:7d0a41464797aeb9862dfdc0bbf1526b82cfd324cdd8036df19e3545f4a9ccb8",
			},
			legacyPGData: true,
		},
		{
			VersionSpec: enginetemplate.VersionSpec{
				ID: "0.8-pg18", Tag: "0.8.7-pg18-trixie",
				Digest:  "sha256:5895949dfcdf308eb0cd31c950c93c14895c77f33fbedfecf1fa511ab1d4bcd2",
				Default: true,
			},
		},
	}
)

// pgVersionsToSpecs 把系内版本表投影为内核 VersionSpec 列（Versions() 面）。
func pgVersionsToSpecs(table []pgVersion) []enginetemplate.VersionSpec {
	out := make([]enginetemplate.VersionSpec, len(table))
	for i, v := range table {
		out[i] = v.VersionSpec
	}
	return out
}

// pgVersionOf 在系内版本表按 ID 取完整条目（含 legacyPGData 差异轴；For
// 已按同一张表解析过 ID，未命中是防御态——保守回落显式 pgdata 形态，
// ≤17 与未知都走命名卷内显式 PGDATA，宁可保守不裸奔镜像默认路径）。
func pgVersionOf(table []pgVersion, spec enginetemplate.VersionSpec) pgVersion {
	for _, v := range table {
		if v.ID == spec.ID {
			return v
		}
	}
	return pgVersion{VersionSpec: spec, legacyPGData: true}
}

// postgresTemplate 是 postgres 引擎的模板 adapter（绑版实例持 version；
// 注册表原型零值仅承载目录面）。
type postgresTemplate struct {
	version pgVersion
}

func (postgresTemplate) Engine() string { return "postgres" }
func (postgresTemplate) Versions() []enginetemplate.VersionSpec {
	return pgVersionsToSpecs(postgresVersions)
}
func (t postgresTemplate) bindVersion(spec enginetemplate.VersionSpec) Template {
	return postgresTemplate{version: pgVersionOf(postgresVersions, spec)}
}
func (t postgresTemplate) Meta() Info          { return Info{Version: t.version.ID, Port: 5432} }
func (t postgresTemplate) ImageDigest() string { return t.version.Digest }
func (t postgresTemplate) Image() string {
	return pinnedRef(pgRepo+":"+t.version.Tag, t.version.Digest)
}

// DataTarget 全代统一挂父目录 /var/lib/postgresql（design §2.5）：≤17 镜像
// 在 /var/lib/postgresql/data 声明 VOLUME（嵌套在命名卷内部→匿名卷遮蔽，
// 显式 PGDATA 逃逸，见 pgDataDir 注）；PG18 镜像 VOLUME 声明随 18+ 目录
// 布局迁到 /var/lib/postgresql 本身——父挂与声明精确重合（挂载覆盖声明，
// 无匿名卷），镜像默认 PGDATA 18/docker 落命名卷内部。挂 data 子路径的
// 旧坑（2026-10 刷新镜像带 18+ 布局入口，docker-library/postgres#1259
// "unused mount" 拒启）由父挂消解；存量旧布局卷（数据在卷根）的迁移见
// runbook。
func (postgresTemplate) DataTarget() string { return "/var/lib/postgresql" }

// pgDataDir 是 ≤17 的显式 PGDATA（容器内路径），必须落在 DataTarget 挂载
// 面内部且避开镜像 VOLUME 声明路径 /var/lib/postgresql/data：镜像在该
// 路径声明 VOLUME，docker 对"嵌套在命名卷内部的镜像 VOLUME 路径"铸匿名
// 卷——遮蔽命名卷同名子目录（2026-10-04 staging 实录：每次任务替换 =
// 新匿名空卷 = initdb 空库，数据隐形丢失且 healthcheck 全绿掩盖）。显式
// PGDATA 把 initdb 与服务都钉进命名卷，任务替换数据存活（e2e 任务替换存活
// 锚 + B2a 双代 live 实证钉死该回归）。
const pgDataDir = "/var/lib/postgresql/pgdata"

// user / dbName 是 postgres 系的连接账号与库名（模板冻结值）。
const (
	pgUser   = "fleetly"
	pgDBName = "fleetly"
)

// PostgreSQLConfFile 是 postgres 系 static 旋钮的渲染材料文件名（容器内
// /run/secrets/<名>；postgresql.conf 覆盖片段，GUC 原生行语法）。
const PostgreSQLConfFile = "database-postgresql-conf"

// pgKnobs 是 postgres 系首批旋钮（B2b，design §3.4；pgvector 嵌入同集——
// 两引擎配置面同构）。分级依据 = 引擎原生 postmaster/sighup/user 上下文
// 知识：shared_buffers/max_connections 是 postmaster 上下文参数（重启
// 生效）= static；work_mem 是 user 上下文参数（ALTER SYSTEM +
// pg_reload_conf 免重启、postgresql.auto.conf 住数据卷自持久）= dynamic
// ——声明面先行，ApplyDynamicSettings/ApplySpec 随 B2d。Default 是引擎
// 原生缺省的目录回显面（渲染不因缺省补行——空 settings 渲染面与现状
// 逐字节一致）。
var pgKnobs = []enginetemplate.Knob{
	{Key: "shared_buffers", Kind: enginetemplate.KnobSize, Default: "128mb",
		Class: enginetemplate.KnobStatic, Category: "memory"},
	{Key: "max_connections", Kind: enginetemplate.KnobInt, Min: "1", Max: "10000", Default: "100",
		Class: enginetemplate.KnobStatic, Category: "connections"},
	{Key: "work_mem", Kind: enginetemplate.KnobSize, Default: "4mb",
		Class: enginetemplate.KnobDynamic, Category: "memory"},
}

// pgOwnedKeys 是 postgres 系的平台 owned 键闭集（ADR-0095 决策 2；design
// §3.1 红队 F-04 补全版，postgresql.conf 原生 GUC 词形，按族分配适用面）。
// 清单即守卫对照集（TestPlatformOwnedKeysUnreachable 与 Knobs 键集交集
// 恒空）——这些键永不可成为旋钮。
var pgOwnedKeys = []string{
	// 认证（口令哈希面；平台账号口令经 env 材料面注入，ADR-0029 决策 6）
	"password_encryption",
	// 监听与通告
	"listen_addresses", "port",
	// 数据目录（卷布局是平台所有权——父挂契约见 DataTarget 注）
	"data_directory", "log_directory",
	// 复制与集群
	"primary_conninfo",
	// 模块与 shell 执行面（预载库/动态库路径/归档恢复命令/口令短语命令）
	"shared_preload_libraries", "session_preload_libraries", "local_preload_libraries",
	"dynamic_library_path", "archive_command", "restore_command", "ssl_passphrase_command",
	// 文件包含与身份（include 族/HBA 与 identity 文件/证书密钥文件）
	"include", "include_dir", "include_if_exists", "hba_file", "ident_file",
	"ssl_key_file", "ssl_cert_file",
}

// Knobs 返回 postgres 系旋钮白名单（版本键控面：绑版实例可分叉——本批
// 各版本同集；副本防调用方改注册表）。
func (postgresTemplate) Knobs() []enginetemplate.Knob {
	out := make([]enginetemplate.Knob, len(pgKnobs))
	copy(out, pgKnobs)
	return out
}

// OwnedKeys 返回 postgres 系平台 owned 键闭集（pgvector 嵌入同集）。
func (postgresTemplate) OwnedKeys() []string {
	out := make([]string, len(pgOwnedKeys))
	copy(out, pgOwnedKeys)
	return out
}

// pgWorkloadEnv 渲染 postgres 系共用 env（绑版分叉唯一点：≤17 显式
// PGDATA；PG18 不设——镜像默认 18/docker 已落在父挂命名卷内部）。
func pgWorkloadEnv(t pgVersion) map[string]string {
	env := map[string]string{
		"POSTGRES_USER":          pgUser,
		"POSTGRES_DB":            pgDBName,
		"POSTGRES_PASSWORD_FILE": "/run/secrets/" + PasswordFile,
	}
	if t.legacyPGData {
		env["PGDATA"] = pgDataDir
	}
	return env
}

func (t postgresTemplate) Workload() (map[string]string, []string) {
	return pgWorkloadEnv(t.version), nil
}

func (postgresTemplate) Probe() []string {
	// 探针不带凭证：pg_isready 的成功回复只证明服务在服（连接握手成功），
	// 不要求认证通过。
	return []string{"pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", pgUser, "-d", pgDBName}
}

// Materials 渲染凭证材料 + static 旋钮折叠面（B2b，ADR-0095 决策 4）。PG
// 现无 conf 渲染：static 旋钮（shared_buffers/max_connections）折进
// postgresql.conf 覆盖片段材料（PostgreSQLConfFile，`key = value` 行、
// 排序稳定）——**渲染面先行**：本批现役消费点恒传空 map（不产该文件，
// 渲染面与现状逐字节一致）；命令面接线（`postgres -c <key>=<value>` 覆写
// 或 config_file 指向）随 B2d Workload 扩展（design §4.3 `tpl.Workload
// (staticOnly)` 预留）落地，届时该材料被命令面消费。空 settings 输出与
// 无旋钮现状逐字节一致（golden 零漂移钉死）。
func (postgresTemplate) Materials(password string, staticSettings map[string]string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	// 喂入律纵深：只消费本引擎 static 旋钮（dynamic/未知键结构性丢弃），
	// 值过渲染安全闸（fail-closed）。
	keys, static, err := staticKnobSettings(pgKnobs, staticSettings)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return map[string][]byte{PasswordFile: []byte(password)}, nil
	}
	var conf strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&conf, "%s = %s\n", k, static[k])
	}
	return map[string][]byte{
		PasswordFile:       []byte(password),
		PostgreSQLConfFile: []byte(conf.String()),
	}, nil
}

func (postgresTemplate) ConnURL(host, password string) string {
	u := &url.URL{
		Scheme: "postgresql",
		User:   url.UserPassword(pgUser, password),
		Host:   fmt.Sprintf("%s:%d", host, 5432),
		Path:   pgDBName,
	}
	return u.String()
}

// pgBackupPassFile 是备份/恢复工具的凭证材料文件名（PGPASSFILE 行格式
// host:port:db:user:password——冒号在该格式中不可转义，字符集闸是唯一
// 防线；与 Workload 的 POSTGRES_PASSWORD_FILE 形态分立，同名同内容会互
// 相污染语义）。
const pgBackupPassFile = "database-backup-pgpass"

// pgBackupMaterials 渲染备份/恢复共用的 pgpass 材料。
func pgBackupMaterials(host, password string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	return map[string][]byte{pgBackupPassFile: []byte(
		fmt.Sprintf("%s:5432:%s:%s:%s\n", host, pgDBName, pgUser, password))}, nil
}

func (postgresTemplate) Backup(host, password string) (enginetemplate.BackupSpec, error) {
	files, err := pgBackupMaterials(host, password)
	if err != nil {
		return enginetemplate.BackupSpec{}, err
	}
	// custom 格式（-Fc）：压缩 + TOC + 支持非寻位 stdin 恢复（pg_restore
	// 对管道输入整档缓冲）；产物走 stdout。
	return enginetemplate.BackupSpec{
		Argv:        []string{"pg_dump", "-h", host, "-p", "5432", "-U", pgUser, "-d", pgDBName, "--format=custom"},
		Env:         map[string]string{"PGPASSFILE": "/run/secrets/" + pgBackupPassFile},
		SecretFiles: files,
	}, nil
}

func (postgresTemplate) Restore(host, password string) (enginetemplate.RestoreSpec, error) {
	files, err := pgBackupMaterials(host, password)
	if err != nil {
		return enginetemplate.RestoreSpec{}, err
	}
	// 输入经文件挂载（BackupInputPath 单源——hijack stdin EOF 不可达，
	// ADR-0039 落地实录）；custom 格式恢复目标库（模板 env 面首启建库）。
	return enginetemplate.RestoreSpec{
		Mode:        enginetemplate.RestoreStream,
		Argv:        []string{"pg_restore", "-h", host, "-p", "5432", "-U", pgUser, "-d", pgDBName, "--no-password", enginetemplate.BackupInputPath},
		Env:         map[string]string{"PGPASSFILE": "/run/secrets/" + pgBackupPassFile},
		SecretFiles: files,
	}, nil
}

// RotatePassword 渲染数据面改密（IA v3 二期⑤b）：ALTER USER 平台账号；
// 认证走 pgpass 材料（current）。PASSWORD 字面值单引号插值的转义安全由
// validatePassword 字符集闸承担（与 mongo init 脚本同款纵深口径）。首启
// env 的 POSTGRES_PASSWORD_FILE 只在空卷 initdb 生效，改密必须走数据面。
func (postgresTemplate) RotatePassword(host, current, next string) (enginetemplate.RotateSpec, error) {
	if err := validatePassword(current); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	files, err := pgBackupMaterials(host, current)
	if err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	return enginetemplate.RotateSpec{
		Argv: []string{"psql", "-h", host, "-p", "5432", "-U", pgUser, "-d", pgDBName,
			"-v", "ON_ERROR_STOP=1", "--no-password",
			"-c", "ALTER USER " + pgUser + " WITH PASSWORD '" + next + "'"},
		Env:         map[string]string{"PGPASSFILE": "/run/secrets/" + pgBackupPassFile},
		SecretFiles: files,
	}, nil
}
