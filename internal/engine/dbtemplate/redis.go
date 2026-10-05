package dbtemplate

// redis 模板：requirepass 不支持密码文件——平台合成 redis.conf 经材料
// 通道注入（argv 只含配置路径，不落明文，ADR-0014 材料纪律）。

import (
	"fmt"
	"net/url"
)

// RedisConfFile 是 redis 凭证材料文件名（容器内 /run/secrets/<名>；平台
// 合成的 requirepass + appendonly 配置）。
const RedisConfFile = "database-redis-conf"

// redisTemplate 是 redis 引擎的模板 adapter。
type redisTemplate struct{}

// redisImageTag / redisImageDigest 是 digest 钉定对（F2.7/ADR-0045；bump
// 纪律同 postgresImageTag 注）。digest 取 2026-10-05 Docker Hub index
// digest（multi-arch 真源）。
const (
	redisImageTag    = "redis:7.4"
	redisImageDigest = "sha256:c6eabf748fc7a61dbb5a705c78bcf3d6377b1127a97d0ce965c11c44ba46896f"
)

func (redisTemplate) Engine() string      { return "redis" }
func (redisTemplate) Meta() Info          { return Info{Version: "7.4", Port: 6379} }
func (redisTemplate) Image() string       { return pinnedRef(redisImageTag, redisImageDigest) }
func (redisTemplate) ImageDigest() string { return redisImageDigest }
func (redisTemplate) DataTarget() string  { return "/data" }

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

func (redisTemplate) Backup(host, password string) (BackupSpec, error) {
	if err := validatePassword(password); err != nil {
		return BackupSpec{}, err
	}
	// --rdb - ：服务端全量 RDB 流向 stdout（BGSAVE 语义，不阻塞服务）。
	// REDISCLI_AUTH 是材料纪律唯一登记例外（ADR-0039 决策 5）：redis-cli
	// 无文件面；一次性平台工具容器内的 env，无持久面。
	return BackupSpec{
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
const redisRestoreScript = "cp " + BackupInputPath + " " + SeedMountPoint + "/dump.rdb; " +
	"redis-server --dir " + SeedMountPoint + " --port 6399 --daemonize no --appendonly no & " +
	"i=0; until redis-cli -p 6399 ping >/dev/null 2>&1; do i=$((i+1)); [ $i -gt 300 ] && exit 1; sleep 0.1; done; " +
	"redis-cli -p 6399 BGREWRITEAOF; " +
	"i=0; until [ -f " + SeedMountPoint + "/appendonlydir/appendonly.aof.manifest ]; do i=$((i+1)); [ $i -gt 300 ] && exit 1; sleep 0.1; done; " +
	"redis-cli -p 6399 shutdown nosave; rm -f " + SeedMountPoint + "/dump.rdb"

func (redisTemplate) Restore(host, password string) (RestoreSpec, error) {
	if err := validatePassword(password); err != nil {
		return RestoreSpec{}, err
	}
	// RDB 仅启动时装载且 AOF 优先生效：恢复 = 预置卷 + 容器侧 RDB→AOF
	// 转写（redisRestoreScript；ADR-0039 决策 5 与落地实录）。host/password
	// 不进 argv——预置发生在库存在之前。
	return RestoreSpec{
		Mode: RestorePreseed,
		Argv: []string{"sh", "-c", redisRestoreScript},
	}, nil
}
