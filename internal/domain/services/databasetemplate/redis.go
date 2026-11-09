package databasetemplate

// redis 模板：requirepass 不支持密码文件——平台合成 redis.conf 经材料
// 通道注入（argv 只含配置路径，不落明文，ADR-0014 材料纪律）。B2a 版本化
//（ADR-0094 决策 7）：在册 7.4/8.2、8.2 Default（8.0 于 2026-12-01 EOL、
// 7.2 临 EOL 均不入册）；跨版本零差异（版本分叉仅镜像面）。

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// RedisConfFile 是 redis 凭证材料文件名（容器内 /run/secrets/<名>；平台
// 合成的 requirepass + appendonly 配置）。
const RedisConfFile = "database-redis-conf"

// redisRepo 是 redis 引擎的镜像仓库名；redisVersions 是在册版本矩阵
// （digest = 2026-11-05 Docker Hub index 实测，scripts/digests.sh 对账）。
const redisRepo = "redis"

var redisVersions = []dataservicetemplate.VersionSpec{
	{
		ID: "7.4", Tag: "7.4",
		Digest: "sha256:2e3eb11462c4e3759e6972678dd051d150ff54b1e60714b3c33b701260172a90",
	},
	{
		ID: "8.2", Tag: "8.2",
		Digest:  "sha256:b0b01167ff47bc3868e56a967895a26be4230891a1f42a78ab61cdd227accc8d",
		Default: true,
	},
}

// redisTemplate 是 redis 引擎的模板 adapter（绑版实例持 version；注册表
// 原型零值仅承载目录面）。
type redisTemplate struct {
	dataservicetemplate.ToolUserDefault // 工具面缺省：镜像自带用户（官方镜像 root——B8-fix 零行为变化）
	version                             dataservicetemplate.VersionSpec
}

// redisKnobs 是 redis 首批旋钮（B2b，design §3.4）——全 static：redis 无
// 自持久面（CONFIG SET 重启即丢），ADR-0095 决策 4 首批全滚动取舍（卷内
// user.conf include + CONFIG REWRITE 模式挂账后手）。appendonly 现 B2b 前
// 硬编码 yes → 旋钮化（Default "true" 渲染 yes，空 settings 与现状逐字节
// 一致）。maxmemory 无缺省建模（引擎原生缺省 0 = 无限制，size Kind 不表
// 达——目录回显留空即"未设"）。
var redisKnobs = []dataservicetemplate.Knob{
	{Key: "appendonly", Kind: dataservicetemplate.KnobBool, Default: "true",
		Class: dataservicetemplate.KnobStatic, Category: "persistence"},
	{Key: "maxmemory", Kind: dataservicetemplate.KnobSize,
		Class: dataservicetemplate.KnobStatic, Category: "memory"},
	{Key: "maxmemory-policy", Kind: dataservicetemplate.KnobEnum, Default: "noeviction",
		Enum: []string{
			"noeviction", "allkeys-lru", "allkeys-lfu", "volatile-lru",
			"volatile-lfu", "volatile-ttl", "volatile-random", "allkeys-random",
		},
		Class: dataservicetemplate.KnobStatic, Category: "memory"},
}

// redisOwnedKeys 是 redis 的平台 owned 键闭集（ADR-0095 决策 2；design
// §3.1 红队 F-04 补全版，redis.conf 原生指令词形）。清单即守卫对照集
// （TestPlatformOwnedKeysUnreachable）：requirepass 是平台合成 conf 的
// 平台侧单源（轮换走 Secret 重写）；dir 决定 RDB/AOF 落卷面（卷布局 =
// 平台所有权）。
var redisOwnedKeys = []string{
	// 认证（requirepass = 平台合成 conf 材料；masterauth = 复制认证面）
	"requirepass", "masterauth",
	// 监听与通告
	"bind", "port", "protected-mode",
	// 数据目录（dir/dbfilename/appenddirname 决定持久化文件落位）
	"dir", "dbfilename", "appenddirname",
	// 复制与集群
	"replicaof", "slaveof", "cluster-enabled", "cluster-config-file",
	// 模块与 shell 执行面
	"loadmodule", "rename-command",
	// 文件包含与身份
	"include", "aclfile",
}

