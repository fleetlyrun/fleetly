package engine

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/capability"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/build"
	"github.com/fleetlyrun/fleetly/internal/state/deployment"
	"github.com/fleetlyrun/fleetly/internal/state/node"
)

// loadSpec 反序列化 Revision 冻结体（protojson blob）。ctx 透传取消链
// （Q-6：驱动/回放路径的关停可取消，不再内嵌 Background 脱链）。
func (e *Engine) loadSpec(ctx context.Context, revID string) (*specv1.AppSpec, error) {
	rev, err := e.revisions.Get(ctx, e.db.Runner(), revID)
	if err != nil {
		return nil, err
	}
	return unmarshalSpec(rev.Spec)
}

// unmarshalSpec 反序列化 AppSpec 冻结体（Revision 行与受理预检共用）。
func unmarshalSpec(blob []byte) (*specv1.AppSpec, error) {
	spec := &specv1.AppSpec{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(blob, spec); err != nil {
		return nil, err
	}
	return spec, nil
}

// appTeam 解析 App 归属（团队解析单一真源，N0.1 P2-11 / ADR-0028 接实：
// 从 Project 行实取 team_id，不再回退字面量）。
func (e *Engine) appTeam(ctx context.Context, appID string) (string, *app.App, error) {
	a, err := e.apps.Get(ctx, e.db.Runner(), appID)
	if err != nil {
		return "", nil, err
	}
	team, err := e.projectTeam(ctx, a.ProjectID)
	if err != nil {
		return "", nil, err
	}
	return team, a, nil
}

// buildDigests 解析 Deployment 目标 Revision 的构建产物（from_build 进程
// → 下发镜像引用映射；无成功构建返回 nil → 投影期得到精确错误）。Build 是
// Revision 级单产物：全部 from_build 进程共用同一 digest。引用是 digest
// 形态完整引用（<registry>/<app>@sha256:...——地址是平台级配置不进冻结体，
// 投影期组合，ADR-0019 附录 B.4）。
func (e *Engine) buildDigests(ctx context.Context, d *deployment.Deployment) (map[string]string, error) {
	spec, err := e.loadSpec(ctx, d.ToRevision)
	if err != nil {
		return nil, err
	}
	var fromBuild []string
	for _, p := range spec.GetProcesses() {
		if p.GetFromBuild() != "" {
			fromBuild = append(fromBuild, p.GetName())
		}
	}
	if len(fromBuild) == 0 {
		return nil, nil
	}
	// 端点先解析（缺 Registry 的精确失败优先于"无产物"——诊断顺序：先
	// 平台缺件，再产物缺席）。
	endpoint, err := e.registryEndpoint(ctx)
	if err != nil {
		return nil, err
	}
	builds, err := e.builds.ListByRevision(ctx, e.db.Runner(), d.ToRevision)
	if err != nil {
		return nil, err
	}
	for _, b := range builds {
		if b.State == build.StateSucceeded && b.Digest != "" {
			out := make(map[string]string, len(fromBuild))
			ref := LocalImageDigestRef(endpoint.Addr, d.AppID, b.Digest)
			for _, name := range fromBuild {
				out[name] = ref
			}
			return out, nil
		}
	}
	return nil, nil
}

// recordEnsured 记录 Ensure 事实：归属缓存 + 各 Workload 的 Generation 与
// 投影 spec（就绪门集合界定——被移除 process 的旧 workload 不再计入）+
// App 级最近 Generation（Drift 对照锚）。进程内缓存，重启后由幂等重放的
// Ensure 或启动基线重放重建（ADR-0022）。
func (e *Engine) recordEnsured(d *deployment.Deployment, gen uint64, ws []capability.Workload) {
	e.expect.mu.Lock()
	e.expect.expected[d.AppID] = gen
	e.expect.mu.Unlock()
	e.obs.mu.Lock()
	for _, w := range ws {
		e.obs.workloadApp[w.ID] = d.AppID
		e.obs.ensuredGen[w.ID] = gen
		e.obs.ensuredSpec[w.ID] = w
	}
	e.obs.mu.Unlock()
}

// releaseReady 是 L1 健康门：本 Deployment 下发的全部 Workload（gen 匹配
// 的 Ensure 集合）均观测到 running。内存 Ensure 缓存为空（重启）时假阴性
// → 驱动重新幂等 Ensure 后重建，语义自洽。
func (e *Engine) releaseReady(d *deployment.Deployment) bool {
	return e.releaseReadyGen(d, d.Generation)
}

func (e *Engine) releaseReadyGen(d *deployment.Deployment, gen uint64) bool {
	e.obs.mu.RLock()
	defer e.obs.mu.RUnlock()
	count := 0
	for wid, owner := range e.obs.workloadApp {
		if owner != d.AppID || e.obs.ensuredGen[wid] != gen {
			continue
		}
		count++
		ev, seen := e.obs.observations[wid]
		// 就绪门：全部成员必须已观测且 running@gen（未观测 = 未就绪，
		// 与看门狗语义相反——后者未观测不咬合）。
		if !seen || ev.State != capability.WorkloadRunning || uint64(ev.Generation) != gen {
			return false
		}
	}
	return count > 0
}

// watchdogBite 是 L2 看门狗：当前 Generation 观测到 stopped → 咬合描述
// （空串 = 未咬合）。旧代事件不咬合：观测槽按 Workload ID last-write-wins，
// 滚动更新期旧 task 的 stopped@旧代 迟到事件会覆盖 running@当前代——
// 咬合只认当前代（假咬合会让滚动更新秒败进回滚，F0.12 回归钉死）。
func (e *Engine) watchdogBite(d *deployment.Deployment) string {
	gen := d.Generation
	bite, _ := e.scanGeneration(d.AppID, gen, func(ev capability.WorkloadEvent) string {
		if ev.State == capability.WorkloadStopped && uint64(ev.Generation) == gen {
			return fmt.Sprintf("workload %s stopped (gen %d): %s", ev.WorkloadID, ev.Generation, ev.Message)
		}
		return ""
	})
	return bite
}

// scanGeneration 遍历某 App 在指定 Generation 下发的 Workload 集
// （ensuredGen 界定：只有 gen 匹配的 Ensure 记录计入），返回（咬合描述,
// 集合大小）。pred 返回非空即咬合（短路）；未观测的 Workload 不咬合
// （等待/超时路径处理）。集合为空（重启后缓存未重建）由调用方解释。
func (e *Engine) scanGeneration(appID string, gen uint64, pred func(capability.WorkloadEvent) string) (string, int) {
	e.obs.mu.RLock()
	defer e.obs.mu.RUnlock()
	count := 0
	for wid, owner := range e.obs.workloadApp {
		if owner != appID || e.obs.ensuredGen[wid] != gen {
			continue
		}
		count++
		ev, seen := e.obs.observations[wid]
		if !seen {
			continue
		}
		if msg := pred(ev); msg != "" {
			return msg, count
		}
	}
	return "", count
}

// consumeWatch 消费 Runtime Watch 流（连接断开自动重连；provider 内部
// 已自愈事件流，此处兜底 ctx 生命周期的重连）。
func (e *Engine) consumeWatch(ctx context.Context) {
	for ctx.Err() == nil {
		ch, err := e.runtime.Watch(ctx)
		if err != nil {
			e.log.Error("engine watch: open", "err", err)
			if !sleepCtx(ctx, time.Second) {
				return
			}
			continue
		}
		e.drainWatch(ctx, ch)
	}
}

func (e *Engine) drainWatch(ctx context.Context, ch <-chan capability.WorkloadEvent) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return // 流关闭 → 外层重连
			}
			e.handleObservation(ctx, ev)
		}
	}
}

