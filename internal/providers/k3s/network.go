package k3s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/oklog/ulid/v2"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// ensureNamespace 建 per-Project Namespace（幂等：已存在即复用；ADR-0052
// 决策 3：Namespace 即互通域——不存在 swarm 侧 overlay 网络的 attachable
// flag-day 面，Namespace 恒存在无重建动词需求）。
func (p *Provider) ensureNamespace(ctx context.Context, nsName string) error {
	_, err := p.cli.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   nsName,
			Labels: map[string]string{labelManaged: "true"},
		},
	}, metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure namespace %s: %w", nsName, err)
	}
	return nil
}

// reconcileEgressNetpol 收敛域上的 egress deny NetworkPolicy（ADR-0052
// 决策 6：域内存在 egress 载体 → 恒有一条 deny policy；全部期望载体不再
// 挂 egress 网络时撤除——policy 与载体标记共同构成隔离不变式）。
func (p *Provider) reconcileEgressNetpol(ctx context.Context, nsName string, ws []capability.Workload) error {
	need := false
	for _, w := range ws {
		if len(w.EgressNetworks) > 0 {
			need = true
			break
		}
	}
	name := toEgressNetpol().Name
	if !need {
		err := p.cli.NetworkingV1().NetworkPolicies(nsName).Delete(ctx, name, metav1.DeleteOptions{})
		if err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("remove egress netpol: %w", err)
		}
		return nil
	}
	_, err := p.cli.NetworkingV1().NetworkPolicies(nsName).Create(ctx, toEgressNetpol(), metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure egress netpol: %w", err)
	}
	return nil
}

// serviceLabels 是域内 Service 的归属标记（域收敛对照面——Service 名是
// 平台 Addressing 名，不带 workload 轴，team/project 双锚过滤）。
func serviceLabels(ns capability.NamespaceRef) map[string]string {
	return map[string]string{
		labelManaged: "true",
		labelTeam:    sanitizeNamePart(ns.Team),
		labelProject: sanitizeNamePart(ns.Project),
	}
}

// ensureSecrets 落盘 Secret 材料（ADR-0014：值经 k8s Secret 分发，容器内
// /run/secrets/<名> 文件注入——不落载体 label 或明文 env）。对象名纳入
// 域标识：Namespace 是 per-Project 的，同项目第二个 App/Database 的同名
// 材料（如 "database-password"）会撞 AlreadyExists 沿用首库密码——第二
// 个库即用错凭证初始化（restore 密码不合的终极根因，e2e 取证矩阵闭环：
// passfile=行真源密码 vs 载体 secret=首库旧密码）。返回平台名 → Secret
// 对象名的解析集（翻译层引用）。
func (p *Provider) ensureSecrets(ctx context.Context, ns capability.NamespaceRef, nsName string, files map[string][]byte) (map[string]string, error) {
	out := make(map[string]string, len(files))
	for platformName, value := range files {
		objName := domainSecretObjectName(ns, platformName)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:   objName,
				Labels: map[string]string{labelManaged: "true"},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{secretDataKey: value},
		}
		_, err := p.cli.CoreV1().Secrets(nsName).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("ensure secret %s: %w", objName, err)
		}
		out[platformName] = objName
	}
	return out, nil
}

// domainSecretObjectName 是材料 Secret 的域唯一对象名（域标识取六轴首个
// 非空轴——App/Database/Task/Browse 各自独立成域；受管域无轴用 system）。
func domainSecretObjectName(ns capability.NamespaceRef, platformName string) string {
	for _, axis := range []string{ns.App, ns.Database, ns.Task, ns.Browse} {
		if axis != "" {
			return "fleetly-mat-" + sanitizeNamePart(axis) + "-" + sanitizeNamePart(platformName)
		}
	}
	return secretObjectName(platformName)
}

