package pubtemplate

// nats 模板（B4b，ADR-0096 决策 7 / design §4.5/§4.6）：nats:<ver>-alpine
// 承载 JetStream 单节点——默认 scratch 变体无 shell（无 exec 探针/无工具），
// alpine 带 busybox wget 打 8222 监控口 /healthz（「不依赖 nc 假设」探针律
// 满足）。工具容器 = natsio/nats-box 钉版（synadia/ 命名空间停更 5+ 年不可
// 用，红队 C-AT-06）——逐流转储/恢复/流清单枚举全走 nats-box。
//
// 凭证形态：平台合成用户（nats-server.conf users 段，声明式轮换 = Secret
// 重写 + 载体重下发——redis requirepass 同款）；Secret 值 = ConnMaterial
// 方言串（nats://<user>:<pw>@<host>:4222，URL 形——pubsub 家族的 nats
// 方言；kafka BOOTSTRAP 方言 B6 接入）。材料纪律例外登记：nats-box 的
// nats CLI 无文件面密码通道（--creds 是 JWT/seed 形态，非 user/pass），
// NATS_URL/NATS_USER/NATS_PASSWORD env 是 REDISCLI_AUTH 同款的工具容器
// 一次性 env 例外（ADR-0039 决策 5 例外族；无持久面）。
//
// 转储契约（design §4.5 L4 家族特化表）：备份 = **逐流** object_set——
// `nats stream backup <stream> <out-dir>` 产 <out-dir>/<stream>.tgz 文件
// 不产 stdout 流（红队 A2①「NATS 不可流式」）；流清单 `nats stream ls
// --json` 枚举 + `nats stream info <stream> --json` 逐流探测 storage 类型
// ——memory storage 流不可快照，受理拒（红队 C-AT-06）。恢复 =
// `nats stream restore <stream> <in-dir>`（空流前提，全流循环）。KV
// bucket 是流（KV_$ bucket 名）天然覆盖。

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// NatsConfFile 是 nats 凭证材料文件名（容器内 /run/secrets/<名>；平台合成
// 的 users 段 + jetstream 旋钮折叠）。
const NatsConfFile = "database-nats-conf"

// natsRepo/natsBoxRepo 是镜像仓库名；natsVersions 是在册版本矩阵（digest =
// 2026-11-07 Docker Hub index 实测，scripts/digests.sh 同口径对账）。
// natsBoxVersions 是工具容器矩阵（逐流转储执行面；与引擎版本无耦合——
// 独立钉版）。
const (
	natsRepo    = "nats"
	natsBoxRepo = "natsio/nats-box"
)

var natsVersions = []enginetemplate.VersionSpec{
	{
		ID: "2.10", Tag: "2.10-alpine",
		Digest: "sha256:b83efabe3e7def1e0a4a31ec6e078999bb17c80363f881df35edc70fcb6bb927",
	},
	{
		ID: "2.11", Tag: "2.11-alpine",
		Digest:  "sha256:e4bf19f15fd3218814a4e3c9e0064e1334bd8aa20d5984b9f1a0afd084f8cc00",
		Default: true,
	},
}

// natsBoxVersion 是 natsio/nats-box 工具容器钉版（0.15.0；digest = 2026-11-07
// Docker Hub index 实测）。
var natsBoxVersion = enginetemplate.VersionSpec{
	ID: "0.15.0", Tag: "0.15.0",
	Digest: "sha256:66da9616df76fb4a7898e10028bb29a31ade15da2779e4de6504b103c9e97d46",
}

// natsTemplate 是 nats 引擎的模板 adapter（绑版实例持 version；注册表原型
// 零值仅承载目录面）。
type natsTemplate struct {
	version enginetemplate.VersionSpec
}