// handleObservation：观测缓存刷新 + 节点锚定落库 + drift 检测 + Kick。
// C3 观测 verdict owner（批 0.5 裁决）：Run 观测先经 workloadRun 归属路由
// 进 Run 状态机（taskobs.go），App 观测走部署面——两轨裁决权分立。
func (e *Engine) handleObservation(ctx context.Context, ev capability.WorkloadEvent) {
	if ev.NodeJoined != nil {
		e.handleNodeJoined(ctx, ev.NodeJoined)
		return
	}
	if ev.WorkloadID == "" {
		return
	}
	if _, runOwned := e.runOwner(ev.WorkloadID); runOwned {
		e.handleRunObservation(ctx, ev.WorkloadID, ev)
		return
	}
	e.obs.mu.Lock()
	e.obs.observations[ev.WorkloadID] = ev
	e.obs.mu.Unlock()
	if ev.State != capability.WorkloadStopped {
		e.clearStoppedSig(ev.WorkloadID) // 稳态 stopped 去抖解除（ADR-0022）
	}
	e.detectDrift(ctx, ev)
	e.loop.Kick()
}

// runOwner 返回 Run 观测归属（workloadID → taskID；Task 域缓存组面）。
func (e *Engine) runOwner(workloadID string) (string, bool) {
	e.task.obsMu.RLock()
	defer e.task.obsMu.RUnlock()
	taskID, ok := e.task.workloadRun[workloadID]
	return taskID, ok
}

