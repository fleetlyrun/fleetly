package engine

// secret 挂载解析与装载（E4 managed-databases §2.7，D-DB-7，W4-S4）：
// compose 服务级 secrets 引用（source ∈ 顶层 external 声明，校验层已收窄）
// → 发布引擎 preparing 期按 app_secrets 装载三件事——
//
//  1. 存在性哨兵（plan-time fail-fast）：声明名在平台密钥库缺失 →
//     E_SECRET_NOT_FOUND（422，点名全部缺失名——校验层只保证声明面自洽，
//     值的存在性在引擎侧权威）；
//  2. Swarm secret 确保：naming.SecretName(app, name, hash8(明文))——值
//     轮换即换名换引用（引用进 desired-hash）；底座对象缺失时以解密后的
//     值创建（SecretEnsurer 端口，实现 = substrate.Client；在服务
//     create/update 之前在位——SecretReference 需要底座对象 ID，W3 真机
//     教训同源）；
//  3. 注入：SecretMount{SecretName, Target: /run/secrets/<target>} 进
//     ServiceSpec.Secrets——值零进规划产物（快照只带名字），明文只存活于
//     「解密 → ensure 载荷」内存链，不进日志/事件/审计/错误。
//
// 生效语义（§2.7 与库凭据轮换的刻意差异）：app secret 值变化 = 换 hash8
// 换名 → 引用与 desired-hash 变化 → 随**下次部署**换挂——无自动重部署
//（轮换即换名的引用模型下，自动触发面只有 db.rotate 的库凭据路径）。
//
// 重放路径（回滚/归位/漂移收敛）经 restoreSnapshot → applyDesired：快照
// 的 SecretMount 按名对 app_secrets 现值解析（preflightRollback 校验 +
// applyDesired 确保）——名不匹配（轮换已发生）→ E_SECRET_NOT_FOUND 诚实
// 失败，不做静默改写（D-REL-9 同族：快照不可变纪律）。

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/compose"
	"github.com/fleetlyrun/fleetly/internal/naming"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// SecretEnsurer 是引擎对 Swarm secret 对象的确保端口（D-DB-7 注入链的
// 底座原语；实现 = internal/substrate.Client，单测注入假实现）。幂等语义
// 由实现保证（inspect → 缺失才 create——名内嵌值指纹，「存在性」判据即
// 幂等）；data 只进创建载荷，实现方绝不记日志/拼错误。
type SecretEnsurer interface {
	// EnsureSecret 确认 Swarm secret 在位并返回其对象 ID；缺失时以 data
	// 创建（labels 为归属标注——清场/识别的选择器锚）。
	EnsureSecret(ctx context.Context, name string, data []byte, labels map[string]string) (string, error)
}

// resolveSecretMounts 解析本次发布的 secret 挂载面（preparing 现读密钥库，
// 不缓存长驻）。返回：服务 → SecretMount 列表（按 target 字典序——
// desired-hash 确定性）；无任何声明的 app 返回 nil（零写入零底座副作用）。
// 声明缺失 → E_SECRET_NOT_FOUND（点名全部缺失名，一次暴露全量缺口）。
func (e *Engine) resolveSecretMounts(ctx context.Context, appID, appName string, spec *compose.Spec) (map[string][]SecretMount, error) {
	// 声明收集（跨服务去重：同一 source 多服务引用 = 一次解密一次 ensure）。
	var sources []string
	for i := range spec.Services {
		for _, ref := range spec.Services[i].Secrets {
			sources = append(sources, ref.Source)
		}
	}
	if len(sources) == 0 {
		return nil, nil
	}
	sort.Strings(sources)
	sources = dedupeStrings(sources)

	// 确保端口未接线（装配缺失）：带声明的部署规划期显式失败——静默跳过
	// 会产出「部署成功但 /run/secrets 缺文件」的悬案。
	if e.secretEnsure == nil {
		return nil, errorf("E_RUNTIME_UNAVAILABLE",
			"service(s) declare compose secrets but the secret ensurer port is not wired (assembly bug: the engine requires a SecretEnsurer for secret declarations)")
	}

	// 存在性哨兵：全量缺口一次点名（可行动面——逐个修比逐轮部署撞墙诚实）。
	var missing []string
	plainBySource := make(map[string]string, len(sources))
	for _, source := range sources {
		cipher, err := e.store.GetAppSecretCipher(ctx, appID, source)
		if err != nil {
			if !errors.Is(err, state.ErrAppSecretNotFound) {
				return nil, errorf("E_RUNTIME_UNAVAILABLE", "failed to read secret %s from the platform secret store: %v", source, err)
			}
			missing = append(missing, source)
			continue
		}
		plain, derr := e.box.Decrypt([]byte(cipher))
		if derr != nil {
			// 解密失败 = 密钥/密文损坏：显式失败不静默降级（错值挂载比失败
			// 更危险——与 system env 重放路径同纪律）。
			return nil, errorf("E_RUNTIME_UNAVAILABLE",
				"failed to decrypt platform secret %s (master key mismatch or corrupted ciphertext)", source)
		}
		plainBySource[source] = string(plain)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, apperr.New("E_SECRET_NOT_FOUND",
			"secret(s) %s declared by the compose file are not in the platform secret store (set them with: fleetly secrets set %s <name> --value <value>)",
			strings.Join(missing, ", "), appName).
			WithContext("missing", strings.Join(missing, ",")).
			WithContext("app", appName)
	}

	mounts := make(map[string][]SecretMount)
	ensured := make(map[string]string, len(sources)) // source → 底座对象 ID
	for i := range spec.Services {
		svc := &spec.Services[i]
		if len(svc.Secrets) == 0 {
			continue
		}
		list := make([]SecretMount, 0, len(svc.Secrets))
		for _, ref := range svc.Secrets {
			plain := plainBySource[ref.Source]
			secretName, err := secretNameFor(appName, ref.Source, plain)
			if err != nil {
				return nil, err
			}
			if _, ok := ensured[ref.Source]; !ok {
				id, eerr := e.secretEnsure.EnsureSecret(ctx, secretName, []byte(plain), secretLabels(appName))
				if eerr != nil {
					return nil, errorf("E_RUNTIME_UNAVAILABLE", "failed to ensure swarm secret for %s: %v", ref.Source, eerr)
				}
				ensured[ref.Source] = id
			}
			list = append(list, SecretMount{SecretName: secretName, Target: "/run/secrets/" + ref.Target})
		}
		sort.Slice(list, func(a, b int) bool { return list[a].Target < list[b].Target })
		mounts[svc.Name] = list
	}
	return mounts, nil
}

