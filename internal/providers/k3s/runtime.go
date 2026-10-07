package k3s

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/util/retry"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// nodeTokenPath 是 k3s node token 的文件位置（k3s 发行缺省；Enrollment
// 的材料源——Provider 与 k3s server 同机 = 控制面单机假设，ADR-0019 同款
// 合法形态）。
const nodeTokenPath = "/var/lib/rancher/k3s/server/node-token" //nolint:gosec // 路径常量（k3s 发行缺省），非凭证本体；token 值运行期读取

// watchPollInterval 是 Watch 循环里节点锚定扫描与全量对账的节拍（事件流
// 覆盖即时路径；锚定检出上限 ≈ 该间隔——swarm anchoringPollInterval 同源
// 语义）。
const watchPollInterval = 10 * time.Second

// Ensure 幂等下发期望状态（架构 §5：唯一写动词）。
//
// 域内收敛语义：以 ns label 选择器列出 fleetly 管辖的域内对象，逐
// Workload create-or-update（no-op 两道闸：lastIssued 账本短路 + canonical
// 深比对），不在期望集内的域内对象移除——同 Generation 重放安全（场景 1）。
func (p *Provider) Ensure(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, m capability.Materials) (err error) {
	// 期望集载体名 + 碰撞前置拒绝（swarm B9 同款：不同 Workload 折叠成同
	// 载体名会让后者静默覆盖前者）。
	desired := make(map[string]string, len(ws))
	for _, w := range ws {
		name := workloadName(ns, w, gen)
		if prev, ok := desired[name]; ok && prev != w.ID {
			return fmt.Errorf("k3s ensure %s: workloads %s and %s both resolve to object name %q (carrier name collision; process names must be distinct DNS labels)", ns, prev, w.ID, name)
		}
		desired[name] = w.ID
	}

	// 断路器账本整拍作废（Ensure 失败 = 拍内收敛未完成，下拍保守重发）。
	defer func() {
		if err != nil {
			p.forgetLastIssued(sortedKeys(desired)...)
		}
	}()

	nsName := namespaceName(ns)
	if err := p.ensureNamespace(ctx, nsName); err != nil {
		return fmt.Errorf("k3s ensure %s: %w", ns, err)
	}
	// egress 强隔离（ADR-0052 决策 6）：域内存在 egress 载体 → 域上恒有
	// 一条 deny policy（幂等收敛；全部期望载体都不再挂 egress 网络时撤除）。
	if err := p.reconcileEgressNetpol(ctx, nsName, ws); err != nil {
		return fmt.Errorf("k3s ensure %s: %w", ns, err)
	}

	// 材料先行（ADR-0014）：Secret 对象落盘 + 拉取凭证（imagePullSecrets），
	// 再翻译载体 spec（引用对象名）。逐载体材料覆写（ADR-0048 双代窗）：
	// nil = 调用级（域默认）。
	callSecrets, err := p.ensureSecrets(ctx, ns, nsName, m.SecretFiles)
	if err != nil {
		return fmt.Errorf("k3s ensure %s: %w", ns, err)
	}
	pullNames, err := p.ensureImagePullSecrets(ctx, nsName, m.RegistryAuth)
	if err != nil {
		return fmt.Errorf("k3s ensure %s: %w", ns, err)
	}
	carrierSets := map[*capability.Materials]map[string]string{&m: callSecrets}
	for i := range ws {
		pm := ws[i].Materials
		if pm == nil {
			continue
		}
		if _, seen := carrierSets[pm]; seen {
			continue
		}
		c, cerr := p.ensureSecrets(ctx, ns, nsName, pm.SecretFiles)
		if cerr != nil {
			return fmt.Errorf("k3s ensure %s: %w", ns, cerr)
		}
		carrierSets[pm] = c
	}
	secretFilesOf := func(w capability.Workload) map[string]string {
		if w.Materials != nil {
			if c, ok := carrierSets[w.Materials]; ok {
				return c
			}
		}
		return callSecrets
	}

	// PVC 先行（Volume 附件；local-path 绑定发生在首次调度——ensurePVC
	// 只建 claim，等待由调度承载）。
	for _, w := range ws {
		for _, v := range w.Volumes {
			if err := p.ensurePVC(ctx, nsName, v.VolumeID); err != nil {
				return fmt.Errorf("k3s ensure %s: %w", ns, err)
			}
		}
	}

	existing, err := p.listDomainObjects(ctx, ns)
	if err != nil {
		return fmt.Errorf("k3s ensure %s: list existing: %w", ns, err)
	}

	// 服务名集合（域内收敛对照 + Service 期望集）。
	wantServices := map[string]bool{}
	for _, w := range ws {
		secretFiles := secretFilesOf(w)
		name := workloadName(ns, w, gen)
		desiredJSON := ""
		switch {
		case w.Global:
			ds := toDaemonSet(ns, w, gen, secretFiles, pullNames)
			desiredJSON = canonicalJSON(ds)
			if err := p.putObject(ctx, nsName, "daemonset", name, desiredJSON, func() error {
				return p.putDaemonSet(ctx, nsName, ds)
			}); err != nil {
				return fmt.Errorf("k3s ensure %s: %w", ns, err)
			}
		case w.Restart == capability.RestartNever:
			pod := toOneShotPod(ns, w, gen, secretFiles, pullNames)
			desiredJSON = canonicalJSON(pod)
			if err := p.putObject(ctx, nsName, "pod", name, desiredJSON, func() error {
				return p.putPod(ctx, nsName, pod)
			}); err != nil {
				return fmt.Errorf("k3s ensure %s: %w", ns, err)
			}
		default:
			d := toDeployment(ns, w, gen, secretFiles, pullNames)
			desiredJSON = canonicalJSON(d)
			if err := p.putObject(ctx, nsName, "deployment", name, desiredJSON, func() error {
				return p.putDeployment(ctx, nsName, d)
			}); err != nil {
				return fmt.Errorf("k3s ensure %s: %w", ns, err)
			}
		}
		_ = desiredJSON

		// Addressing → Service（平台 DNS 名 = Service 名；多 Workload 同名
		// 池级声明幂等——后写覆盖同形）。无声明端口的工作负载翻译为 headless
		// Service（toService 内裁决——k8s 零端口 Service 仅 headless 合法，
		// 名解析语义保持）。
		for _, a := range w.Addressing {
			svc := toService(ns, a.Name, addressingLabelKey(a.Name), w)
			svcJSON := canonicalJSON(svc)
			if err := p.putObject(ctx, nsName, "service", svc.Name, svcJSON, func() error {
				return p.putService(ctx, nsName, svc)
			}); err != nil {
				return fmt.Errorf("k3s ensure %s: %w", ns, err)
			}
			wantServices[svc.Name] = true
		}
	}

	// 域内收敛：期望集之外的 fleetly 管辖对象移除（workload 载体 + Service；
	// Secret/PVC 是材料与数据面，删除走各自生命周期，不随域收敛）。
	for _, obj := range existing {
		if _, ok := desired[obj.name]; !ok {
			if err := p.deleteDomainObject(ctx, nsName, obj); err != nil {
				return fmt.Errorf("k3s ensure %s: remove stale %s: %w", ns, obj.name, err)
			}
			p.forgetLastIssued(obj.name)
		}
	}
	svcSel := labels.Set(serviceLabels(ns)).AsSelector()
	svcs, err := p.cli.CoreV1().Services(nsName).List(ctx, metav1.ListOptions{LabelSelector: svcSel.String()})
	if err != nil {
		return fmt.Errorf("k3s ensure %s: list services: %w", ns, err)
	}
	for _, svc := range svcs.Items {
		if !wantServices[svc.Name] {
			if err := p.cli.CoreV1().Services(nsName).Delete(ctx, svc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("k3s ensure %s: remove stale service %s: %w", ns, svc.Name, err)
			}
			p.forgetLastIssued("service/" + svc.Name)
		}
	}
	// Drift 对照账本落账（Watch 的 gen 对照信号面）。
	for _, w := range ws {
		p.recordIssuedGen(w.ID, workloadGeneration(w, gen))
	}
	return nil
}

