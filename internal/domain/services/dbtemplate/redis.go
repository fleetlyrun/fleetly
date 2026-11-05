package dbtemplate

// redis 模板：requirepass 不支持密码文件——平台合成 redis.conf 经材料
// 通道注入（argv 只含配置路径，不落明文，ADR-0014 材料纪律）。B2a 版本化
//（ADR-0094 决策 7）：在册 7.4/8.2、8.2 Default（8.0 于 2026-12-01 EOL、
// 7.2 临 EOL 均不入册）；跨版本零差异（版本分叉仅镜像面）。

import (
	"fmt"
	"net/url"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// RedisConfFile 是 redis 凭证材料文件名（容器内 /run/secrets/<名>；平台
// 合成的 requirepass + appendonly 配置）。
const RedisConfFile = "database-redis-conf"

// redisRepo 是 redis 引擎的镜像仓库名；redisVersions 是在册版本矩阵
// （digest = 2026-11-05 Docker Hub index 实测，scripts/digests.sh 对账）。
const redisRepo = "redis"

var redisVersions = []enginetemplate.VersionSpec{
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
	version enginetemplate.VersionSpec
}

func (redisTemplate) Engine() string { return "redis" }
func (redisTemplate) Versions() []enginetemplate.VersionSpec {
	out := make([]enginetemplate.VersionSpec, len(redisVersions))
	copy(out, redisVersions)
	return out
}
func (redisTemplate) bindVersion(spec enginetemplate.VersionSpec) Template {
	return redisTemplate{version: spec}
}
func (t redisTemplate) Meta() Info { return Info{Version: t.version.ID, Port: 6379} }
func (t redisTemplate) Image() string {
	return pinnedRef(redisRepo+":"+t.version.Tag, t.version.Digest)
}
func (t redisTemplate) ImageDigest() string { return t.version.Digest }
func (redisTemplate) DataTarget() string    { return "/data" }

func (redisTemplate) Workload() (map[string]string, []string) {
	return nil, []string{"redis-server", "/run/secrets/" + RedisConfFile}
}

func (redisTemplate) Probe() []string {
	// 探针不带凭证：NOAUTH 错误回复同样证明服务在服（连接失败才非零）。
	return []string{"redis-cli", "-p", "6379", "ping"}
}

func (redisTemplate) Materials(password string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	// appendonly：容器重启不丢已确认写（数据卷已在；AOF 是库级持久语义
	// 的最小诚实形态）。
	return map[string][]byte{
		RedisConfFile: []byte("requirepass " + password + "\nappendonly yes\n"),
	}, nil
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

func (redisTemplate) Backup(host, password string) (enginetemplate.BackupSpec, error) {
	if err := validatePassword(password); err != nil {
		return enginetemplate.BackupSpec{}, err
	}
	// --rdb - ：服务端全量 RDB 流向 stdout（BGSAVE 语义，不阻塞服务）。
	// REDISCLI_AUTH 是材料纪律唯一登记例外（ADR-0039 决策 5）：redis-cli
	// 无文件面；一次性平台工具容器内的 env，无持久面。
	return enginetemplate.BackupSpec{
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
const redisRestoreScript = "cp " + enginetemplate.BackupInputPath + " " + enginetemplate.SeedMountPoint + "/dump.rdb; " +
	"redis-server --dir " + enginetemplate.SeedMountPoint + " --port 6399 --daemonize no --appendonly no & " +
	"i=0; until redis-cli -p 6399 ping >/dev/null 2>&1; do i=$((i+1)); [ $i -gt 300 ] && exit 1; sleep 0.1; done; " +
	"redis-cli -p 6399 BGREWRITEAOF; " +
	"i=0; until [ -f " + enginetemplate.SeedMountPoint + "/appendonlydir/appendonly.aof.manifest ]; do i=$((i+1)); [ $i -gt 300 ] && exit 1; sleep 0.1; done; " +
	"redis-cli -p 6399 shutdown nosave; rm -f " + enginetemplate.SeedMountPoint + "/dump.rdb"

func (redisTemplate) Restore(host, password string) (enginetemplate.RestoreSpec, error) {
	if err := validatePassword(password); err != nil {
		return enginetemplate.RestoreSpec{}, err
	}
	// RDB 仅启动时装载且 AOF 优先生效：恢复 = 预置卷 + 容器侧 RDB→AOF
	// 转写（redisRestoreScript；ADR-0039 决策 5 与落地实录）。host/password
	// 不进 argv——预置发生在库存在之前。
	return enginetemplate.RestoreSpec{
		Mode: enginetemplate.RestorePreseed,
		Argv: []string{"sh", "-c", redisRestoreScript},
	}, nil
}

// RotatePassword 声明式轮换（IA v3 二期⑤b）：requirepass 在平台合成 conf
// 材料内且 argv 每启重读——Secret 重写 + 收敛环重下发载体即生效，无
// utility（Argv 空；旧值在新载体上线前保持有效，无破窗）。current/next
// 仍过字符集闸：next 即将进新 conf 材料，闸是渲染面单一防线。
func (redisTemplate) RotatePassword(host, current, next string) (enginetemplate.RotateSpec, error) {
	if err := validatePassword(current); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	return enginetemplate.RotateSpec{}, nil
}
