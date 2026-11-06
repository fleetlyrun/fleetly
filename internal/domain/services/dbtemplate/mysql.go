package dbtemplate

// mysql 模板（F2.1 矩阵扩展；B2a 版本化，ADR-0094 决策 7）：在册 8.4/9、
// 9 Default（8.0 已 EOL 2026-04 不入册；9 = innovation track 短在册窗由
// EOL 看护承载）。官方镜像入口脚本支持全部敏感变量的 _FILE 形态——app
// 账号密码走文件（argv/env 不落明文，ADR-0014 材料纪律）。root 密码与
// app 密码同值双文件：Secret 真源是单条连接串（ADR-0029 决策 6），root
// 是容器内管理位、不进任何回显面——同值是单真源约束下的最小诚实形态，
// 隔离增益为零但攻击面不扩大（两文件同卷同权限）。跨版本零差异（版本
// 分叉仅镜像面）。

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// MySQL 凭证材料文件名（容器内 /run/secrets/<名>）。
const (
	MySQLPasswordFile     = "database-password"
	MySQLRootPasswordFile = "database-root-password"
)

// mysqlUser / mysqlDBName 是 mysql 的连接账号与库名（模板冻结值）。
const (
	myUser   = "fleetly"
	myDBName = "fleetly"
)

// mysqlRepo 是 mysql 引擎的镜像仓库名；mysqlVersions 是在册版本矩阵
// （ADR-0094 决策 7；digest = 2026-11-05 Docker Hub index 实测，
// scripts/digests.sh 对账；9 取精确 minor 9.7——tag 形 9.x，minor 钉定、
// patch 随 digest 钉）。
const mysqlRepo = "mysql"

var mysqlVersions = []enginetemplate.VersionSpec{
	{
		ID: "8.4", Tag: "8.4",
		Digest: "sha256:a81d043930e51745522bd5c430961cf8286f0a23f0a9a2a6e0108f7ea71b0902",
	},
	{
		ID: "9", Tag: "9.7",
		Digest:  "sha256:df90258da46f6e7ff8574ff84f17618aba0765cac749928299c69fd4e7cb8ad8",
		Default: true,
	},
}

// mysqlTemplate 是 mysql 引擎的模板 adapter（绑版实例持 version；注册表
// 原型零值仅承载目录面）。
type mysqlTemplate struct {
	version enginetemplate.VersionSpec
}

// mysqlKnobs 是 mysql 首批旋钮（B2b，design §3.4）——全 dynamic：MySQL
// `SET PERSIST` 自持久（mysqld-auto.cnf 住数据目录，8.4/9 全系支持、重启
// 自愈），innodb_buffer_pool_size 支持在线调。声明面先行（ApplySpec 随
// B2d）；static 渲染子集恒空（喂入律下 Materials 不消费 dynamic 键）。
var mysqlKnobs = []enginetemplate.Knob{
	{Key: "max_connections", Kind: enginetemplate.KnobInt, Default: "151",
		Class: enginetemplate.KnobDynamic, Category: "connections"},
	{Key: "innodb_buffer_pool_size", Kind: enginetemplate.KnobSize, Default: "128mb",
		Class: enginetemplate.KnobDynamic, Category: "memory"},
}

// mysqlOwnedKeys 是 mysql 的平台 owned 键闭集（ADR-0095 决策 2；design
// §3.1 红队 F-04 补全版，mysqld 系统变量下划线词形 + option-file 指令
// 原生 ! 词形）。清单即守卫对照集（TestPlatformOwnedKeysUnreachable）。
var mysqlOwnedKeys = []string{
	// 监听与通告
	"bind_address", "port",
	// 数据目录（datadir 及 InnoDB 物理home目录——卷布局是平台所有权）
	"datadir", "innodb_data_home_dir", "innodb_log_group_home_dir",
	// 复制与集群
	"server_id", "log_bin",
	// 模块与 shell 执行面（init_file = 启动 SQL 执行面；plugin_load* = 模块装载）
	"init_file", "plugin_load", "plugin_load_add",
	// 文件包含与身份（option-file include 指令带 ! 原生词形；ssl_key/cert = 证书身份面）
	"!include", "!includedir", "ssl_key", "ssl_cert",
}

// Knobs 返回 mysql 旋钮白名单（版本键控面：绑版实例可分叉——本批各版本
// 同集；副本防调用方改注册表）。
func (mysqlTemplate) Knobs() []enginetemplate.Knob {
	out := make([]enginetemplate.Knob, len(mysqlKnobs))
	copy(out, mysqlKnobs)
	return out
}