// putObject 是 create-or-update 的账本门：no-op 账本命中即短路（受管域
// 每拍重放，服务端 defaulted 字段漂移会让深比对恒不等——lastIssued 账本
// 是主闸，深比对兜住账本作废后的第一拍；swarm C19-1 同款双闸）。
func (p *Provider) putObject(ctx context.Context, nsName, kind, name, desiredJSON string, write func() error) error {
	if desiredJSON != "" && desiredJSON == p.lastIssuedOf(kind+"/"+name) {
		return nil
	}
	if err := write(); err != nil {
		return err
	}
	p.recordLastIssued(kind+"/"+name, desiredJSON)
	return nil
}

// putDeployment 以 create-or-update 落 Deployment（冲突 = spec 不等即
// 全量替换更新——Generation 语义由平台掌管）。update 走冲突重试：status
// 子资源的控制器写者会抬 resourceVersion，Get→Update 窗口内被写即 409
// （e2e 实证 rollback 重放拍撞 deployment controller 的 status 写）。
func (p *Provider) putDeployment(ctx context.Context, nsName string, d *appsv1.Deployment) error {
	_, err := p.cli.AppsV1().Deployments(nsName).Create(ctx, d, metav1.CreateOptions{})
	if err == nil || !apierrors.IsAlreadyExists(err) {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur, err := p.cli.AppsV1().Deployments(nsName).Get(ctx, d.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		d.ResourceVersion = cur.ResourceVersion
		_, err = p.cli.AppsV1().Deployments(nsName).Update(ctx, d, metav1.UpdateOptions{})
		return err
	})
}