// ensureImagePullSecrets 落盘 registry 拉取凭证（dockerconfigjson 形态；
// Materials.RegistryAuth 的 server → imagePullSecrets 引用名列表）。
func (p *Provider) ensureImagePullSecrets(ctx context.Context, nsName string, auth map[string]capability.RegistryCredential) ([]string, error) {
	if len(auth) == 0 {
		return nil, nil
	}
	var names []string
	for _, server := range sortedKeys(auth) {
		cred := auth[server]
		objName := imagePullSecretName(server)
		dockerCfg := map[string]map[string]string{
			server: {
				"username": cred.Username,
				"password": cred.Secret,
				"auth":     base64.StdEncoding.EncodeToString([]byte(cred.Username + ":" + cred.Secret)),
			},
		}
		raw, merr := json.Marshal(map[string]any{"auths": dockerCfg})
		if merr != nil {
			return nil, fmt.Errorf("ensure pull secret %s: %w", objName, merr)
		}
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:   objName,
				Labels: map[string]string{labelManaged: "true"},
			},
			Type: corev1.SecretTypeDockerConfigJson,
			Data: map[string][]byte{corev1.DockerConfigJsonKey: raw},
		}
		_, err := p.cli.CoreV1().Secrets(nsName).Create(ctx, secret, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("ensure pull secret %s: %w", objName, err)
		}
		names = append(names, objName)
	}
	return names, nil
}

// ensurePVC 建 Volume 的 PVC 声明（幂等：已存在即复用——尊重预创建同名
// claim 的既有绑定，与 swarm 预创建同名卷复用语义对齐）。local-path 绑定
// 发生在首次调度（卷钉住 = PV 节点亲和天然承载）。
func (p *Provider) ensurePVC(ctx context.Context, nsName, volumeID string) error {
	_, err := p.cli.CoreV1().PersistentVolumeClaims(nsName).Create(ctx, toPVC(volumeID), metav1.CreateOptions{})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("ensure pvc %s: %w", pvcName(volumeID), err)
	}
	return nil
}

// anchorNodes 是节点身份锚定（架构 §5 契约义务，D-MN-8）：扫描全部 Node，
// 无 fleetly.node.id 标记的 → 铸平台节点 ID（ULID）→ label 写回 → NodeJoined
// 事件（经 Watch 流上报）。刷新 nodeIDs 观测缓存（Node 名 → 平台 ID）。
func (p *Provider) anchorNodes(ctx context.Context, out chan<- capability.WorkloadEvent) error {
	nodes, err := p.cli.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("k3s anchor nodes: %w", err)
	}
	cache := make(map[string]string, len(nodes.Items))
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if id := node.Labels[labelNodeID]; id != "" {
			cache[node.Name] = id
			continue
		}
		id := ulid.Make().String()
		node.Labels[labelNodeID] = id
		updated, err := p.cli.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		if err != nil {
			// 锚定失败不阻断观测（下次扫描重试）；缓存不收未写回节点。
			continue
		}
		cache[node.Name] = id
		if out != nil {
			ev := capability.WorkloadEvent{
				NodeJoined: &capability.NodeJoined{
					NodeID:    id,
					CarrierID: node.Name,
					Minted:    true,
				},
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}
		_ = updated
	}
	p.nodeIDMu.Lock()
	p.nodeIDs = cache
	p.nodeIDMu.Unlock()
	return nil
}

// platformNodeID 把 k8s 节点名还原为平台节点 ID（锚定缓存；空 = 未锚定，
// 下一锚定拍补齐）。
func (p *Provider) platformNodeID(nodeName string) string {
	p.nodeIDMu.Lock()
	defer p.nodeIDMu.Unlock()
	return p.nodeIDs[nodeName]
}

// recordLastIssued 落账（no-op 断路器）。
func (p *Provider) recordLastIssued(name, canonical string) {
	p.ledgerMu.Lock()
	defer p.ledgerMu.Unlock()
	if p.lastIssued == nil {
		p.lastIssued = map[string]string{}
	}
	p.lastIssued[name] = canonical
}

func (p *Provider) lastIssuedOf(name string) string {
	p.ledgerMu.Lock()
	defer p.ledgerMu.Unlock()
	return p.lastIssued[name]
}

func (p *Provider) forgetLastIssued(names ...string) {
	p.ledgerMu.Lock()
	defer p.ledgerMu.Unlock()
	for _, n := range names {
		delete(p.lastIssued, n)
	}
}
