// Package naming 是 fleetly 平台对象命名与最小 label 集的唯一定义点
// （state-model §2.4 对象标记契约 + architecture §2.4 服务命名与网络行）。
//
// 纪律：
//   - 命名公式逐字对齐设计文档，任何字符串改动都意味着契约破坏（表驱动
//     测试对照文档原文钉死，naming_test.go）；
//   - 平台命名只存在于适配器——归一化 compose 不出现平台名（§2.4「平台
//     命名不进归一化 compose」）；调用方 = internal/substrate / 发布引擎；
//   - label 密钥/payload 永不进 label（state-model §2.4）。
package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// 平台命名公式（文档原文逐字；state-model §2.4 / architecture §2.4）：
//
//	Swarm 服务名   fleetly-<app>-<service>
//	Secret 名      fleetly-<app>-<name>-<hash8>   （hash8 = 内容 sha256 前 8）
//	卷名           fleetly-<app>-<key>-<appid8>   （appid8 = app ID 前 8）
//	网络名         fleetly-<app>-net              （每 app 专属 overlay；
//	                                                 文档未钉字符串，保守补全，
//	                                                 见包内注释与遗留记录）
const (
	// namePrefix 是全部平台对象名的公共前缀（防集群全局命名空间撞名）。
	namePrefix = "fleetly-"
)

// ServiceName 返回 Swarm 服务名 `fleetly-<app>-<service>`：两个 app 各有
// 同名服务（web/db）不冲突；服务别名 = compose 服务名（app 内短名互访与
// compose 语义一致，见 NetworkAlias）。
func ServiceName(app, service string) (string, error) {
	if err := validateComponent("app", app); err != nil {
		return "", err
	}
	if err := validateComponent("service", service); err != nil {
		return "", err
	}
	return joinName(app, service), nil
}

// SecretName 返回 Swarm secret 名 `fleetly-<app>-<name>-<hash8>`。hash8
// 由调用方经 Hash8(内容) 计算——**值轮换即换名换引用**（architecture §2.4
// 密钥行；desired-hash 以 secret 引用参与，轮换天然触发重部署）。
func SecretName(app, name, hash8 string) (string, error) {
	if err := validateComponent("app", app); err != nil {
		return "", err
	}
	if err := validateComponent("name", name); err != nil {
		return "", err
	}
	if err := validateHash8(hash8); err != nil {
		return "", err
	}
	return joinName(app, name, hash8), nil
}

// VolumeName 返回平台卷名 `fleetly-<app>-<key>-<appid8>`：卷无 label，
// 用命名约定承载归属（state-model §2.4；VolumeOptions.Labels 生效前的
// 收敛路径，stateful-placement §6）。appid8 = app 平台 ID 前 8 位。
func VolumeName(app, key, appID string) (string, error) {
	if err := validateComponent("app", app); err != nil {
		return "", err
	}
	if err := validateComponent("key", key); err != nil {
		return "", err
	}
	id8, err := AppID8(appID)
	if err != nil {
		return "", err
	}
	return joinName(app, key, id8), nil
}

// NetworkName 返回 app 专属 overlay 网络名（保守补全 `fleetly-<app>-net`；
// 文档钉死的是「每 app 专属 overlay 网络 + 服务别名 = compose 服务名」语义，
// 未钉网络名字符串——命名与前缀纪律保持一致，跨 app 网络隔离由专属网络
// 承载，architecture §2.4 服务命名与网络行）。
func NetworkName(app string) (string, error) {
	if err := validateComponent("app", app); err != nil {
		return "", err
	}
	return namePrefix + app + "-net", nil
}

// NetworkAlias 返回服务在 app 网络内的别名 = compose 服务名（app 内短名
// 互访与 compose 语义一致；跨 app 不可见——隔离由每 app 专属网络保证）。
func NetworkAlias(service string) (string, error) {
	if err := validateComponent("service", service); err != nil {
		return "", err
	}
	return service, nil
}

// cronJobNamePrefix 是一次性 cron job 服务名的固定前缀（E5 Cron）：完整名
// `fleetly-cron-<app>-<service>-<ulid8>`。前缀隔离了长驻服务的
// `fleetly-<app>-<service>` 命名空间——引擎对账（省略=删除的删除扫描）与
// 启动残留收口都以该前缀识别 job 服务的瞬时性（在途 job 不得被发布对账
// 误删；ulid8 使同 schedule 的重叠触发命名天然不冲突）。
const cronJobNamePrefix = namePrefix + "cron-"