func (p *Provider) putDaemonSet(ctx context.Context, nsName string, ds *appsv1.DaemonSet) error {
	_, err := p.cli.AppsV1().DaemonSets(nsName).Create(ctx, ds, metav1.CreateOptions{})
	if err == nil || !apierrors.IsAlreadyExists(err) {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur, err := p.cli.AppsV1().DaemonSets(nsName).Get(ctx, ds.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		ds.ResourceVersion = cur.ResourceVersion
		_, err = p.cli.AppsV1().DaemonSets(nsName).Update(ctx, ds, metav1.UpdateOptions{})
		return err
	})
}

func (p *Provider) putPod(ctx context.Context, nsName string, pod *corev1.Pod) error {
	_, err := p.cli.CoreV1().Pods(nsName).Create(ctx, pod, metav1.CreateOptions{})
	if err == nil || !apierrors.IsAlreadyExists(err) {
		return err
	}
	// one-shot Pod 已存在：期望同 ID 即幂等（重放不重建——Run 语义）。
	return nil
}

func (p *Provider) putService(ctx context.Context, nsName string, svc *corev1.Service) error {
	_, err := p.cli.CoreV1().Services(nsName).Create(ctx, svc, metav1.CreateOptions{})
	if err == nil || !apierrors.IsAlreadyExists(err) {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cur, err := p.cli.CoreV1().Services(nsName).Get(ctx, svc.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		svc.ResourceVersion = cur.ResourceVersion
		svc.Spec.ClusterIP = cur.Spec.ClusterIP // Service immutability：ClusterIP 由服务端持有
		_, err = p.cli.CoreV1().Services(nsName).Update(ctx, svc, metav1.UpdateOptions{})
		return err
	})
}

// domainObject 是域内对象的轻量枚举（收敛对照面）。
type domainObject struct {
	kind string // deployment|daemonset|pod
	name string
}