// natsKnobs 是 nats 首批旋钮（B4b，design §4.5 家族特化 + §3.4 形状）——
// 全 static：JetStream 资源面住在 nats-server.conf 的 jetstream{} 段
// （平台合成材料），无自持久热应用面。键 = 引擎原生词（jetstream 段
// max_file/max_memory），不发明中间名。
var natsKnobs = []enginetemplate.Knob{
	{Key: "jetstream_max_file", Kind: enginetemplate.KnobSize,
		Class: enginetemplate.KnobStatic, Category: "persistence"},
	{Key: "jetstream_max_memory", Kind: enginetemplate.KnobSize,
		Class: enginetemplate.KnobStatic, Category: "memory"},
}

// natsOwnedKeys 是 nats 的平台 owned 键闭集（ADR-0095 决策 2 语义在
// pubsub 家族的映射——旋钮面收窄到 jetstream 资源两键，owned 面 = 平台
// 合成 conf 的结构性键）：authorization/users 是平台凭证单源（轮换走
// Secret 重写）；store_dir/http_port 决定数据落卷与监控口（卷布局与探针
// = 平台所有权——命令面 -js -sd /data -m 8222 同源）；include/系统面
// 键防旋钮回流。
var natsOwnedKeys = []string{
	// 认证（users/authorization/cluster perms = 平台合成 conf 的凭证段）
	"authorization", "users", "user", "password", "nkeys", "resolver",
	// 监听与通告
	"port", "listen", "client_advertise", "cluster", "gateways", "leafnodes",
	// 监控与数据目录（store_dir 与命令面 -sd 同源；http/monitor 与 -m 同源）
	"http", "http_port", "https_port", "monitor", "store_dir", "jetstream",
	// 文件包含与系统面
	"include", "systemd", "pid_file", "log_file",
}

// Knobs 返回 nats 旋钮白名单（副本防调用方改注册表）。
func (natsTemplate) Knobs() []enginetemplate.Knob {
	out := make([]enginetemplate.Knob, len(natsKnobs))
	copy(out, natsKnobs)
	return out
}

// OwnedKeys 返回 nats 平台 owned 键闭集（副本同上）。
func (natsTemplate) OwnedKeys() []string {
	out := make([]string, len(natsOwnedKeys))
	copy(out, natsOwnedKeys)
	return out
}

func (natsTemplate) Engine() string  { return "nats" }
func (natsTemplate) Dialect() string { return "nats" }
func (natsTemplate) DataCapabilities() enginetemplate.DataCapabilities {
	// 引擎级能力位（B6a per-engine 拆分）：nats 逐流备份面在册（族缺省
	// 同值——StreamDumpFace 是消费面）。
	return enginetemplate.DataCapabilities{Backup: true}
}
func (natsTemplate) Versions() []enginetemplate.VersionSpec {
	out := make([]enginetemplate.VersionSpec, len(natsVersions))
	copy(out, natsVersions)
	return out
}
func (natsTemplate) bindVersion(spec enginetemplate.VersionSpec) Template {
	return natsTemplate{version: spec}
}
func (t natsTemplate) Meta() Info { return Info{Version: t.version.ID, Port: 4222} }
func (t natsTemplate) Image() string {
	return pinnedRef(natsRepo+":"+t.version.Tag, t.version.Digest)
}
func (t natsTemplate) ImageDigest() string { return t.version.Digest }
func (natsTemplate) DataTarget() string    { return "/data" }

// NatsBoxImage 返回 nats-box 工具容器钉版镜像（逐流转储/恢复/流清单的
// 执行面；Template.Image 是引擎载体面——两镜像分立，本面独立钉版）。
func NatsBoxImage() string {
	return pinnedRef(natsBoxRepo+":"+natsBoxVersion.Tag, natsBoxVersion.Digest)
}