// handleNodeJoined：节点观测缓存 upsert（nodes 表非权威）+ node.joined
// 事件（仅铸造时发——引擎重启的重复锚定扫描不重复发事件）。
func (e *Engine) handleNodeJoined(ctx context.Context, nj *capability.NodeJoined) {
	now := state.FormatTime(e.clock.Now())
	err := e.db.Tx(ctx, func(tx *sql.Tx) error {
		return e.nodes.Upsert(ctx, tx, &node.Node{
			PlatformID: nj.NodeID, CarrierID: nj.CarrierID, Available: true,
			FirstSeenAt: now, LastSeenAt: now,
		})
	})
	if err != nil {
		e.log.Error("engine watch: node upsert", "node", nj.NodeID, "err", err)
	}
	if !nj.Minted {
		return
	}
	_, err = e.outbox.Append(ctx, e.db.Runner(), eventNodeJoined, "node", nj.NodeID,
		nodeJoinedPayloadJSON(nj.NodeID, nj.CarrierID, nj.Minted))
	if err != nil {
		e.log.Error("engine watch: node.joined event", "node", nj.NodeID, "err", err)
	}
}

// detectDrift：对照最近 Ensure 的 Generation（架构 §5 Drift 判定；检测
// 默认开、收敛默认 opt-in——只发事件不动状态，ADR-0005）。去抖：同一
// (workload, expected, observed) 签名不重复发；观测回归 expected 即清
// 签名（下次偏离可再发）。
func (e *Engine) detectDrift(ctx context.Context, ev capability.WorkloadEvent) {
	e.obs.mu.RLock()
	appID, owned := e.obs.workloadApp[ev.WorkloadID]
	e.obs.mu.RUnlock()
	if !owned {
		return // 非平台管辖载体：观测缓存已登记，事件不落（孤儿面后续批）
	}
	e.expect.mu.Lock()
	expected := e.expect.expected[appID]
	e.expect.mu.Unlock()

	if expected != 0 && uint64(ev.Generation) == expected && !ev.Drift {
		e.drift.mu.Lock()
		delete(e.drift.sig, ev.WorkloadID)
		e.drift.mu.Unlock()
		return
	}
	if expected == 0 && !ev.Drift {
		return
	}

	sig := fmt.Sprintf("%d|%d|%v", expected, uint64(ev.Generation), ev.Drift)
	e.drift.mu.Lock()
	if e.drift.sig[ev.WorkloadID] == sig {
		e.drift.mu.Unlock()
		return
	}
	e.drift.sig[ev.WorkloadID] = sig
	e.drift.mu.Unlock()

	_, err := e.outbox.Append(ctx, e.db.Runner(), eventWorkloadDrift, "workload", ev.WorkloadID,
		driftEventPayloadJSON(ev, appID, expected))
	if err != nil {
		e.log.Error("engine watch: drift event", "workload", ev.WorkloadID, "err", err)
	}
}

// parseDeadline 解析落库截止（空串/坏值 = nil）。
func parseDeadline(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
