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

func (redisTemplate) Engine() string     { return "redis" }
func (redisTemplate) Meta() Info         { return Info{Version: "7.4", Port: 6379} }
func (redisTemplate) Image() string      { return "redis:7.4" }
func (redisTemplate) DataTarget() string { return "/data" }

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

func (redisTemplate) Restore(host, password string) (RestoreSpec, error) {
	if err := validatePassword(password); err != nil {
		return RestoreSpec{}, err
	}
	// RDB 仅启动时装载：恢复 = 预置卷（工具容器把 stdin 落为卷内
	// dump.rdb，库首启装载；ADR-0039 决策 5）。host/password 不进 argv——
	// 预置发生在库存在之前；镜像自带 coreutils（debian 基座）。
	return RestoreSpec{
		Mode: RestorePreseed,
		Argv: []string{"cp", "/dev/stdin", SeedMountPoint + "/dump.rdb"},
	}, nil
}

func (redisTemplate) ImageDigest() string { return "" } // F2.7 空槽