// listDomainObjects 列出隔离域内 fleetly 管辖的 workload 载体对象。
func (p *Provider) listDomainObjects(ctx context.Context, ns capability.NamespaceRef) ([]domainObject, error) {
	nsName := namespaceName(ns)
	sel := labels.Set(nsSelector(ns)).AsSelector()
	var out []domainObject
	deps, err := p.cli.AppsV1().Deployments(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, err
	}
	for _, d := range deps.Items {
		out = append(out, domainObject{kind: "deployment", name: d.Name})
	}
	dss, err := p.cli.AppsV1().DaemonSets(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, err
	}
	for _, d := range dss.Items {
		out = append(out, domainObject{kind: "daemonset", name: d.Name})
	}
	pods, err := p.cli.CoreV1().Pods(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return nil, err
	}
	for _, pod := range pods.Items {
		// Deployment/DaemonSet 管理的 pod 由 owner 收敛，不独立枚举（避免
		// 双重删除）；只枚举无 controller owner 的独立 pod（one-shot Run）。
		if ownerIsController(pod.OwnerReferences) {
			continue
		}
		out = append(out, domainObject{kind: "pod", name: pod.Name})
	}
	return out, nil
}

// ownerIsController 报告是否挂在任一 controller owner 下。
func ownerIsController(refs []metav1.OwnerReference) bool {
	for _, r := range refs {
		if r.Controller != nil && *r.Controller {
			return true
		}
	}
	return false
}