// Workload 渲染 env/command：命令面携带 -js（JetStream 启用）、-sd /data
// （存储目录）、-m 8222（监控口供探针）、-c 平台合成 conf（users 段）。
// 「-js -sd 也可全 conf 化」的取舍（design §4.6）：保留命令面形态——与
// dbtemplate 引擎的 Workload 命令面先例同构（postgres -c 覆写同款），conf
// 材料只承载凭证与旋钮折叠面；conf 化 jetstream{} 段的等价形态在 conf 注释
// 面披露（Materials）。static 旋钮不进命令面（住 conf 材料——喂入律：本
// 引擎无 dynamic 旋钮故无泄漏面；host/memoryMB 是 kafka 通告/heap 联动的
// 接口扩面，nats 忽略——签名齐形）。
func (natsTemplate) Workload(_ string, _ int64, _ map[string]string) (map[string]string, []string, error) {
	return nil, []string{"nats-server", "-js", "-sd", "/data", "-m", "8222", "-c", "/run/secrets/" + NatsConfFile}, nil
}

// Probe 渲染存活探针（design §4.6：alpine 带 busybox wget 打监控口
// /healthz——200 即健康；scratch 变体无 shell 不可 exec 是选 alpine 的
// 全部理由）。
func (natsTemplate) Probe() []string {
	return []string{"wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8222/healthz"}
}

// Materials 渲染 nats Workload 的凭证材料与 static 旋钮折叠面：平台合成
// nats-server.conf——users 段（平台用户 + 密码）+ jetstream{} 资源段
// （旋钮 max_file/max_memory；只在设置时渲染——空 settings 的产物与无旋钮
// 现状逐字节一致）。密码过渲染安全闸（[0-9A-Za-z]，enginetemplate.
// ValidatePassword——conf 行语法与 URL 双面防线）。
func (natsTemplate) Materials(password string, staticSettings map[string]string) (map[string][]byte, error) {
	if err := validatePassword(password); err != nil {
		return nil, err
	}
	keys, static, err := staticKnobSettings(natsKnobs, staticSettings)
	if err != nil {
		return nil, err
	}
	var conf strings.Builder
	// 凭证段：平台合成用户（声明式轮换——Secret 重写即新 conf 随载体重
	// 下发，redis requirepass 同款）。NATSPlatformUser 是平台用户名单源。
	conf.WriteString("authorization {\n")
	conf.WriteString("  users: [\n")
	conf.WriteString("    {user: \"" + NATSPlatformUser + "\", password: \"" + password + "\"}\n")
	conf.WriteString("  ]\n")
	conf.WriteString("}\n")
	// jetstream{} 资源段：旋钮折叠面（max_file/max_memory；引擎原生键）。
	// store_dir 不在此渲染——命令面 -sd /data 单源（conf 化等价形态：
	// jetstream { store_dir: /data }；两形态取一，命令面与探针口 -m 8222
	// 同族，卷布局是平台所有权）。
	if len(keys) > 0 {
		conf.WriteString("jetstream {\n")
		for _, k := range keys {
			conf.WriteString("  " + natsConfKey(k) + ": " + natsConfSize(static[k]) + "\n")
		}
		conf.WriteString("}\n")
	}
	return map[string][]byte{NatsConfFile: []byte(conf.String())}, nil
}

// natsConfKey 把旋钮键折回 nats conf 原生键（jetstream_max_file →
// max_file——旋钮命名带 jetstream_ 前缀防与其它段撞键，渲染面还原段内
// 原生词面）。
func natsConfKey(knobKey string) string {
	return strings.TrimPrefix(knobKey, "jetstream_")
}

// natsConfSize 渲染 size 值的 nats conf 字面（引擎收 <num><B|K|M|G|P> 形，
// 1024 进制——受理口径 knob 单位 mb/gb 同为 1024 进制，数值直映射大写
// 单位即同值：512mb → 512MB）。
func natsConfSize(v string) string {
	return strings.ToUpper(v)
}

// NATSPlatformUser 是平台合成凭证的用户名（单真源；conf 材料与
// ConnMaterial/注入面共用）。
const NATSPlatformUser = "fleetly"

// ConnMaterial 铸 nats 方言连接材料（URL 形；host 由调用方注入——铸名
// 公式真源在 engineports）。
func (natsTemplate) ConnMaterial(host, password string) ConnMaterialSpec {
	u := &url.URL{
		Scheme: "nats",
		User:   url.UserPassword(NATSPlatformUser, password),
		Host:   fmt.Sprintf("%s:%d", host, 4222),
	}
	return ConnMaterialSpec{URL: u.String(), User: NATSPlatformUser, Password: password}
}

