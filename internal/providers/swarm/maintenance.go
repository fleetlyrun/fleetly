package swarm

// 网络维护子面（ADR-0046 网络重建动词）：平台中介的载体网络重建原语。
// detach/attach 是 service update 的网络附件面改动（全量 spec 替换语义，
// 与 Ensure 同通道的 CAS 版本更新）——平台外改动后必须作废 no-op 断路器
// 账本条目，让下一拍 Ensure 重新走 serviceSpecEqual 等价门（等价即跳过并
// 回填账本，不产生额外滚动；不等价才是真 drift 自愈面）。
//
// re-attach 的附件形状（同域附件的 Aliases——ADR-0034 进程名别名/双级
// DNS）由 detach 时的原样快照承载：attach 只换 Target（旧网络 ID → 复建
// 后新 ID），其余逐字节还原——与 fresh Ensure 的 resolveNetworkTargets
// 产物一致，重建后下一拍 Ensure 等价跳过。

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/client"

	"github.com/fleetlyrun/fleetly/internal/capability"
)

// networkDrainRetry 是 RemoveNetwork 对 in-use 类错误的退避节拍（detach
// 触发的任务替换需要时间排水——stop-first + StopGrace 量级由调用方 ctx
// 上界收口，这里的节拍只是轮询密度）。
const networkDrainRetry = 2 * time.Second

// InspectNetwork 返回载体网络快照（不存在时 Exists=false）。附着枚举走
// 全量 ServiceList + spec 网络目标比对（docker 对 service 列表无 network
// 过滤器；外来服务无平台标签也在枚举面——归属裁决在引擎侧）。
func (p *Provider) InspectNetwork(ctx context.Context, ns capability.NamespaceRef, network string) (capability.NetworkCarrierState, error) {
	name := carrierNetworkName(ns, network)
	res, err := p.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err != nil {
		if isNotFound(err) {
			return capability.NetworkCarrierState{}, nil
		}
		// Q-20 同款分诊：inspect 失败 ≠ 不存在——权限/连接类错误上抛带
		// 原因，不伪装缺失。
		return capability.NetworkCarrierState{}, fmt.Errorf("swarm inspect network %s: %w", name, err)
	}
	state := capability.NetworkCarrierState{
		Exists:     true,
		Attachable: res.Network.Attachable,
		Managed:    res.Network.Labels[labelNetManaged] == "true",
	}
	services, err := p.cli.ServiceList(ctx, client.ServiceListOptions{})
	if err != nil {
		return capability.NetworkCarrierState{}, fmt.Errorf("swarm inspect network %s: list services: %w", name, err)
	}
	for i := range services.Items {
		svc := services.Items[i]
		if !serviceAttachedTo(svc, name, res.Network.ID) {
			continue
		}
		att := capability.NetworkAttachment{Carrier: svc.Spec.Name}
		if labels := svc.Spec.Labels; labels[labelManaged] == "true" {
			att.Workload = labels[labelWorkload]
			// 域轴还原（标记值是 sanitizeNamePart 产物——小写化的平台 ID，
			// 引擎侧比对用大小写折叠）。
			att.Domain = capability.NamespaceRef{
				Team:     labels[labelTeam],
				Project:  labels[labelProject],
				App:      labels[labelApp],
				Task:     labels[labelTask],
				Database: labels[labelDatabase],
			}
		}
		state.Attachments = append(state.Attachments, att)
	}
	sort.Slice(state.Attachments, func(i, j int) bool {
		return state.Attachments[i].Carrier < state.Attachments[j].Carrier
	})
	return state, nil
}

// serviceAttachedTo 报告服务 spec 是否附着目标网络（ID 或名双形态——
// 平台 Ensure 先行解析发 ID，手工/边缘形态可能持名）。
func serviceAttachedTo(svc swarm.Service, name, id string) bool {
	for _, att := range svc.Spec.TaskTemplate.Networks {
		if att.Target == id || att.Target == name {
			return true
		}
	}
	return false
}