// ensureSnapshotSecrets 为快照重放路径确保 secret 底座对象在位（
// applyDesired 头部调用——回滚/归位/漂移收敛共用；发布路径的 ensure 已在
// resolveSecretMounts 完成，此处幂等重入无害）。快照的挂载名必须能对
// app_secrets 现值解析（名 = fleetly-<app>-<name>-<hash8>，值轮换即换名
// ——不匹配 = 轮换已发生，挂载名悬空）：缺失解析 → E_SECRET_NOT_FOUND。
func (e *Engine) ensureSnapshotSecrets(ctx context.Context, rec state.DeployRecord, specs []ServiceSpec) error {
	// 快照挂载名收集。
	names := map[string]bool{}
	for i := range specs {
		for _, m := range specs[i].Secrets {
			names[m.SecretName] = true
		}
	}
	if len(names) == 0 {
		return nil
	}
	if e.secretEnsure == nil {
		return errorf("E_RUNTIME_UNAVAILABLE",
			"snapshot carries secret mounts but the secret ensurer port is not wired (assembly bug)")
	}
	rows, err := e.store.ListAppSecrets(ctx, rec.AppID)
	if err != nil {
		return errorf("E_RUNTIME_UNAVAILABLE", "failed to read the platform secret store for replay: %v", err)
	}
	plainByName := make(map[string]string, len(rows))
	for _, row := range rows {
		plain, derr := e.box.Decrypt([]byte(row.ValueCipher))
		if derr != nil {
			return errorf("E_RUNTIME_UNAVAILABLE",
				"failed to decrypt platform secret %s (master key mismatch or corrupted ciphertext)", row.Name)
		}
		secretName, serr := secretNameFor(rec.AppName, row.Name, string(plain))
		if serr != nil {
			return serr
		}
		plainByName[secretName] = string(plain)
	}
	for name := range names {
		plain, ok := plainByName[name]
		if !ok {
			return apperr.New("E_SECRET_NOT_FOUND",
				"snapshot secret mount %s no longer resolves against the platform secret store (the secret value was rotated or removed since this revision; redeploy to refresh mounts)", name).
				WithContext("secret", name).
				WithContext("app", rec.AppName)
		}
		if _, err := e.secretEnsure.EnsureSecret(ctx, name, []byte(plain), secretLabels(rec.AppName)); err != nil {
			return errorf("E_RUNTIME_UNAVAILABLE", "failed to ensure swarm secret for replay: %v", err)
		}
	}
	return nil
}

// secretNameFor 由密钥库声明名构造 Swarm secret 名（值轮换即换名换引用，
// managed-databases §2.7 逐字兑现 naming.SecretName 既有设计）。
func secretNameFor(app, name, plain string) (string, error) {
	secretName, err := naming.SecretName(app, name, naming.Hash8(plain))
	if err != nil {
		return "", errorf("E_RUNTIME_UNAVAILABLE", "naming failed for secret %s: %v", name, err)
	}
	return secretName, nil
}

// secretLabels 构造 app secret 的归属 label 集（清场/识别的选择器锚——
// 值轮换换名后旧对象的 best-effort 清场按 fleetly.app 选择器扫描）。
func secretLabels(app string) map[string]string {
	return map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     app,
	}
}

// dedupeStrings 保序去重。
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}