// ErrStreamScopedBackup/ErrStreamScopedRestore 是「本引擎转储面逐流」哨兵：
// Template.Backup/Restore 的单流契约对 nats 结构性不适用——backups 卫星
// 经 StreamDumpFace（ListStreams/InspectStream/DumpStream/RestoreStream）
// 消费逐流方言面。错误面先于哨兵：密码仍过渲染闸（纵深）。
var (
	ErrStreamScopedBackup  = errors.New("pubtemplate: nats backup is stream-scoped — use the StreamDumpFace (list/inspect/dump per stream)")
	ErrStreamScopedRestore = errors.New("pubtemplate: nats restore is stream-scoped — use the StreamDumpFace (restore per stream)")
)

// Backup 单流契约对 nats 不适用（逐流 object_set；哨兵文案即设计事实）。
func (natsTemplate) Backup(host, password string) (enginetemplate.BackupSpec, error) {
	_ = host
	if err := validatePassword(password); err != nil {
		return enginetemplate.BackupSpec{}, err
	}
	return enginetemplate.BackupSpec{}, ErrStreamScopedBackup
}

// Restore 单流契约对 nats 不适用（逐流恢复；哨兵同上）。
func (natsTemplate) Restore(host, password string) (enginetemplate.RestoreSpec, error) {
	_ = host
	if err := validatePassword(password); err != nil {
		return enginetemplate.RestoreSpec{}, err
	}
	return enginetemplate.RestoreSpec{}, ErrStreamScopedRestore
}

// RotatePassword 声明式轮换（ADR-0096 决策 4：nats = 平台合成用户住
// nats-server.conf——轮换 = Secret 重写 + 载体重下发，无破窗；Argv 空）。
// current/next 仍过字符集闸：next 即将进新 conf 材料。
func (natsTemplate) RotatePassword(host, current, next string) (enginetemplate.RotateSpec, error) {
	_ = host
	if err := validatePassword(current); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	return enginetemplate.RotateSpec{}, nil
}

// ApplyDynamicSettings：nats 无 dynamic 面（首批旋钮全 static——JetStream
// 资源面住 conf 材料，变更经滚动生效）。非空 settings 即哨兵（正常分流
// 不可达：ValidateSettings 在受理位已把白名单收敛到全 static 集）。
func (natsTemplate) ApplyDynamicSettings(_, _ string, _ map[string]string) (enginetemplate.ApplySpec, error) {
	return enginetemplate.ApplySpec{}, enginetemplate.ErrNoDynamicFace
}

// —— 逐流转储方言面（StreamDumpFace 的 nats 实现；design §4.5 物理约束
// A2①：NATS 不可流式——产文件不产 stdout 流） ——

// natsBoxEnv 铸 nats-box 工具容器的连接 env（NATS_URL/NATS_USER/
// NATS_PASSWORD——nats CLI 的原生 env 面；REDISCLI_AUTH 同款例外登记：
// 工具容器无文件面密码通道，一次性 env，无持久面）。
func natsBoxEnv(host, password string) map[string]string {
	return map[string]string{
		"NATS_URL":      "nats://" + host + ":4222",
		"NATS_USER":     NATSPlatformUser,
		"NATS_PASSWORD": password,
	}
}

// ListStreams 渲染流清单枚举（stdout = JSON：`nats stream ls --json` 出
// 流名数组）。KV bucket 是流（KV_* 名）天然在列。
func (natsTemplate) ListStreams(host, password string) (enginetemplate.BackupSpec, error) {
	if err := validatePassword(password); err != nil {
		return enginetemplate.BackupSpec{}, err
	}
	return enginetemplate.BackupSpec{
		Argv: []string{"nats", "stream", "ls", "--json"},
		Env:  natsBoxEnv(host, password),
	}, nil
}