// Knobs 返回 redis 旋钮白名单（版本键控面：绑版实例可分叉——本批各版本
// 同集；副本防调用方改注册表）。
func (redisTemplate) Knobs() []dataservicetemplate.Knob {
	out := make([]dataservicetemplate.Knob, len(redisKnobs))
	copy(out, redisKnobs)
	return out
}

// OwnedKeys 返回 redis 平台 owned 键闭集。
func (redisTemplate) OwnedKeys() []string {
	out := make([]string, len(redisOwnedKeys))
	copy(out, redisOwnedKeys)
	return out
}

func (redisTemplate) Engine() string { return "redis" }
func (redisTemplate) Versions() []dataservicetemplate.VersionSpec {
	out := make([]dataservicetemplate.VersionSpec, len(redisVersions))
	copy(out, redisVersions)
	return out
}
func (redisTemplate) bindVersion(spec dataservicetemplate.VersionSpec) Template {
	return redisTemplate{version: spec}
}
func (t redisTemplate) Meta() Info { return Info{Version: t.version.ID, Port: 6379} }
func (t redisTemplate) Image() string {
	return pinnedRef(redisRepo+":"+t.version.Tag, t.version.Digest)
}
func (t redisTemplate) ImageDigest() string { return t.version.Digest }
func (redisTemplate) DataTarget() string    { return "/data" }

// Workload 渲染 env/command（static 旋钮不进命令面——redis 的 static 车道
// 走 Materials 合成 conf〔B2b 已就绪，B2d 起由 databaseMaterials 喂入〕；
// 喂入律：staticSettings 只消费 static 旋钮，本引擎无 dynamic 旋钮故无
// 泄漏面，签名带参保持接口齐形）。
func (redisTemplate) Workload(_ map[string]string) (map[string]string, []string) {
	return nil, []string{"redis-server", "/run/secrets/" + RedisConfFile}
}

func (redisTemplate) Probe() []string {
	// 探针不带凭证：NOAUTH 错误回复同样证明服务在服（连接失败才非零）。
	return []string{"redis-cli", "-p", "6379", "ping"}
}

func (redisTemplate) Materials(password string, staticSettings map[string]string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	// 喂入律纵深：只消费本引擎 static 旋钮（dynamic/未知键结构性丢弃），
	// 值过渲染安全闸（fail-closed）。
	keys, static, err := staticKnobSettings(redisKnobs, staticSettings)
	if err != nil {
		return nil, err
	}
	var conf strings.Builder
	conf.WriteString("requirepass " + password + "\n")
	// appendonly 旋钮化（B2b，design §3.4）：平台冻结的库级持久语义缺省
	// = 旋钮 Default "true" → yes——容器重启不丢已确认写（数据卷已在；
	// AOF 是库级持久语义的最小诚实形态，B2b 前硬编码 yes 的旋钮化形态）。
	// 恒渲染行：空 settings 与 B2b 前现状逐字节一致。
	appendonly := "true"
	if v, ok := static["appendonly"]; ok {
		appendonly = v
	}
	conf.WriteString("appendonly " + redisBoolWord(appendonly) + "\n")
	// 其余 static 旋钮按排序键序增行（redis.conf 行语法——值已被渲染安全
	// 闸约束在无空白字符集）。
	for _, k := range keys {
		if k == "appendonly" {
			continue
		}
		conf.WriteString(k + " " + static[k] + "\n")
	}
	return map[string][]byte{RedisConfFile: []byte(conf.String())}, nil
}

// redisBoolWord 把用户面 bool 值（true/false）翻成 redis.conf 原生词
// （yes/no——键与值都走引擎原生词面，零翻译层是键面；值面两词形是渲染
// 面的方言收敛）。
func redisBoolWord(v string) string {
	if v == "false" {
		return "no"
	}
	return "yes"
}

func (redisTemplate) ConnURL(host, password string) string {
	u := &url.URL{
		Scheme: "redis",
		User:   url.UserPassword("", password),
		Host:   fmt.Sprintf("%s:%d", host, 6379),
		Path:   "0",
	}
	return u.String()
}