// deleteDomainObject 删除域内 stale 载体对象。
func (p *Provider) deleteDomainObject(ctx context.Context, nsName string, obj domainObject) error {
	var err error
	switch obj.kind {
	case "deployment":
		err = p.cli.AppsV1().Deployments(nsName).Delete(ctx, obj.name, metav1.DeleteOptions{})
	case "daemonset":
		err = p.cli.AppsV1().DaemonSets(nsName).Delete(ctx, obj.name, metav1.DeleteOptions{})
	case "pod":
		err = p.cli.CoreV1().Pods(nsName).Delete(ctx, obj.name, metav1.DeleteOptions{})
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

// Remove 拆除隔离域内全部载体对象（幂等；已不存在不计错）。Secret/PVC
// 是材料与数据面：数据处置是显式动作（场景 3），不随域拆除自动删除
// （swarm Remove 不动 docker volume 的对应语义）。
func (p *Provider) Remove(ctx context.Context, ns capability.NamespaceRef) error {
	nsName := namespaceName(ns)
	objs, err := p.listDomainObjects(ctx, ns)
	if err != nil {
		return fmt.Errorf("k3s remove %s: %w", ns, err)
	}
	for _, obj := range objs {
		if err := p.deleteDomainObject(ctx, nsName, obj); err != nil {
			return fmt.Errorf("k3s remove %s: %w", ns, err)
		}
		p.forgetLastIssued(obj.name)
	}
	sel := labels.Set(serviceLabels(ns)).AsSelector()
	svcs, err := p.cli.CoreV1().Services(nsName).List(ctx, metav1.ListOptions{LabelSelector: sel.String()})
	if err != nil {
		return fmt.Errorf("k3s remove %s: %w", ns, err)
	}
	for _, svc := range svcs.Items {
		if err := p.cli.CoreV1().Services(nsName).Delete(ctx, svc.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("k3s remove %s: %w", ns, err)
		}
		p.forgetLastIssued("service/" + svc.Name)
	}
	// egress policy 是域级对象，随域拆除。
	if err := p.cli.NetworkingV1().NetworkPolicies(nsName).Delete(ctx, toEgressNetpol().Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("k3s remove %s: netpol: %w", ns, err)
	}
	return nil
}

// Watch 返回全集群状态流：pod 事件 → WorkloadEvent（engine 按 ID 归属
// 过滤）+ 节点锚定（NodeJoined）。实现 = apiserver watch 流（pod）+ 周期
// 锚定扫描（节点）+ 周期全量对账兜底（watch 断流自愈）。
func (p *Provider) Watch(ctx context.Context) (<-chan capability.WorkloadEvent, error) {
	out := make(chan capability.WorkloadEvent, 64)
	go p.watchLoop(ctx, out)
	return out, nil
}

func (p *Provider) watchLoop(ctx context.Context, out chan<- capability.WorkloadEvent) {
	defer close(out)
	// 首轮锚定（DescribeCluster 前置）+ 周期扫描。
	_ = p.anchorNodes(ctx, out) // 首拍锚定失败不阻断观测（DescribeCluster 同语义，下拍重试）
	ticker := time.NewTicker(watchPollInterval)
	defer ticker.Stop()
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	podEvents := p.podWatchStream(watchCtx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = p.anchorNodes(ctx, out) // 周期锚定失败不阻断观测（下拍重试）

			p.reconcileAllPods(ctx, out)
		case ev, ok := <-podEvents:
			if !ok {
				// watch 断流：重建（退避由 client-go watch retry 语义承载）。
				time.Sleep(time.Second)
				podEvents = p.podWatchStream(watchCtx)
				continue
			}
			if ev != nil {
				p.sendEvent(ctx, out, *ev)
			}
		}
	}
}

// sendEvent 非阻塞投递（消费端停读时丢弃中间事件——全量对账兜底周期
// 重发状态，不阻塞 provider 循环）。
func (p *Provider) sendEvent(ctx context.Context, out chan<- capability.WorkloadEvent, ev capability.WorkloadEvent) {
	select {
	case out <- ev:
	case <-ctx.Done():
	default:
	}
}

// podWatchStream 建立 pod 全集群 watch 流（managed 标记过滤；断流由调用
// 方重建）。返回 nil 事件表示跳过（非 managed/无 workload 标记）。
func (p *Provider) podWatchStream(ctx context.Context) <-chan *capability.WorkloadEvent {
	out := make(chan *capability.WorkloadEvent, 64)
	go func() {
		defer close(out)
		w, err := p.cli.CoreV1().Pods("").Watch(ctx, metav1.ListOptions{
			LabelSelector: labels.Set(map[string]string{labelManaged: "true"}).String(),
		})
		if err != nil {
			return // 断流重建由调用方承载
		}
		defer w.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-w.ResultChan():
				if !ok {
					return
				}
				pod, ok := msg.Object.(*corev1.Pod)
				if !ok {
					continue
				}
				if ev := p.mapPodEvent(msg.Type, pod); ev != nil {
					select {
					case out <- ev:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return out
}

// mapPodEvent 把 pod 状态映射为 WorkloadEvent（managed 域过滤 + 实例/退出
// 码还原；one-shot 终态 Completed/Failed = ADR-0025 决策 2 语义）。
func (p *Provider) mapPodEvent(typ watch.EventType, pod *corev1.Pod) *capability.WorkloadEvent {
	lb := pod.Labels
	if lb[labelManaged] != "true" || lb[labelWorkload] == "" {
		return nil
	}
	if typ == watch.Deleted {
		// 删除事件不映射终态（终态由 Pod phase 承载——k8s Pod 对象留档；
		// 域收敛删除的载体不发终态事件，观测以全量对账为准）。
		return nil
	}
	gen, _ := parseUint(lb[labelGeneration])
	ev := &capability.WorkloadEvent{
		WorkloadID: lb[labelWorkload],
		Generation: capability.Generation(gen),
		Instance:   pod.Name,
		Node:       p.platformNodeID(pod.Spec.NodeName),
	}
	state, exitCode, reason := podWorkloadState(pod)
	ev.State = state
	ev.ExitCode = exitCode
	ev.Reason = reason
	if p.isDrifted(lb[labelWorkload], gen) {
		ev.Drift = true
	}
	return ev
}

// podWorkloadState 把 pod phase/容器状态映射为平台 WorkloadState。
func podWorkloadState(pod *corev1.Pod) (capability.WorkloadState, *int, string) {
	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		code := 0
		return capability.WorkloadCompleted, &code, ""
	case corev1.PodFailed:
		code := 1
		reason := ""
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Terminated != nil {
				code = int(cs.State.Terminated.ExitCode) //nolint:gosec // 退出码域内
				reason = cs.State.Terminated.Reason
			}
		}
		return capability.WorkloadFailed, &code, reason
	case corev1.PodRunning:
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Waiting != nil && cs.State.Waiting.Reason == "CrashLoopBackOff" {
				return capability.WorkloadDegraded, nil, cs.State.Waiting.Reason
			}
		}
		if podReady(pod) {
			return capability.WorkloadRunning, nil, ""
		}
		return capability.WorkloadDegraded, nil, "not ready"
	default: // Pending / Unknown
		return capability.WorkloadPending, nil, string(pod.Status.Phase)
	}
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// issuedGenMu 是 issuedGen 账本的互斥（结构体字段——Ensure 落账、Watch
// 对照并发面）。

// recordIssuedGen 落最近下发的 Generation（Ensure 成功拍落账——Watch 的
// Drift 对照信号面：载体 Generation 标记与最近下发值不一致即 drift）。
func (p *Provider) recordIssuedGen(workloadID string, gen uint64) {
	p.issuedGenMu.Lock()
	defer p.issuedGenMu.Unlock()
	if p.issuedGen == nil {
		p.issuedGen = map[string]uint64{}
	}
	p.issuedGen[workloadID] = gen
}

func (p *Provider) isDrifted(workloadID string, observed uint64) bool {
	p.issuedGenMu.Lock()
	defer p.issuedGenMu.Unlock()
	want, ok := p.issuedGen[workloadID]
	return ok && want != observed
} // reconcileAllPods 全量对账（watch 断流兜底 + 启动首轮快照——重启后
// 观测面立即可用，不依赖事件）。
func (p *Provider) reconcileAllPods(ctx context.Context, out chan<- capability.WorkloadEvent) {
	pods, err := p.cli.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		LabelSelector: labels.Set(map[string]string{labelManaged: "true"}).String(),
	})
	if err != nil {
		return
	}
	sort.Slice(pods.Items, func(i, j int) bool { return pods.Items[i].Name < pods.Items[j].Name })
	for i := range pods.Items {
		if ev := p.mapPodEvent(watch.Added, &pods.Items[i]); ev != nil {
			p.sendEvent(ctx, out, *ev)
		}
	}
}