// InspectStream 渲染单流探测（stdout = `nats stream info <stream> --json`
// 的流信息 JSON——config.storage 是 memory 流拒判据）。
func (natsTemplate) InspectStream(host, password, stream string) (enginetemplate.BackupSpec, error) {
	if err := validatePassword(password); err != nil {
		return enginetemplate.BackupSpec{}, err
	}
	if err := validateStreamName(stream); err != nil {
		return enginetemplate.BackupSpec{}, err
	}
	return enginetemplate.BackupSpec{
		Argv: []string{"nats", "stream", "info", stream, "--json"},
		Env:  natsBoxEnv(host, password),
	}, nil
}

// DumpStream 渲染单流备份：`nats stream backup <stream> <out-dir>`——nats
// CLI 向 out-dir 写备份目录（backup.json 元数据 + 分块文件），不产 stdout
// 流（红队 A2①）。调用方经 UtilityHostDir 回读目录并打成单成员 .tgz 再
// Put（StreamMemberFile 公式）；物理约束：manager 磁盘峰值 = 最大单成员
// （逐成员串行「落盘→Put→删本地」）。
func (natsTemplate) DumpStream(host, password, stream, outDir string) (enginetemplate.BackupSpec, error) {
	if err := validatePassword(password); err != nil {
		return enginetemplate.BackupSpec{}, err
	}
	if err := validateStreamName(stream); err != nil {
		return enginetemplate.BackupSpec{}, err
	}
	if outDir == "" {
		return enginetemplate.BackupSpec{}, fmt.Errorf("pubtemplate: dump stream %q requires an output directory", stream)
	}
	return enginetemplate.BackupSpec{
		Argv: []string{"nats", "stream", "backup", stream, outDir},
		Env:  natsBoxEnv(host, password),
	}, nil
}

// RestoreStream 渲染单流恢复：`nats stream restore <in-dir>`（nats CLI 从
// 备份目录内的 backup.json 元数据取流名与配置——流名不作 argv 位置参数；
// 空流前提：目标流不存在或零消息）。恢复输入 = 备份成员 .tgz 由调用方解包
// 进 in-dir（manager 物化 → UtilityHostDir 只读挂载），多成员逐流执行。
// stream 参数仍校验（成员名/诊断面），不进 argv。
func (natsTemplate) RestoreStream(host, password, stream, inDir string) (enginetemplate.RestoreSpec, error) {
	if err := validatePassword(password); err != nil {
		return enginetemplate.RestoreSpec{}, err
	}
	if err := validateStreamName(stream); err != nil {
		return enginetemplate.RestoreSpec{}, err
	}
	if inDir == "" {
		return enginetemplate.RestoreSpec{}, fmt.Errorf("pubtemplate: restore stream %q requires an input directory", stream)
	}
	return enginetemplate.RestoreSpec{
		Mode: enginetemplate.RestoreStream,
		Argv: []string{"nats", "stream", "restore", inDir},
		Env:  natsBoxEnv(host, password),
	}, nil
}

// StreamMemberFile 是单流备份产物文件名公式（<stream>.tgz；DumpStream 产
// 物与 RestoreStream 输入的单源——backups 卫星与 manifest 成员名共用）。
func StreamMemberFile(stream string) string { return stream + ".tgz" }

// validateStreamName 钳制流名（nats 流名值域：点分 token，字符集
// [A-Za-z0-9_-.>]；备份/恢复 argv 的注入面——备份成员名兼 ObjectStore
// 对象键段与 manifest 成员名，收敛到无路径分隔符的保守子集）。
func validateStreamName(stream string) error {
	if stream == "" || len(stream) > 256 {
		return fmt.Errorf("pubtemplate: stream name %q is empty or too long", stream)
	}
	for i := 0; i < len(stream); i++ {
		c := stream[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_' || c == '-' || c == '.' || c == '>':
		default:
			return fmt.Errorf("pubtemplate: stream name %q carries characters outside the [A-Za-z0-9_-.>] set", stream)
		}
	}
	return nil
}