// DetachNetwork 从载体上摘除网络附件（幂等：本就无该附件即 no-op）。
// 摘下的附件形状快照进 rebuildSaved（attach 的还原锚）。
func (p *Provider) DetachNetwork(ctx context.Context, ns capability.NamespaceRef, network, carrier string) error {
	netName := carrierNetworkName(ns, network)
	inspect, err := p.cli.ServiceInspect(ctx, carrier, client.ServiceInspectOptions{})
	if err != nil {
		if isNotFound(err) {
			return nil // 载体已消失：摘除对象不存在即完成（幂等语义同 Remove）
		}
		return fmt.Errorf("swarm detach network %s: inspect %s: %w", netName, carrier, err)
	}
	svc := inspect.Service
	netID := ""
	if res, nerr := p.cli.NetworkInspect(ctx, netName, client.NetworkInspectOptions{}); nerr == nil {
		netID = res.Network.ID
	} else if !isNotFound(nerr) {
		return fmt.Errorf("swarm detach network %s: inspect network: %w", netName, nerr)
	}
	kept := make([]swarm.NetworkAttachmentConfig, 0, len(svc.Spec.TaskTemplate.Networks))
	var saved *swarm.NetworkAttachmentConfig
	for _, att := range svc.Spec.TaskTemplate.Networks {
		if att.Target == netName || (netID != "" && att.Target == netID) {
			cp := att
			saved = &cp
			continue
		}
		kept = append(kept, att)
	}
	if saved == nil {
		return nil // 本就没有该附件（重跑收敛的中间态）
	}
	spec := svc.Spec
	spec.TaskTemplate.Networks = kept
	if uerr := p.updateServiceCAS(ctx, nsFromCarrier(svc), spec, svc, ""); uerr != nil {
		return fmt.Errorf("swarm detach network %s: %w", netName, uerr)
	}
	p.forgetLastIssued(spec.Name) // 平台外改动载体：账本条目作废（Ensure 重新走等价门）
	p.rememberDetached(netName, spec.Name, *saved)
	return nil
}

// AttachNetwork 把网络附件加回载体（幂等：已附着即 no-op）。附件形状从
// detach 快照还原（Target 换成复建后的新网络 ID）；无快照时以裸附件
// 兜底（引擎只对它 detach 过的载体调 attach，此形态是防御面）。
func (p *Provider) AttachNetwork(ctx context.Context, ns capability.NamespaceRef, network, carrier string) error {
	netName := carrierNetworkName(ns, network)
	res, err := p.cli.NetworkInspect(ctx, netName, client.NetworkInspectOptions{})
	if err != nil {
		return fmt.Errorf("swarm attach network %s: inspect network: %w", netName, err)
	}
	inspect, err := p.cli.ServiceInspect(ctx, carrier, client.ServiceInspectOptions{})
	if err != nil {
		if isNotFound(err) {
			return fmt.Errorf("swarm attach network %s: carrier %s no longer exists", netName, carrier)
		}
		return fmt.Errorf("swarm attach network %s: inspect %s: %w", netName, carrier, err)
	}
	svc := inspect.Service
	if serviceAttachedTo(svc, netName, res.Network.ID) {
		return nil // 已附着（重跑收敛的中间态）
	}
	att := swarm.NetworkAttachmentConfig{Target: res.Network.ID}
	if saved, ok := p.takeDetached(netName, svc.Spec.Name); ok {
		att = saved
		att.Target = res.Network.ID
	}
	spec := svc.Spec
	spec.TaskTemplate.Networks = append(spec.TaskTemplate.Networks, att)
	if uerr := p.updateServiceCAS(ctx, nsFromCarrier(svc), spec, svc, ""); uerr != nil {
		return fmt.Errorf("swarm attach network %s: %w", netName, uerr)
	}
	p.forgetLastIssued(spec.Name)
	return nil
}

