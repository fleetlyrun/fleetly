package databasetemplate

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

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
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

var mysqlVersions = []dataservicetemplate.VersionSpec{
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
	dataservicetemplate.ToolUserDefault // 工具面缺省：镜像自带用户（官方镜像 root——B8-fix 零行为变化）
	version                             dataservicetemplate.VersionSpec
}

// mysqlKnobs 是 mysql 首批旋钮（B2b，design §3.4）——全 dynamic：MySQL
// `SET PERSIST` 自持久（mysqld-auto.cnf 住数据目录，8.4/9 全系支持、重启
// 自愈），innodb_buffer_pool_size 支持在线调。声明面先行（ApplySpec 随
// B2d）；static 渲染子集恒空（喂入律下 Materials 不消费 dynamic 键）。
var mysqlKnobs = []dataservicetemplate.Knob{
	{Key: "max_connections", Kind: dataservicetemplate.KnobInt, Default: "151",
		Class: dataservicetemplate.KnobDynamic, Category: "connections"},
	{Key: "innodb_buffer_pool_size", Kind: dataservicetemplate.KnobSize, Default: "128mb",
		Class: dataservicetemplate.KnobDynamic, Category: "memory"},
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
func (mysqlTemplate) Knobs() []dataservicetemplate.Knob {
	out := make([]dataservicetemplate.Knob, len(mysqlKnobs))
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
func (mysqlTemplate) Versions() []dataservicetemplate.VersionSpec {
	out := make([]dataservicetemplate.VersionSpec, len(mysqlVersions))
	copy(out, mysqlVersions)
	return out
}
func (mysqlTemplate) bindVersion(spec dataservicetemplate.VersionSpec) Template {
	return mysqlTemplate{version: spec}
}
func (t mysqlTemplate) Meta() Info { return Info{Version: t.version.ID, Port: 3306} }
func (t mysqlTemplate) Image() string {
	return pinnedRef(mysqlRepo+":"+t.version.Tag, t.version.Digest)
}
func (t mysqlTemplate) ImageDigest() string { return t.version.Digest }
func (mysqlTemplate) DataTarget() string    { return "/var/lib/mysql" }

func (mysqlTemplate) Workload(_ map[string]string) (map[string]string, []string) {
	// datadir 显式子目录（staging 实咬 2026-11-07 记录·六十二）：卷根挂
	// /var/lib/mysql 且实例标记 fleetly-instance.json 落卷根（ADR-0097 标记
	// 契约），而 mysql initdb 对数据目录「有任何文件即拒」（MY-010457）——
	// pg 走 pgdata 子目录、mongo 容忍根文件，唯独 mysql 严格。修法=入口
	// 脚本带 --datadir server arg（入口从 args 探测 datadir 并在该子目录
	// mkdir+chown+初始化），卷根=标记+data/ 子目录两不相扰。command 是
	// entrypoint 覆盖位（swarm ContainerSpec.Command——postgres B2b 同款
	// 先例）：官方入口 docker-entrypoint.sh 必须显式带头，否则 runc 把
	// --datadir 当可执行文件（staging 第二咬）。
	return map[string]string{
		"MYSQL_DATABASE":           myDBName,
		"MYSQL_USER":               myUser,
		"MYSQL_PASSWORD_FILE":      "/run/secrets/" + MySQLPasswordFile,
		"MYSQL_ROOT_PASSWORD_FILE": "/run/secrets/" + MySQLRootPasswordFile,
	}, []string{"docker-entrypoint.sh", "--datadir=/var/lib/mysql/data"}
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

// myAdminMaterials 渲染 root 凭证 [client] INI（dynamic 热应用与轮换等
// 平台管理动作专用——SET PERSIST 权限面见 ApplyDynamicSettings 注释）。
func myAdminMaterials(host, password string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	return map[string][]byte{myBackupDefaultsFile: []byte(
		"[client]\nhost=" + host + "\nuser=root\npassword=" + password + "\n")}, nil
}

func (mysqlTemplate) Backup(host, password string) (dataservicetemplate.BackupSpec, error) {
	files, err := myBackupMaterials(host, password)
	if err != nil {
		return dataservicetemplate.BackupSpec{}, err
	}
	// --single-transaction：InnoDB 一致快照不锁表；SQL 文本 dump 走 stdout。
	return dataservicetemplate.BackupSpec{
		Argv: []string{"mysqldump", "--defaults-extra-file=/run/secrets/" + myBackupDefaultsFile,
			"--single-transaction", "--routines", "--triggers", "--events", myDBName},
		SecretFiles: files,
	}, nil
}

func (mysqlTemplate) Restore(host, password string) (dataservicetemplate.RestoreSpec, error) {
	files, err := myBackupMaterials(host, password)
	if err != nil {
		return dataservicetemplate.RestoreSpec{}, err
	}
	// 目标库由模板 env 面（MYSQL_DATABASE）首启建好；SQL 流经文件挂载
	//（source 客户端命令读文件——hijack stdin EOF 不可达，ADR-0039 实录）。
	return dataservicetemplate.RestoreSpec{
		Mode:        dataservicetemplate.RestoreStream,
		Argv:        []string{"mysql", "--defaults-extra-file=/run/secrets/" + myBackupDefaultsFile, "-e", "source " + dataservicetemplate.BackupInputPath, myDBName},
		SecretFiles: files,
	}, nil
}

// RotatePassword 渲染数据面改密（IA v3 二期⑤b）：ALTER USER 平台账号
// （首启 entrypoint 铸的是 'fleetly'@'%'）；认证走 defaults 文件（current）。
// IDENTIFIED BY 单引号插值的转义安全由 validatePassword 字符集闸承担。
// MYSQL_PASSWORD_FILE 只在空卷首启建号生效，改密必须走数据面。
func (mysqlTemplate) RotatePassword(host, current, next string) (dataservicetemplate.RotateSpec, error) {
	if err := validatePassword(current); err != nil {
		return dataservicetemplate.RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return dataservicetemplate.RotateSpec{}, err
	}
	files, err := myBackupMaterials(host, current)
	if err != nil {
		return dataservicetemplate.RotateSpec{}, err
	}
	return dataservicetemplate.RotateSpec{
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
//
// 凭证走 root（staging 实咬 2026-11-08 记录·六十二：SET PERSIST 需
// SUPER/SYSTEM_VARIABLES_ADMIN，app 账号 ERROR 1227 拒绝——平台管理
// 动作以 root 执行；root 密码=app 密码同值双文件是单真源最小诚实形态
// 〔见包注〕，INI 换 user=root 即可，host/password 面零变化）。
func (mysqlTemplate) ApplyDynamicSettings(host, password string, settings map[string]string) (dataservicetemplate.ApplySpec, error) {
	if err := validatePassword(password); err != nil {
		return dataservicetemplate.ApplySpec{}, err
	}
	keys, dyn, err := dataservicetemplate.ValidateDynamicSettings(mysqlKnobs, settings)
	if err != nil {
		return dataservicetemplate.ApplySpec{}, err
	}
	if len(keys) == 0 {
		return dataservicetemplate.ApplySpec{}, dataservicetemplate.ErrNoDynamicFace
	}
	files, err := myAdminMaterials(host, password)
	if err != nil {
		return dataservicetemplate.ApplySpec{}, err
	}
	byKey := make(map[string]dataservicetemplate.Knob, len(mysqlKnobs))
	for _, k := range mysqlKnobs {
		byKey[k.Key] = k
	}
	stmts := make([]string, 0, len(keys))
	for _, k := range keys {
		v, err := mysqlNativeValue(byKey[k], dyn[k])
		if err != nil {
			return dataservicetemplate.ApplySpec{}, err
		}
		stmts = append(stmts, "SET PERSIST "+k+" = "+v)
	}
	return dataservicetemplate.ApplySpec{
		Argv: []string{"mysql", "--defaults-extra-file=/run/secrets/" + myBackupDefaultsFile,
			"-e", strings.Join(stmts, "; ")},
		SecretFiles: files,
	}, nil
}

// mysqlNativeValue 把受理口径的旋钮值渲染为 MySQL SET 语句原生字面（B2d）：
// SET 语句不收单位后缀（K/M/G 只在 option file 与命令行合法——"128mb"
// 在 SET 右值即参数类型错误）——size 折为字节整数；int 的受理字面即
// MySQL 字面。
func mysqlNativeValue(k dataservicetemplate.Knob, value string) (string, error) {
	if k.Kind != dataservicetemplate.KnobSize {
		return value, nil
	}
	n, err := dataservicetemplate.KnobSizeBytes(value)
	if err != nil {
		return "", err
	}
	return strconv.FormatInt(n, 10), nil
}