// OwnedKeys 返回 mysql 平台 owned 键闭集。
func (mysqlTemplate) OwnedKeys() []string {
	out := make([]string, len(mysqlOwnedKeys))
	copy(out, mysqlOwnedKeys)
	return out
}

func (mysqlTemplate) Engine() string { return "mysql" }
func (mysqlTemplate) Versions() []enginetemplate.VersionSpec {
	out := make([]enginetemplate.VersionSpec, len(mysqlVersions))
	copy(out, mysqlVersions)
	return out
}
func (mysqlTemplate) bindVersion(spec enginetemplate.VersionSpec) Template {
	return mysqlTemplate{version: spec}
}
func (t mysqlTemplate) Meta() Info { return Info{Version: t.version.ID, Port: 3306} }
func (t mysqlTemplate) Image() string {
	return pinnedRef(mysqlRepo+":"+t.version.Tag, t.version.Digest)
}
func (t mysqlTemplate) ImageDigest() string { return t.version.Digest }
func (mysqlTemplate) DataTarget() string    { return "/var/lib/mysql" }

func (mysqlTemplate) Workload(_ map[string]string) (map[string]string, []string) {
	return map[string]string{
		"MYSQL_DATABASE":           myDBName,
		"MYSQL_USER":               myUser,
		"MYSQL_PASSWORD_FILE":      "/run/secrets/" + MySQLPasswordFile,
		"MYSQL_ROOT_PASSWORD_FILE": "/run/secrets/" + MySQLRootPasswordFile,
	}, nil
}

func (mysqlTemplate) Probe() []string {
	// 探针不带凭证：mysqladmin ping 对"连上但被拒（Access denied）"同样
	// 退出 0——拒绝本身就是服务在服的证明（mysql 文档口径，pg_isready
	// 同语义）。
	return []string{"mysqladmin", "ping", "-h", "127.0.0.1"}
}

// Materials 渲染凭证材料 + static 旋钮折叠面（B2b，ADR-0095 决策 4）：
// mysql 首批旋钮全 dynamic（SET PERSIST 自持久），static 子集恒空——
// Materials 输出不随 settings 变（喂入律：dynamic 键结构性不进材料，
// staticKnobSettings 纵深过滤兜底）。
func (mysqlTemplate) Materials(password string, staticSettings map[string]string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	if _, _, err := staticKnobSettings(mysqlKnobs, staticSettings); err != nil {
		return nil, err
	}
	return map[string][]byte{
		MySQLPasswordFile:     []byte(password),
		MySQLRootPasswordFile: []byte(password),
	}, nil
}

func (mysqlTemplate) ConnURL(host, password string) string {
	u := &url.URL{
		Scheme: "mysql",
		User:   url.UserPassword(myUser, password),
		Host:   fmt.Sprintf("%s:%d", host, 3306),
		Path:   myDBName,
	}
	return u.String()
}

// myBackupDefaultsFile 是备份/恢复工具的凭证材料文件名（mysql 客户端
// --defaults-extra-file 的 INI 形态；ini 单节断言见 injection 家族测试）。
const myBackupDefaultsFile = "database-backup-defaults"

// myBackupMaterials 渲染 [client] INI（备份/恢复共用；--defaults-extra-file
// 必须是首参——mysql 客户端只读首参位置的 defaults 文件，后置即静默无效）。
func myBackupMaterials(host, password string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	return map[string][]byte{myBackupDefaultsFile: []byte(
		"[client]\nhost=" + host + "\nuser=" + myUser + "\npassword=" + password + "\n")}, nil
}

func (mysqlTemplate) Backup(host, password string) (enginetemplate.BackupSpec, error) {
	files, err := myBackupMaterials(host, password)
	if err != nil {
		return enginetemplate.BackupSpec{}, err
	}
	// --single-transaction：InnoDB 一致快照不锁表；SQL 文本 dump 走 stdout。
	return enginetemplate.BackupSpec{
		Argv: []string{"mysqldump", "--defaults-extra-file=/run/secrets/" + myBackupDefaultsFile,
			"--single-transaction", "--routines", "--triggers", "--events", myDBName},
		SecretFiles: files,
	}, nil
}