// RemoveNetwork 删除载体网络（幂等）。两段语义：
//   - in-use 类错误（附着端点未排空——detach 触发的任务替换仍在滚动）
//     按退避节拍重试至成功；
//   - rm 返回成功≠已消失（swarm overlay 的删除是异步的：端点排空后网络
//     才真退役——dind e2e 实证：rm 成功后立即 inspect 仍见旧网络，复建
//     create-or-get 会 get 半边复用残留载体）。删除后轮询 inspect 直至
//     NotFound 才返回，使复建步只可能 create 全新载体。
//
// 其余错误如实上抛（含一次性容器附着的兜底面：utility 容器不在服务枚举
// 面，rm 的 in-use 失败即其痕迹）。全程受调用方 ctx 界约束。
func (p *Provider) RemoveNetwork(ctx context.Context, ns capability.NamespaceRef, network string) error {
	name := carrierNetworkName(ns, network)
	for {
		_, err := p.cli.NetworkRemove(ctx, name, client.NetworkRemoveOptions{})
		if err != nil && !isNotFound(err) {
			if !isNetworkInUse(err) {
				return fmt.Errorf("swarm remove network %s: %w", name, err)
			}
		} else {
			// rm 成功（或本就不在）：等异步退役落地（NotFound）。
			if err := p.awaitNetworkGone(ctx, name); err != nil {
				return err
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("swarm remove network %s: attached endpoints did not drain before the deadline: %w", name, ctx.Err())
		case <-time.After(networkDrainRetry):
		}
	}
}

// awaitNetworkGone 轮询网络消失（swarm overlay 异步退役的收口半边）。
func (p *Provider) awaitNetworkGone(ctx context.Context, name string) error {
	for {
		_, err := p.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("swarm remove network %s: confirm removal: %w", name, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("swarm remove network %s: the carrier network did not retire before the deadline: %w", name, ctx.Err())
		case <-time.After(networkDrainRetry):
		}
	}
}

// EnsureNetwork 按 ensureNetworks create 半边同源复建（network.go
// ensureOneNetwork 单源）；已存在即复用。
func (p *Provider) EnsureNetwork(ctx context.Context, ns capability.NamespaceRef, network string) error {
	if err := p.ensureOneNetwork(ctx, ns, network); err != nil {
		return fmt.Errorf("swarm ensure network carrier: %w", err)
	}
	return nil
}

// isNetworkInUse 识别"网络仍被附着"类错误（docker 文案：active endpoints
// / in use 两族——isUpdateOutOfSequence 同款稳定文案匹配，daemon 升级改写
// 文案时退化为单发失败如实上抛，重试面消失但语义不破）。
func isNetworkInUse(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "active endpoints") || strings.Contains(msg, "in use")
}

// nsFromCarrier 由载体标记还原隔离域（updateServiceCAS 的日志锚参数）。
// 标记值是 sanitizeNamePart 产物：空轴会物化为 "fleetly"（namePrefix），
// 还原时剥掉。
func nsFromCarrier(svc swarm.Service) capability.NamespaceRef {
	labels := svc.Spec.Labels
	strip := func(v string) string {
		if v == namePrefix {
			return ""
		}
		return v
	}
	return capability.NamespaceRef{
		Team:     strip(labels[labelTeam]),
		Project:  strip(labels[labelProject]),
		App:      strip(labels[labelApp]),
		Task:     strip(labels[labelTask]),
		Database: strip(labels[labelDatabase]),
	}
}

// ---- detach 附件形状快照（attach 的还原锚；进程内存态，重启即冷——
// 冷却后 attach 退化为裸附件兜底，仅影响防御面） ----

// rememberDetached 记录一次摘除的附件形状（键 = 网络|载体）。
func (p *Provider) rememberDetached(netName, carrier string, att swarm.NetworkAttachmentConfig) {
	p.savedMu.Lock()
	defer p.savedMu.Unlock()
	if p.rebuildSaved == nil {
		p.rebuildSaved = map[string]swarm.NetworkAttachmentConfig{}
	}
	p.rebuildSaved[netName+"|"+carrier] = att
}

// takeDetached 取出并清除一次摘除快照（单次消费）。
func (p *Provider) takeDetached(netName, carrier string) (swarm.NetworkAttachmentConfig, bool) {
	p.savedMu.Lock()
	defer p.savedMu.Unlock()
	key := netName + "|" + carrier
	att, ok := p.rebuildSaved[key]
	if ok {
		delete(p.rebuildSaved, key)
	}
	return att, ok
}