// CronJobName 返回一次性 cron job 的 Swarm 服务名
// `fleetly-cron-<app>-<service>-<ulid8>`（ulid8 = 触发 run 的 ULID 前 8 位
// ——同 schedule 串行〔max-concurrent 1〕下的唯一性兜底）。
func CronJobName(app, service, runID string) (string, error) {
	if err := validateComponent("app", app); err != nil {
		return "", err
	}
	if err := validateComponent("service", service); err != nil {
		return "", err
	}
	if len(runID) < 8 {
		return "", fmt.Errorf("naming: run id %q shorter than 8 chars", runID)
	}
	return cronJobNamePrefix + app + "-" + service + "-" + runID[:8], nil
}

// IsCronJobName 报告 Swarm 服务名是否为一次性 cron job 服务（引擎对账的
// 删除扫描与残留收口按此前缀排除/识别瞬时 job 服务）。
func IsCronJobName(name string) bool {
	return strings.HasPrefix(name, cronJobNamePrefix)
}

// 库族命名公式（E4 数据库托管，managed-databases §5.4 文档锚逐字；新增
// 函数非改既有公式——与 app 名族 `fleetly-<app>-*` 解耦的独立前缀族，
// 库实例与 app 可重名、对象不撞，§2.1 名字空间独立）：
//
//	库服务名   fleetly-db-<name>-<service>
//	库网络名   fleetly-db-<name>-net           （每实例专属共享 overlay）
//	库卷名     fleetly-db-<name>-<key>-<id8>   （id8 = 实例 ID 前 8）
//	库 secret 名 fleetly-db-<name>-<secret>-<hash8>
//
// 前缀族常量 dbNamePrefix 同时是引擎对账的识别面（IsDbServiceName——库
// 服务不受发布对账「省略=删除」扫描管辖，后续阶段消费）。
const (
	// dbNamePrefix 是库族对象名的固定前缀（fleetly-db-）。
	dbNamePrefix = namePrefix + "db-"
)

// DBServiceName 返回库服务名 `fleetly-db-<name>-<service>`（name = 库实例
// 名，service = 模板服务名——模板渲染层供给，internal/dbtemplate）。
func DBServiceName(name, service string) (string, error) {
	if err := validateComponent("name", name); err != nil {
		return "", err
	}
	if err := validateComponent("service", service); err != nil {
		return "", err
	}
	return dbNamePrefix + name + "-" + service, nil
}

// DBNetworkName 返回库实例专属共享 overlay 网络名 `fleetly-db-<name>-net`
// （库服务挂该网络、别名 = 实例名；引用方服务部署时由平台附加挂载）。
func DBNetworkName(name string) (string, error) {
	if err := validateComponent("name", name); err != nil {
		return "", err
	}
	return dbNamePrefix + name + "-net", nil
}

// DBVolumeName 返回库数据卷名 `fleetly-db-<name>-<key>-<id8>`（id8 = 库
// 实例平台 ID 前 8 位——防代际静默复用，与 app 卷的 appid8 同纪律）。
func DBVolumeName(name, key, instanceID string) (string, error) {
	if err := validateComponent("name", name); err != nil {
		return "", err
	}
	if err := validateComponent("key", key); err != nil {
		return "", err
	}
	id8, err := instanceID8(instanceID)
	if err != nil {
		return "", err
	}
	return dbNamePrefix + name + "-" + key + "-" + id8, nil
}

// DBSecretName 返回库 secret 名 `fleetly-db-<name>-<secret>-<hash8>`
// （hash8 = 值 sha256 前 8——值轮换即换名换引用，app secret 同纪律）。
func DBSecretName(name, secret, hash8 string) (string, error) {
	if err := validateComponent("name", name); err != nil {
		return "", err
	}
	if err := validateComponent("secret", secret); err != nil {
		return "", err
	}
	if err := validateHash8(hash8); err != nil {
		return "", err
	}
	return dbNamePrefix + name + "-" + secret + "-" + hash8, nil
}

