package buckettemplate

// silo 模板（B5，ADR-0096 决策 7 / design §4.6）：pgsty/silo——MinIO 社区续
// 命 fork（MinIO 社区版 2026-02 EOL；e2e 既有 S3 假端点镜像转正为正式选
// 型，RustFS 维持 ADR-0042 挂账不吸收）。**许可 = AGPLv3**（MinIO fork 继承
// ——目录 license_note 全引擎化披露，红队 C-AT-06：AGPL 网络条款对「平台替
// 用户跑」形态的审视比 BSL 用例限制更敏感，托管合规裁定归运营方、代码面
// 不设限）。
//
// live 定案实录（2026-11-07 本机 docker 实测 pgsty/silo:RELEASE.2026-10-26
// T00-00-00Z，digest = Docker Hub index 同日实测）：
//
//   - 镜像入口 = /usr/bin/docker-entrypoint.sh → silo；Workload.Command 在
//     swarm/k3s 两 provider 皆是 entrypoint 覆盖语义——`silo server /data`
//     直接覆盖实测起服正常（entrypoint 脚本只做 _FILE env 展开与降权，
//     本平台凭证走 env 直给，绕过无损）。
//   - 健康面：MinIO 系 `/minio/health/ready`（9000 S3 API 口）实测 200；
//     镜像内 **有 curl、无 wget**（非 distroless 变体：bash+coreutils+curl
//     +mcli），探针 = `curl -sf -o /dev/null http://127.0.0.1:9000/minio/
//     health/ready`（-f 使 4xx/5xx 非零退出，-o /dev/null 不吞 stdout）。
//     distroless 变体无 shell 无 curl 不可 exec 探针——选基础变体的全部理由
//     （nats scratch→alpine 同款取舍）。
//   - 数据目录：`server /data` 起服后 /data 内生 .minio.sys——DataTarget=/data。
//   - root 凭证 env：MINIO_ROOT_USER / MINIO_ROOT_PASSWORD（MinIO 兼容 env
//     名原样继承，实测生效；root user 最短 3 字符、password 最短 8——平台
//     AK=fleetly、SK=hex 48 皆满足）。
//   - 控制台口：不传 --console-address 时随机口（实测 :36223），不暴露
//     不投影（Ports 只出 9000）。
//   - S3 roundtrip：另一容器经网络以 root AK/SK `mcli mb/cp/cat` 实测通过
//     ——e2e 腿工具容器 = 同镜像自带 mcli（mc fork；零额外镜像）。
//
// 凭证形态：root AK/SK 经启动 env（ADR-0014 A.2 追认，包注）；轮换 = 声明
// 式（RotateSpec Argv 空）——新 SK 进 Workload env → 投影指纹变 →
// Generation 推进 → stop-first 滚动。零旋钮 v1（design §3.2 表：silo 首批
// 零旋钮；MinIO 系配置面多为 env/`mc admin config`，旋钮化挂账）。

