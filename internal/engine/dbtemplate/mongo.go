package dbtemplate

// mongo 模板（F2.1 矩阵扩展）：官方镜像无 _FILE 密码变量——凭证纪律走
// 双材料形态：平台合成 mongod.conf（开 authorization）与 init 脚本（首启
// 建库建用户，密码嵌在脚本材料内——材料即密文通道，ADR-0014）。命令把
// init 脚本拷进 entrypoint 的初始化目录后转交官方入口（固定字面量，零
// 拼接、零变量）。探针用 mongosh ping——驱动拓扑探测面，授权前可用。

import (
	"encoding/json"
	"fmt"
	"net/url"
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

// mongoTemplate 是 mongo 引擎的模板 adapter。
type mongoTemplate struct{}

// mongoImageTag / mongoImageDigest 是 digest 钉定对（F2.7/ADR-0045；bump
// 纪律同 postgresImageTag 注）。digest 取 2026-10-05 Docker Hub index
// digest（multi-arch 真源）。
const (
	mongoImageTag    = "mongo:8.0"
	mongoImageDigest = "sha256:d0d926f94df099bff534b7ee5b5986458131a22489dfff8664509af0c1e2ca9c"
)

func (mongoTemplate) Engine() string      { return "mongo" }
func (mongoTemplate) Meta() Info          { return Info{Version: "8.0", Port: 27017} }
func (mongoTemplate) Image() string       { return pinnedRef(mongoImageTag, mongoImageDigest) }
func (mongoTemplate) ImageDigest() string { return mongoImageDigest }
func (mongoTemplate) DataTarget() string  { return "/data/db" }

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

func (mongoTemplate) Materials(password string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
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

func (mongoTemplate) Backup(host, password string) (BackupSpec, error) {
	files, err := moBackupMaterials(host, password)
	if err != nil {
		return BackupSpec{}, err
	}
	// --archive --gzip：单流 BSON 归档走 stdout（--db 限业务库）。
	return BackupSpec{
		Argv: []string{"mongodump", "--config=/run/secrets/" + moBackupConfigFile,
			"--archive", "--gzip", "--db", moDBName},
		SecretFiles: files,
	}, nil
}

func (mongoTemplate) Restore(host, password string) (RestoreSpec, error) {
	files, err := moBackupMaterials(host, password)
	if err != nil {
		return RestoreSpec{}, err
	}
	// 归档经文件挂载（--archive 带路径实参——hijack stdin EOF 不可达，
	// ADR-0039 实录）；目标库同名（ns 映射恒等，模板库名冻结）。
	return RestoreSpec{
		Mode:        RestoreStream,
		Argv:        []string{"mongorestore", "--config=/run/secrets/" + moBackupConfigFile, "--archive=" + BackupInputPath, "--gzip"},
		SecretFiles: files,
	}, nil
}

// RotatePassword 渲染数据面改密（IA v3 二期⑤b）：changeUserPassword 改
// 平台账号（用户活在业务库数据内——init 脚本只在空卷首启执行一次，改密
// 必须走数据面）；认证走 --config URI（current）。--eval 单引号插值的转
// 义安全由 validatePassword 字符集闸承担（与 init 脚本同款纵深口径）。
// 载体重下发后新 init 脚本材料（next）落位——未来空卷重建与单真源自洽。
func (mongoTemplate) RotatePassword(host, current, next string) (RotateSpec, error) {
	if err := validatePassword(current); err != nil {
		return RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return RotateSpec{}, err
	}
	files, err := moBackupMaterials(host, current)
	if err != nil {
		return RotateSpec{}, err
	}
	return RotateSpec{
		Argv: []string{"mongosh", "--config=/run/secrets/" + moBackupConfigFile, "--quiet",
			"--eval", "db.getSiblingDB('" + moDBName + "').changeUserPassword('" + moUser + "', '" + next + "')"},
		SecretFiles: files,
	}, nil
}