// Addresses 返回隔离域的可达地址：期望集成员的 Addressing Service DNS 名
// （<name>.<ns>.svc）× 声明端口——期望集端口注入语义（swarm Addresses 同
// 源：未匹配到期望集成员的不计入）。
func (p *Provider) Addresses(ctx context.Context, ns capability.NamespaceRef, expected []capability.Workload) ([]capability.Endpoint, error) {
	nsName := namespaceName(ns)
	var endpoints []capability.Endpoint
	for _, w := range expected {
		for _, a := range w.Addressing {
			host := serviceCarrierName(a.Name) + "." + nsName + ".svc"
			for _, port := range w.Ports {
				// Addr 是裸主机名、端口在 Port 字段分立（swarm 契约同构——
				// 消费面拼 scheme://Addr:Port；把端口折进 Addr 会产出
				// "host:80:80" 双端口 URL，traefik precheck 即拒——e2e
				// route 段实证）。
				endpoints = append(endpoints, capability.Endpoint{
					Addr:    host,
					Process: w.Process,
					Port:    port.Port,
				})
			}
		}
	}
	return endpoints, nil
}

// DescribeCluster 返回集群观测视图（节点缓存；权威归属判定永远查平台表）。
func (p *Provider) DescribeCluster(ctx context.Context) (capability.ClusterView, error) {
	if err := p.anchorNodes(ctx, nil); err != nil {
		return capability.ClusterView{}, err
	}
	nodes, err := p.cli.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return capability.ClusterView{}, fmt.Errorf("k3s describe cluster: %w", err)
	}
	view := capability.ClusterView{}
	for i := range nodes.Items {
		node := &nodes.Items[i]
		ready := false
		addr := ""
		for _, c := range node.Status.Conditions {
			if c.Type == "Ready" {
				ready = c.Status == corev1.ConditionTrue
			}
		}
		for _, a := range node.Status.Addresses {
			if a.Type == corev1.NodeInternalIP && addr == "" {
				addr = a.Address
			}
		}
		role := "worker"
		if _, cp := node.Labels["node-role.kubernetes.io/control-plane"]; cp {
			role = "manager"
		}
		view.Nodes = append(view.Nodes, capability.NodeView{
			NodeID:    node.Labels[labelNodeID],
			CarrierID: node.Name,
			Hostname:  node.Name,
			Addr:      addr,
			Role:      role,
			Available: ready,
			Labels:    node.Labels,
		})
	}
	return view, nil
}