func (mysqlTemplate) Restore(host, password string) (enginetemplate.RestoreSpec, error) {
	files, err := myBackupMaterials(host, password)
	if err != nil {
		return enginetemplate.RestoreSpec{}, err
	}
	// 目标库由模板 env 面（MYSQL_DATABASE）首启建好；SQL 流经文件挂载
	//（source 客户端命令读文件——hijack stdin EOF 不可达，ADR-0039 实录）。
	return enginetemplate.RestoreSpec{
		Mode:        enginetemplate.RestoreStream,
		Argv:        []string{"mysql", "--defaults-extra-file=/run/secrets/" + myBackupDefaultsFile, "-e", "source " + enginetemplate.BackupInputPath, myDBName},
		SecretFiles: files,
	}, nil
}

// RotatePassword 渲染数据面改密（IA v3 二期⑤b）：ALTER USER 平台账号
// （首启 entrypoint 铸的是 'fleetly'@'%'）；认证走 defaults 文件（current）。
// IDENTIFIED BY 单引号插值的转义安全由 validatePassword 字符集闸承担。
// MYSQL_PASSWORD_FILE 只在空卷首启建号生效，改密必须走数据面。
func (mysqlTemplate) RotatePassword(host, current, next string) (enginetemplate.RotateSpec, error) {
	if err := validatePassword(current); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	files, err := myBackupMaterials(host, current)
	if err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	return enginetemplate.RotateSpec{
		Argv: []string{"mysql", "--defaults-extra-file=/run/secrets/" + myBackupDefaultsFile,
			"-e", "ALTER USER '" + myUser + "'@'%' IDENTIFIED BY '" + next + "'"},
		SecretFiles: files,
	}, nil
}

// ApplyDynamicSettings 渲染 dynamic 旋钮热应用（B2d，ADR-0095 决策 4）：
// 命令面 `SET PERSIST <key> = <value>`（8.4/9 全系支持；innodb_buffer_pool_size
// 在线调）；多旋钮 = 单 -e 多语句（";" 分隔——客户端逐语句执行，任一失败
// 即整体非零退出，部分失败由调用方的失败强制 Forget 拉回语义收口）。持久
// 化 = mysqld-auto.cnf 住数据目录，重启自愈。凭证复用备份方言的
// defaults-extra-file 材料面（INI 单节；--defaults-extra-file 必须首参）。
// 值经 mysqlNativeValue 渲染为 SET 语句原生字面，且已被
// ValidateDynamicSettings 约束在渲染安全字符集。settings 为空 = 防御态
// （正常分流不可达——mysql 无 static 旋钮，SplitSettings 的 static 子集
// 恒空），返回无 dynamic 面哨兵。
func (mysqlTemplate) ApplyDynamicSettings(host, password string, settings map[string]string) (enginetemplate.ApplySpec, error) {
	if err := validatePassword(password); err != nil {
		return enginetemplate.ApplySpec{}, err
	}
	keys, dyn, err := enginetemplate.ValidateDynamicSettings(mysqlKnobs, settings)
	if err != nil {
		return enginetemplate.ApplySpec{}, err
	}
	if len(keys) == 0 {
		return enginetemplate.ApplySpec{}, enginetemplate.ErrNoDynamicFace
	}
	files, err := myBackupMaterials(host, password)
	if err != nil {
		return enginetemplate.ApplySpec{}, err
	}
	byKey := make(map[string]enginetemplate.Knob, len(mysqlKnobs))
	for _, k := range mysqlKnobs {
		byKey[k.Key] = k
	}
	stmts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, err := mysqlNativeValue(byKey[k], dyn[k])
		if err != nil {
			return enginetemplate.ApplySpec{}, err
		}
		stmts = append(stmts, "SET PERSIST "+k+" = "+v)
	}
	return enginetemplate.ApplySpec{
		Argv: []string{"mysql", "--defaults-extra-file=/run/secrets/" + myBackupDefaultsFile,
			"-e", strings.Join(stmts, "; ")},
		SecretFiles: files,
	}, nil
}

// mysqlNativeValue 把受理口径的旋钮值渲染为 MySQL SET 语句原生字面（B2d）：
// SET 语句不收单位后缀（K/M/G 只在 option file 与命令行合法——"128mb"
// 在 SET 右值即参数类型错误）——size 折为字节整数；int 的受理字面即
// MySQL 字面。
func mysqlNativeValue(k enginetemplate.Knob, value string) (string, error) {
	if k.Kind != enginetemplate.KnobSize {
		return value, nil
	}
	n, err := enginetemplate.KnobSizeBytes(value)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(n, 10), nil
}
