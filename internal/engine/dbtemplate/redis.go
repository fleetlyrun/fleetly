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

func (redisTemplate) Materials(password string) map[string][]byte {
	// appendonly：容器重启不丢已确认写（数据卷已在；AOF 是库级持久语义
	// 的最小诚实形态）。
	return map[string][]byte{
		RedisConfFile: []byte("requirepass " + password + "\nappendonly yes\n"),
	}
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

func (redisTemplate) BackupCommand() string { return "" } // F2.2 空槽
func (redisTemplate) ImageDigest() string   { return "" } // F2.7 空槽
