package dbtemplate

// mongo 模板（F2.1 矩阵扩展；B2a 版本化，ADR-0094 决策 7）：在册 8.0/8.2、
// 8.2 Default（7.0 不入册——无兼容义务，收敛在册面）。官方镜像无 _FILE
// 密码变量——凭证纪律走双材料形态：平台合成 mongod.conf（开 authorization）
// 与 init 脚本（首启建库建用户，密码嵌在脚本材料内——材料即密文通道，
// ADR-0014）。命令把 init 脚本拷进 entrypoint 的初始化目录后转交官方入口
//（固定字面量，零拼接、零变量）。探针用 mongosh ping——驱动拓扑探测面，
// 授权前可用。跨版本零差异（版本分叉仅镜像面）。

import (
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// mongo 凭证材料文件名（容器内 /run/secrets/<名>）。
const (
	MongoConfFile    = "database-mongo-conf"
	MongoInitJSFile  = "database-mongo-init"
	mongoInitDropDir = "/docker-entrypoint-initdb.d"
)

// mongoUser / mongoDBName 是 mongo 的连接账号与库名（模板冻结值；用户建
// 在业务库内——连接串 authSource 缺省即路径库名，零参数自洽）。
const (
	moUser   = "fleetly"
	moDBName = "fleetly"
)

// mongoRepo 是 mongo 引擎的镜像仓库名；mongoVersions 是在册版本矩阵
// （digest = 2026-11-05 Docker Hub index 实测，scripts/digests.sh 对账）。
const mongoRepo = "mongo"

var mongoVersions = []enginetemplate.VersionSpec{
	{
		ID: "8.0", Tag: "8.0",
		Digest: "sha256:b9924c330a8bbc5800fe9c69f04a84e221eca7b28fd049a0354147d9908fa142",
	},
	{
		ID: "8.2", Tag: "8.2",
		Digest:  "sha256:e0ce8c35124d4a9f9785532d1f268f39e9728ffa1cb38f46fa482436424c4bd3",
		Default: true,
	},
}

// mongoTemplate 是 mongo 引擎的模板 adapter（绑版实例持 version；注册表
// 原型零值仅承载目录面）。
type mongoTemplate struct {
	version enginetemplate.VersionSpec
}

// mongoKnobs：mongo 首批零旋钮（design §3.4——setParameter 面窄）。
// Knobs 返回 nil（白名单空集 = 任何 settings 键都被受理位拒）。

// mongoOwnedKeys 是 mongo 的平台 owned 键闭集（ADR-0095 决策 2；design
// §3.1 红队 F-04 补全版，mongod 旗标/YAML 词形）。清单即守卫对照集
// （TestPlatformOwnedKeysUnreachable）——首批零旋钮时交集真空，本面为
// 未来增旋钮的回流口守卫。authorization 是平台合成 conf 已设面
// （security.authorization: enabled）；config 指针本身属平台合成 conf。
var mongoOwnedKeys = []string{
	// 认证（authorization = 平台合成 conf 冻结面；keyFile = 集群认证身份；
	// authenticationMechanisms = setParameter 认证机制面）
	"authorization", "keyFile", "authenticationMechanisms",
	// 监听与通告
	"bind_ip", "port",
	// 数据目录
	"dbpath",
	// 复制与集群
	"replSetName",
	// 文件包含与身份（config 指针 = 平台合成 conf 的所有权）
	"config",
}

// Knobs 返回 mongo 旋钮白名单（首批空集）。
func (mongoTemplate) Knobs() []enginetemplate.Knob { return nil }

// OwnedKeys 返回 mongo 平台 owned 键闭集。
func (mongoTemplate) OwnedKeys() []string {
	out := make([]string, len(mongoOwnedKeys))
	copy(out, mongoOwnedKeys)
	return out
}

func (mongoTemplate) Engine() string { return "mongo" }
func (mongoTemplate) Versions() []enginetemplate.VersionSpec {
	out := make([]enginetemplate.VersionSpec, len(mongoVersions))
	copy(out, mongoVersions)
	return out
}
func (mongoTemplate) bindVersion(spec enginetemplate.VersionSpec) Template {
	return mongoTemplate{version: spec}
}
func (t mongoTemplate) Meta() Info { return Info{Version: t.version.ID, Port: 27017} }
func (t mongoTemplate) Image() string {
	return pinnedRef(mongoRepo+":"+t.version.Tag, t.version.Digest)
}
func (t mongoTemplate) ImageDigest() string { return t.version.Digest }
func (mongoTemplate) DataTarget() string    { return "/data/db" }

func (mongoTemplate) Workload() (map[string]string, []string) {
	return nil, []string{"sh", "-c",
		"cp /run/secrets/" + MongoInitJSFile + " " + mongoInitDropDir + "/10-fleetly-user.js" +
			" && exec docker-entrypoint.sh mongod --config /run/secrets/" + MongoConfFile}
}

func (mongoTemplate) Probe() []string {
	// ping 是驱动拓扑探测面命令：授权开启下也无须认证（hello/ping 家族
	// 的预认证豁免）——不带凭证即证明服务在服。
	return []string{"mongosh", "--quiet", "--eval", "db.adminCommand('ping')"}
}

// Materials 渲染凭证材料 + static 旋钮折叠面（B2b，ADR-0095 决策 4）：
// 零旋钮引擎——static 子集恒空（staticKnobSettings 对 nil knob 表全
// 丢弃），输出不随 settings 变。
func (mongoTemplate) Materials(password string, staticSettings map[string]string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	if _, _, err := staticKnobSettings(nil, staticSettings); err != nil {
		return nil, err
	}
	conf := "storage:\n  dbPath: /data/db\nnet:\n  port: 27017\nsecurity:\n  authorization: enabled\n"
	// 首启初始化脚本（空数据卷时 entrypoint 以本地无认证模式执行一次）：
	// 建业务库与账号。密码经渲染面字符集闸（单引号串不可转义，纵深防御）。
	initJS := "db.getSiblingDB('" + moDBName + "').createUser({user: '" + moUser +
		"', pwd: '" + password + "', roles: [{role: 'readWrite', db: '" + moDBName + "'}]});\n"
	return map[string][]byte{
		MongoConfFile:   []byte(conf),
		MongoInitJSFile: []byte(initJS),
	}, nil
}

func (mongoTemplate) ConnURL(host, password string) string {
	u := &url.URL{
		Scheme: "mongodb",
		User:   url.UserPassword(moUser, password),
		Host:   fmt.Sprintf("%s:%d", host, 27017),
		Path:   moDBName, // authSource 缺省 = 路径库，与 init 脚本建户位置自洽
	}
	return u.String()
}

// moBackupConfigFile 是备份/恢复工具的凭证材料文件名（database tools
// --config 的 JSON 形态——经 encoding/json Marshal 结构化生成，转义由
// 构造承担，零手拼文本）。
const moBackupConfigFile = "database-backup-config"

// moBackupMaterials 渲染 --config JSON（备份/恢复共用：{"uri": ConnURL}）。
func moBackupMaterials(host, password string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	cfg, err := json.Marshal(map[string]string{"uri": mongoTemplate{}.ConnURL(host, password)})
	if err != nil {
		return nil, fmt.Errorf("dbtemplate: render mongo tools config: %w", err)
	}
	return map[string][]byte{moBackupConfigFile: cfg}, nil
}

func (mongoTemplate) Backup(host, password string) (enginetemplate.BackupSpec, error) {
	files, err := moBackupMaterials(host, password)
	if err != nil {
		return enginetemplate.BackupSpec{}, err
	}
	// --archive --gzip：单流 BSON 归档走 stdout（--db 限业务库）。
	return enginetemplate.BackupSpec{
		Argv: []string{"mongodump", "--config=/run/secrets/" + moBackupConfigFile,
			"--archive", "--gzip", "--db", moDBName},
		SecretFiles: files,
	}, nil
}

func (mongoTemplate) Restore(host, password string) (enginetemplate.RestoreSpec, error) {
	files, err := moBackupMaterials(host, password)
	if err != nil {
		return enginetemplate.RestoreSpec{}, err
	}
	// 归档经文件挂载（--archive 带路径实参——hijack stdin EOF 不可达，
	// ADR-0039 实录）；目标库同名（ns 映射恒等，模板库名冻结）。
	return enginetemplate.RestoreSpec{
		Mode:        enginetemplate.RestoreStream,
		Argv:        []string{"mongorestore", "--config=/run/secrets/" + moBackupConfigFile, "--archive=" + enginetemplate.BackupInputPath, "--gzip"},
		SecretFiles: files,
	}, nil
}

// RotatePassword 渲染数据面改密（IA v3 二期⑤b）：changeUserPassword 改
// 平台账号（用户活在业务库数据内——init 脚本只在空卷首启执行一次，改密
// 必须走数据面）；认证走 --config URI（current）。--eval 单引号插值的转
// 义安全由 validatePassword 字符集闸承担（与 init 脚本同款纵深口径）。
// 载体重下发后新 init 脚本材料（next）落位——未来空卷重建与单真源自洽。
func (mongoTemplate) RotatePassword(host, current, next string) (enginetemplate.RotateSpec, error) {
	if err := validatePassword(current); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	files, err := moBackupMaterials(host, current)
	if err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	return enginetemplate.RotateSpec{
		Argv: []string{"mongosh", "--config=/run/secrets/" + moBackupConfigFile, "--quiet",
			"--eval", "db.getSiblingDB('" + moDBName + "').changeUserPassword('" + moUser + "', '" + next + "')"},
		SecretFiles: files,
	}, nil
}