// Enrollment 生成节点加入材料（k3s agent 命令；节点零平台安装物——k8s
// 节点 kubelet 由 k3s agent 自带，k3s 对 k8s 节点 = docker 对 swarm 节点
// 的运行时前提）。rotate 是 k3s 侧未支持面：node token 轮换需 server 重启
// 介入，诚实失败（C3 泄漏处置走 runbook 的 server 面轮换序）。AgentCommand
// 恒空 = 无节点侧代理面（exec 集中形态经 apiserver，ADR-0053 决策 1——
// worker 节点零平台代理物，EnrollKit 契约"空 = Provider 无代理面"同判）。
func (p *Provider) Enrollment(ctx context.Context, rotate bool, o capability.EnrollmentOptions) (capability.EnrollKit, error) {
	if rotate {
		return capability.EnrollKit{}, fmt.Errorf("k3s enrollment: token rotation is not supported by this provider yet (rotate via k3s server restart; see runbook)")
	}
	token, err := p.readNodeToken()
	if err != nil {
		return capability.EnrollKit{}, fmt.Errorf("k3s enrollment: %w", err)
	}
	return capability.EnrollKit{
		Command:   fmt.Sprintf("k3s agent --server %s --token %s", p.advertiseServerURL(ctx), token),
		ExpiresAt: time.Time{}, // k3s node token 无时效字段（观测面空）
	}, nil
}

// advertiseServerURL 解析 worker 可达的控制面地址：kubeconfig 的 server
// 常是 127.0.0.1 形态（k3s 发行缺省），worker 执行 `--server 127.0.0.1`
// 必失败——列 Node 取 control-plane 角色节点的 InternalIP（端口沿用
// kubeconfig server 的端口；k3s server 证书默认含节点 IP SAN）。解析
// 失败回退 kubeconfig 原文（单节点同机形态语义不变）。
func (p *Provider) advertiseServerURL(ctx context.Context) string {
	u, err := url.Parse(p.apiServer)
	if err != nil {
		return p.apiServer
	}
	port := u.Port()
	if port == "" {
		port = "6443"
	}
	nodes, err := p.cli.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return p.apiServer
	}
	fallback := ""
	for i := range nodes.Items {
		node := &nodes.Items[i]
		addr := ""
		for _, a := range node.Status.Addresses {
			if a.Type == corev1.NodeInternalIP && addr == "" {
				addr = a.Address
			}
		}
		if addr == "" {
			continue
		}
		if _, cp := node.Labels["node-role.kubernetes.io/control-plane"]; cp {
			return "https://" + net.JoinHostPort(addr, port)
		}
		if fallback == "" {
			fallback = addr
		}
	}
	if fallback != "" {
		return "https://" + net.JoinHostPort(fallback, port)
	}
	return p.apiServer
}

// canonicalJSON 把对象归一为可比较的 JSON 字节串（map 键排序由
// encoding/json 内建；status/resourceVersion 等服务端字段在 marshal spec
// 子集时天然剥离——desired 对象不含这些字段）。
func canonicalJSON(obj interface{}) string {
	raw, err := json.Marshal(obj)
	if err != nil {
		return ""
	}
	return string(raw)
}

func parseUint(s string) (uint64, error) {
	return strconv.ParseUint(s, 10, 64)
}