// IsDbServiceName 报告 Swarm 服务名是否为库族服务（fleetly-db- 前缀——
// 引擎对账的删除扫描与 reconcile 按此前缀豁免库服务：库收敛器自管，
// 不受发布对账「省略=删除」管辖）。
func IsDbServiceName(name string) bool {
	return strings.HasPrefix(name, dbNamePrefix)
}

// dbJobNamePrefix 是库备份/恢复/校验一次性 job 服务名的固定前缀（E4 S5，
// cron 前缀纪律同款）：完整名 `fleetly-dbjob-<instance>-<purpose>-<ulid8>`。
// **独立于库服务前缀 fleetly-db-**——一次性 job 服务的瞬时性必须可按名
// 识别：cron 孤儿清扫（internal/cron sweepOrphanJobs 只认 fleetly-cron-）、
// 引擎对账（按 app label 列举，job 服务不带 app label）、库收敛拍（只
// inspect 实例自己的服务名）与日志管线（CronJobServiceStates 只认
// fleetly-cron-）都按前缀/label 边界忽略它——前缀不独立会被某一方的
// 清扫误伤（在途备份 job 被删 = 备份静默丢失）。ulid8 使同实例重叠触发
// 的命名天然不冲突（重叠受理由操作互斥哨兵拒绝，此处只兜底）。
const dbJobNamePrefix = namePrefix + "dbjob-"

// DBJobName 返回库一次性 job 的 Swarm 服务名
// `fleetly-dbjob-<instance>-<purpose>-<ulid8>`（purpose = backup/verify/
// restore/prune 语义段；ulid8 = run ULID 前 8 位）。
func DBJobName(instance, purpose, runID string) (string, error) {
	if err := validateComponent("instance", instance); err != nil {
		return "", err
	}
	if err := validateComponent("purpose", purpose); err != nil {
		return "", err
	}
	if len(runID) < 8 {
		return "", fmt.Errorf("naming: run id %q shorter than 8 chars", runID)
	}
	return dbJobNamePrefix + instance + "-" + purpose + "-" + runID[:8], nil
}

// IsDBJobName 报告 Swarm 服务名是否为库一次性 job 服务（清扫/采集面的
// 瞬时性识别谓词，CronJobName 同款纪律）。
func IsDBJobName(name string) bool {
	return strings.HasPrefix(name, dbJobNamePrefix)
}

// instanceID8 返回资源平台 ID 的前 8 位（库卷命名尾缀；AppID8 的泛化形
// ——ULID 前 8 位已含高精度时间戳成分）。
func instanceID8(id string) (string, error) {
	if len(id) < 8 {
		return "", fmt.Errorf("naming: instance id %q shorter than 8 chars", id)
	}
	return id[:8], nil
}

// AppID8 返回 app 平台 ID 的前 8 位（卷命名防撞尾缀）。ULID 为 26 位，
// 前 8 位已含高精度时间戳成分。
func AppID8(appID string) (string, error) {
	if len(appID) < 8 {
		return "", fmt.Errorf("naming: app id %q shorter than 8 chars", appID)
	}
	return appID[:8], nil
}

// Hash8 返回内容 sha256 hex 前 8 位（secret 命名尾缀：值轮换即换名）。
func Hash8(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:8]
}

// joinName 以 '-' 连接命名成分。
func joinName(parts ...string) string {
	return namePrefix + strings.Join(parts, "-")
}

// validateComponent 校验命名成分：非空、不含命名分隔符 '-' 之外的非法字符
// 以外交界字符（compose 标识符字符集 [A-Za-z0-9._-]），防止注入分隔层级或
// 产生非法 DNS 名。
func validateComponent(field, v string) error {
	if v == "" {
		return fmt.Errorf("naming: %s is empty", field)
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("naming: %s %q contains character %q outside [A-Za-z0-9._-]", field, v, r)
		}
	}
	return nil
}

// validateHash8 校验 hash8 形态（8 位小写 hex）。
func validateHash8(h string) error {
	if len(h) != 8 {
		return fmt.Errorf("naming: hash8 %q must be 8 hex chars", h)
	}
	for _, r := range h {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			return fmt.Errorf("naming: hash8 %q must be lowercase hex", h)
		}
	}
	return nil
}