func (redisTemplate) Backup(host, password string) (dataservicetemplate.BackupSpec, error) {
	if err := validatePassword(password); err != nil {
		return dataservicetemplate.BackupSpec{}, err
	}
	// --rdb - ：服务端全量 RDB 流向 stdout（BGSAVE 语义，不阻塞服务）。
	// REDISCLI_AUTH 是材料纪律唯一登记例外（ADR-0039 决策 5）：redis-cli
	// 无文件面；一次性平台工具容器内的 env，无持久面。
	return dataservicetemplate.BackupSpec{
		Argv: []string{"redis-cli", "-h", host, "-p", "6379", "--rdb", "-"},
		Env:  map[string]string{"REDISCLI_AUTH": password},
	}, nil
}

// redisRestoreScript 是预置卷恢复的容器侧编排（平台常量串——零用户数据
// 插值，mongo Workload 的 sh -c 同款先例）：RDB 落卷 → 临时 redis 以
// appendonly no 装载 RDB（redis 7 起 appendonly yes 且无 manifest 时对
// dump.rdb 视而不见——本地最小复现钉死）→ BGREWRITEAOF 把内存态铸成
// AOF manifest+base → 关停并清 RDB。有界等待环（起服/重写各 30s），
// 超时即非零退出（恢复失败诚实落账）。
const redisRestoreScript = "cp " + dataservicetemplate.BackupInputPath + " " + dataservicetemplate.SeedMountPoint + "/dump.rdb; " +
	"redis-server --dir " + dataservicetemplate.SeedMountPoint + " --port 6399 --daemonize no --appendonly no & " +
	"i=0; until redis-cli -p 6399 ping >/dev/null 2>&1; do i=$((i+1)); [ $i -gt 300 ] && exit 1; sleep 0.1; done; " +
	"redis-cli -p 6399 BGREWRITEAOF; " +
	"i=0; until [ -f " + dataservicetemplate.SeedMountPoint + "/appendonlydir/appendonly.aof.manifest ]; do i=$((i+1)); [ $i -gt 300 ] && exit 1; sleep 0.1; done; " +
	"redis-cli -p 6399 shutdown nosave; rm -f " + dataservicetemplate.SeedMountPoint + "/dump.rdb"

func (redisTemplate) Restore(host, password string) (dataservicetemplate.RestoreSpec, error) {
	if err := validatePassword(password); err != nil {
		return dataservicetemplate.RestoreSpec{}, err
	}
	// RDB 仅启动时装载且 AOF 优先生效：恢复 = 预置卷 + 容器侧 RDB→AOF
	// 转写（redisRestoreScript；ADR-0039 决策 5 与落地实录）。host/password
	// 不进 argv——预置发生在库存在之前。
	return dataservicetemplate.RestoreSpec{
		Mode: dataservicetemplate.RestorePreseed,
		Argv: []string{"sh", "-c", redisRestoreScript},
	}, nil
}

// RotatePassword 声明式轮换（IA v3 二期⑤b）：requirepass 在平台合成 conf
// 材料内且 argv 每启重读——Secret 重写 + 收敛环重下发载体即生效，无
// utility（Argv 空；旧值在新载体上线前保持有效，无破窗）。current/next
// 仍过字符集闸：next 即将进新 conf 材料，闸是渲染面单一防线。
func (redisTemplate) RotatePassword(host, current, next string) (dataservicetemplate.RotateSpec, error) {
	if err := validatePassword(current); err != nil {
		return dataservicetemplate.RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return dataservicetemplate.RotateSpec{}, err
	}
	return dataservicetemplate.RotateSpec{}, nil
}

// ApplyDynamicSettings：redis 无 dynamic 面（ADR-0095 决策 4 首批取舍——
// CONFIG SET 重启即丢、无自持久，全部旋钮声明 static 走滚动；卷内
// user.conf include + CONFIG REWRITE 模式挂账后手）。非空 settings 即
// 无 dynamic 面哨兵（正常分流不可达：ValidateSettings 在受理位已把白名单
// 收敛到全 static 集，SplitSettings 的 dynamic 子集恒空——本面是防御态）。
func (redisTemplate) ApplyDynamicSettings(_, _ string, _ map[string]string) (dataservicetemplate.ApplySpec, error) {
	return dataservicetemplate.ApplySpec{}, dataservicetemplate.ErrNoDynamicFace
}