import (
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// siloRepo 是镜像仓库名；siloVersions 是在册版本矩阵（单版——VersionSpec
// 仅一枚，Default 恒真；ID 取 RELEASE 年月短名 `2026-10`，tag 取完整
// RELEASE 串；digest = 2026-11-07 Docker Hub index 实测，scripts/digests.sh
// 同口径对账）。
const siloRepo = "pgsty/silo"

var siloVersions = []enginetemplate.VersionSpec{
	{
		ID: "2026-10", Tag: "RELEASE.2026-10-26T00-00-00Z",
		Digest:  "sha256:7bb2aff6b89efe1cb1303c10fc21d579447434ed06649ac9ec05a5e2dbf30ad7",
		Default: true,
	},
}

// siloPort 是 S3 API 口（MinIO 系缺省 9000；控制台口不投影）。
const siloPort = 9000

// SiloPlatformAccessKey 是平台合成凭证的 access key（单真源；Workload env
// MINIO_ROOT_USER 与 ConnMaterial/注入面共用——NATSPlatformUser 同款）。
const SiloPlatformAccessKey = "fleetly"

// silo root 凭证 env 名（MinIO 兼容 env 名原样继承——G101 纪律：凭证形态
// 字面量拼接）。
var (
	siloEnvRootUser = "MINIO_ROOT_" + "USER"
	siloEnvRootSK   = "MINIO_ROOT_" + "PASSWORD"
)

// siloTemplate 是 silo 引擎的模板 adapter（绑版实例持 version；注册表原型
// 零值仅承载目录面）。
type siloTemplate struct {
	enginetemplate.ToolUserDefault // 工具面缺省：镜像自带用户（silo 镜像 root——B8-fix 零行为变化）
	version                        enginetemplate.VersionSpec
}

// siloOwnedKeys 是 silo 的平台 owned 键闭集（ADR-0095 决策 2 语义在
// bucketstore 家族的映射——v1 零旋钮，owned 面 = MinIO 系 env 配置面的结构
// 性键：root 凭证/监听与通告/数据目录/站点与复制/身份面）。旋钮集为空时
// 本集的守卫交集天然为空，列出是对照集完整性（漏列即回流口）。
var siloOwnedKeys = []string{
	// 认证与身份（root 凭证 env 是平台单源；IAM/OIDC/LDAP 面平台所有权）
	"minio_root_user", "minio_root_password", "minio_access_key", "minio_secret_key",
	"minio_identity_openid", "minio_identity_ldap", "minio_identity_plugin",
	// 监听与通告（S3 API 口与控制台口；server 命令面 /data 同源）
	"minio_address", "minio_console_address", "minio_server_url", "minio_browser_redirect_url",
	// 数据目录与站点/复制面（卷布局与多站点是平台所有权）
	"minio_volumes", "minio_site_name", "minio_site_region", "minio_erasure_set_drive_count",
	// 文件包含与系统面
	"minio_config_env_file", "minio_root_user_file", "minio_root_password_file",
}

// Knobs 返回 silo 旋钮白名单（v1 零旋钮——nil；目录 knobs 空、Update 只
// 有 resources 面）。
func (siloTemplate) Knobs() []enginetemplate.Knob { return nil }

// OwnedKeys 返回 silo 平台 owned 键闭集（副本防调用方改注册表）。
func (siloTemplate) OwnedKeys() []string {
	out := make([]string, len(siloOwnedKeys))
	copy(out, siloOwnedKeys)
	return out
}

func (siloTemplate) Engine() string { return "silo" }
func (siloTemplate) Versions() []enginetemplate.VersionSpec {
	out := make([]enginetemplate.VersionSpec, len(siloVersions))
	copy(out, siloVersions)
	return out
}
func (siloTemplate) bindVersion(spec enginetemplate.VersionSpec) Template {
	return siloTemplate{version: spec}
}
func (t siloTemplate) Meta() Info { return Info{Version: t.version.ID, Port: siloPort} }
func (t siloTemplate) Image() string {
	return pinnedRef(siloRepo+":"+t.version.Tag, t.version.Digest)
}
func (t siloTemplate) ImageDigest() string { return t.version.Digest }
func (siloTemplate) DataTarget() string    { return "/data" }

// Workload 渲染 env/command：命令面 `silo server /data`（entrypoint 覆盖语
// 义，live 定案实测）；env 携带 root AK/SK（ADR-0014 A.2 追认——长效凭证
// 随载体 spec env 下发；SK 进 env 即进投影指纹，轮换=滚动的机械由此成立）。
// 两段凭证皆过渲染安全闸（[0-9A-Za-z]——env 值无引号语法面，闸是纵深：
// AK 是平台常量、SK 是 hex 48，hostile 值到不了这里也照样拒）。static
// 旋钮不进命令面（v1 零旋钮；签名带参保持接口齐形）。
func (siloTemplate) Workload(accessKey, secretKey string, _ map[string]string) (map[string]string, []string, error) {
	if err := validateAccessKey(accessKey); err != nil {
		return nil, nil, err
	}
	if err := validatePassword(secretKey); err != nil {
		return nil, nil, err
	}
	env := map[string]string{
		siloEnvRootUser: accessKey,
		siloEnvRootSK:   secretKey,
	}
	return env, []string{"silo", "server", "/data"}, nil
}

// Probe 渲染存活探针（live 定案：镜像带 curl 无 wget；MinIO 系
// /minio/health/ready 200 即就绪——-f 把 4xx/5xx 翻成非零退出）。
func (siloTemplate) Probe() []string {
	return []string{"curl", "-sf", "-o", "/dev/null", "http://127.0.0.1:9000/minio/health/ready"}
}

// Materials 渲染文件材料面：silo 凭证住 env、无 conf 文件——恒空 map（接口
// 齐形；空产物与「无材料」逐字节等价，签名第四段的 static 子集摘要对零
// 旋钮恒定）。
func (siloTemplate) Materials(_ map[string]string) (map[string][]byte, error) {
	return map[string][]byte{}, nil
}

// ConnMaterial 铸连接材料方言串（S3 方言 = endpoint + AK/SK k=v 串；host
// 由调用方注入——铸名公式真源在 engineports）。
func (siloTemplate) ConnMaterial(host, accessKey, secretKey string) string {
	return RenderConnMaterial(ConnMaterial{
		Endpoint:  fmt.Sprintf("%s:%d", host, siloPort),
		AccessKey: accessKey,
		SecretKey: secretKey,
	})
}

// RotatePassword 声明式轮换（ADR-0096 决策 4：silo root SK 住启动 env——
// 轮换 = Secret 重写 + 新 SK 进投影 env + Generation 推进 = stop-first 滚动；
// Argv 空）。current/next 仍过字符集闸：next 即将进新载体 env。
func (siloTemplate) RotatePassword(host, current, next string) (enginetemplate.RotateSpec, error) {
	_ = host
	if err := validatePassword(current); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	if err := validatePassword(next); err != nil {
		return enginetemplate.RotateSpec{}, err
	}
	return enginetemplate.RotateSpec{}, nil
}

// ApplyDynamicSettings：silo 无 dynamic 面（v1 零旋钮）。非空 settings 即
// 哨兵（正常分流不可达：ValidateSettings 在受理位已把白名单收敛到空集）。
func (siloTemplate) ApplyDynamicSettings(_, _ string, _ map[string]string) (enginetemplate.ApplySpec, error) {
	return enginetemplate.ApplySpec{}, enginetemplate.ErrNoDynamicFace
}

// validateAccessKey 钳制 access key（MinIO root user 最短 3 字符；字符集
// 与密码闸同——env 值与 k=v 方言串两面防线：`;`/`=` 不可进段）。
func validateAccessKey(accessKey string) error {
	if len(accessKey) < 3 {
		return fmt.Errorf("buckettemplate: access key must be at least 3 characters")
	}
	if err := validatePassword(accessKey); err != nil {
		return fmt.Errorf("buckettemplate: access key: %w", err)
	}
	return nil
}
