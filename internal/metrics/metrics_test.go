package metrics

// duty 收敛流程的 hermetic 单测（fake dockerPort——victorialogs duty 测试
// 同型）：opt-in 缺省零常驻（D-W5-2——未显式设置 = 无所欠）、mode=on 三件
// 收敛（服务 + 抓取 config + 卷）、幂等稳态、漂移更新、切回 unset 三件
// 移除 + 卷保留 + config 清场、差分事件、负面（swarm 未就绪、平台 ID 未
// 铸）、健康检查面、status 投影、nodes_reporting 计数接线面。

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/swarm"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// fakeDocker 是 dockerPort 的假件（收敛账面记录 + 幂等填充）。
type fakeDocker struct {
	mu sync.Mutex

	swarmActive bool

	services map[string]ServiceState
	created  []string
	updated  []string
	removed  []string

	volumesEnsured []string

	configs        map[string]swarm.ConfigSpec
	configEnsured  []string
	configsRemoved []string

	inspectErr error
}

func newFakeDocker() *fakeDocker {
	return &fakeDocker{
		services: map[string]ServiceState{},
		configs:  map[string]swarm.ConfigSpec{},
	}
}

func (f *fakeDocker) Info(_ context.Context) (bool, error) { return f.swarmActive, nil }

func (f *fakeDocker) ServiceInspect(_ context.Context, name string) (ServiceState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inspectErr != nil {
		return ServiceState{}, f.inspectErr
	}
	return f.services[name], nil
}

func (f *fakeDocker) ServiceCreate(_ context.Context, spec swarm.ServiceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	name := spec.Name
	f.created = append(f.created, name)
	cur := ServiceState{Exists: true, Version: 1}
	cur.fillFrom(spec)
	f.services[name] = cur
	return nil
}

func (f *fakeDocker) ServiceUpdate(_ context.Context, name string, _ uint64, spec swarm.ServiceSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updated = append(f.updated, name)
	cur := f.services[name]
	cur.Version++
	cur.fillFrom(spec)
	f.services[name] = cur
	return nil
}

func (f *fakeDocker) ServiceRemove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	delete(f.services, name)
	return nil
}

func (f *fakeDocker) VolumeEnsure(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volumesEnsured = append(f.volumesEnsured, name)
	return nil
}

func (f *fakeDocker) ConfigEnsure(_ context.Context, name string, spec swarm.ConfigSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configEnsured = append(f.configEnsured, name)
	f.configs[name] = spec
	return "config-id-" + name, nil
}

// NetworkName 假解析（回显目标——fake 的实况目标本就是名形态，与真实现
// 的「ID → 名」解析语义同构，驱动 steady-state 比对同锚路径）。
func (f *fakeDocker) NetworkName(_ context.Context, target string) (string, error) {
	return target, nil
}

func (f *fakeDocker) ConfigListNames(_ context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.configs))
	for name := range f.configs {
		out = append(out, name)
	}
	return out, nil
}

func (f *fakeDocker) ConfigRemove(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configsRemoved = append(f.configsRemoved, name)
	delete(f.configs, name)
	return nil
}

// eventsSinceZero 抓取事件（测试辅助）。
func eventsSinceZero(t *testing.T, st *state.Store) []state.Event {
	t.Helper()
	evs, err := st.EventsSince(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	return evs
}

// harness 是收敛测试的公共装配（真 state.Store + fake dockerPort）。
type harness struct {
	t   *testing.T
	st  *state.Store
	fk  *fakeDocker
	mgr *Manager
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.InTx(context.Background(), func(tx *state.Tx) error {
		return tx.SetMeta(context.Background(), state.MetaKeyPlatformNodeID, "n_TESTNODEID01")
	}); err != nil {
		t.Fatalf("set meta: %v", err)
	}
	fk := newFakeDocker()
	fk.swarmActive = true
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := NewManagerWithDocker(st, 14, fk, logger)
	return &harness{t: t, st: st, fk: fk, mgr: mgr}
}

// setMode 保存 metrics.mode（SaveMetricsSettings 同事务审计+事件路径）。
func (h *harness) setMode(mode string) {
	h.t.Helper()
	if err := h.st.SaveMetricsSettings(context.Background(), mode, state.MetricsSaveOptions{Actor: "system"}); err != nil {
		h.t.Fatalf("save metrics.mode %s: %v", mode, err)
	}
}

// eventsOf 名字过滤事件（测试辅助）。
func (h *harness) eventsOf(name string) []state.Event {
	h.t.Helper()
	var out []state.Event
	for _, ev := range eventsSinceZero(h.t, h.st) {
		if ev.Name == name {
			out = append(out, ev)
		}
	}
	return out
}

// TestEnsureIdleOnDefault opt-in 缺省语义（D-W5-2，验收标准 7 的单测锚）：
// 未显式设置过 metrics.mode 的库 = unset 生效 → 首拍零常驻（三件服务全不
// 建、零卷零 config）、outcome=idle；Load 投影 Set=false（缺省态与显式
// unset 的区分面）。
func TestEnsureIdleOnDefault(t *testing.T) {
	h := newHarness(t)
	in, err := h.st.LoadMetricsSettings(context.Background())
	if err != nil {
		t.Fatalf("load default: %v", err)
	}
	if in.Mode != state.MetricsModeUnset || in.Set {
		t.Fatalf("default settings = %+v, want unset unset", in)
	}

	oc, err := h.mgr.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure 1: %v", err)
	}
	if oc != outcomeIdle {
		t.Fatalf("outcome = %v, want idle", oc)
	}
	if len(h.fk.created) != 0 || len(h.fk.volumesEnsured) != 0 || len(h.fk.configEnsured) != 0 {
		t.Fatalf("default must deploy nothing: created=%v volumes=%v configs=%v",
			h.fk.created, h.fk.volumesEnsured, h.fk.configEnsured)
	}
	if len(h.eventsOf("metrics.stack_deployed")) != 0 {
		t.Fatal("default must not emit deployed events")
	}
	// 显式 unset 同样无所欠。
	h.setMode(state.MetricsModeUnset)
	if _, err := h.mgr.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure (explicit unset): %v", err)
	}
	if len(h.fk.created) != 0 {
		t.Fatalf("explicit unset deployed %v", h.fk.created)
	}
}

// TestEnsureDeploysStackOnEnable mode=on：抓取 config + 卷 + 三件服务按序
// 创建 + deployed 事件 ×3（payload 带 service/reason）；第二拍稳态零写
// （幂等——无 churn）。
func TestEnsureDeploysStackOnEnable(t *testing.T) {
	h := newHarness(t)
	h.setMode(state.MetricsModeOn)

	oc, err := h.mgr.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure 1: %v", err)
	}
	if oc != outcomeDeployed {
		t.Fatalf("outcome = %v, want deployed", oc)
	}
	if len(h.fk.created) != 3 {
		t.Fatalf("created = %v, want the three managed services", h.fk.created)
	}
	for _, name := range ComponentNames() {
		found := false
		for _, c := range h.fk.created {
			if c == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("created = %v, missing %s", h.fk.created, name)
		}
	}
	if len(h.fk.volumesEnsured) != 1 || h.fk.volumesEnsured[0] != VolumeName {
		t.Fatalf("volumes = %v, want [%s]", h.fk.volumesEnsured, VolumeName)
	}
	if len(h.fk.configEnsured) != 1 || h.fk.configEnsured[0] != scrapeConfigName() {
		t.Fatalf("configs = %v, want [%s]", h.fk.configEnsured, scrapeConfigName())
	}
	evs := h.eventsOf("metrics.stack_deployed")
	if len(evs) != 3 {
		t.Fatalf("deployed events = %d, want 3 (one per service)", len(evs))
	}
	for _, ev := range evs {
		if !strings.Contains(ev.Payload, `"reason":"created"`) {
			t.Fatalf("deployed event payload = %s, want reason created", ev.Payload)
		}
	}

	// 第二拍：稳态零写。
	if _, err := h.mgr.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure 2: %v", err)
	}
	if len(h.fk.created) != 3 || len(h.fk.updated) != 0 {
		t.Fatalf("steady state churn: created=%v updated=%v", h.fk.created, h.fk.updated)
	}
	if len(h.eventsOf("metrics.stack_deployed")) != 3 {
		t.Fatal("steady state re-emitted deployed events")
	}
}

// TestEnsureUpdatesOnDrift 漂移更新：实况偏离期望 spec（镜像/抓取配置引用）
// → 下一拍收敛更新 + deployed(updated) 事件。
func TestEnsureUpdatesOnDrift(t *testing.T) {
	h := newHarness(t)
	h.setMode(state.MetricsModeOn)
	if _, err := h.mgr.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure 1: %v", err)
	}
	// 外部漂移：VM 镜像被手改。
	cur := h.fk.services[VictoriaServiceName]
	cur.Image = "victoriametrics/victoria-metrics:latest"
	h.fk.services[VictoriaServiceName] = cur

	oc, err := h.mgr.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure 2: %v", err)
	}
	if oc != outcomeDeployed {
		t.Fatalf("outcome = %v, want deployed", oc)
	}
	if len(h.fk.updated) != 1 || h.fk.updated[0] != VictoriaServiceName {
		t.Fatalf("updated = %v, want [%s]", h.fk.updated, VictoriaServiceName)
	}
	var updatedEvent int
	for _, ev := range h.eventsOf("metrics.stack_deployed") {
		if strings.Contains(ev.Payload, `"reason":"updated"`) {
			updatedEvent++
		}
	}
	if updatedEvent != 1 {
		t.Fatalf("updated events = %d, want 1", updatedEvent)
	}
}

// TestEnsureRemovesOnDisable 切回 unset：三件服务移除 + removed 事件
// （payload 带 volume_retained=true）；卷对象永不被 duty 删除（数据安全
// 语义——VolumeRemove 不在端口面上，编译期保证）；抓取 config 清场；稳态
// 幂等（无服务时 removeIfPresent no-op 且不再发事件）。
func TestEnsureRemovesOnDisable(t *testing.T) {
	h := newHarness(t)
	h.setMode(state.MetricsModeOn)
	if _, err := h.mgr.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure deploy: %v", err)
	}
	h.setMode(state.MetricsModeUnset)

	oc, err := h.mgr.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure remove: %v", err)
	}
	if oc != outcomeIdle {
		t.Fatalf("outcome = %v, want idle", oc)
	}
	if len(h.fk.removed) != 3 {
		t.Fatalf("removed = %v, want the three managed services", h.fk.removed)
	}
	for _, name := range ComponentNames() {
		if h.fk.services[name].Exists {
			t.Fatalf("service %s still exists after disable", name)
		}
	}
	if len(h.fk.configs) != 0 {
		t.Fatalf("scrape configs = %v, want cleaned", h.fk.configs)
	}
	evs := h.eventsOf("metrics.stack_removed")
	if len(evs) != 1 || !strings.Contains(evs[0].Payload, "volume_retained") {
		t.Fatalf("removed events = %+v, want one with volume_retained", evs)
	}
	// 再一拍：无服务 → 零动作、零事件。
	if _, err := h.mgr.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure idle: %v", err)
	}
	if len(h.fk.removed) != 3 {
		t.Fatalf("idle churn: removed=%v", h.fk.removed)
	}
	if len(h.eventsOf("metrics.stack_removed")) != 1 {
		t.Fatal("idle re-emitted removed event")
	}
}

// TestEnsureSwarmNotReady 负面：swarm 未就绪 → ErrNotSwarmReady（可重试
// 态，Run 循环退避重试）。
func TestEnsureSwarmNotReady(t *testing.T) {
	h := newHarness(t)
	h.setMode(state.MetricsModeOn)
	h.fk.swarmActive = false
	if _, err := h.mgr.Ensure(context.Background()); !errors.Is(err, ErrNotSwarmReady) {
		t.Fatalf("Ensure err = %v, want ErrNotSwarmReady", err)
	}
}

// TestEnsureMissingPlatformID 负面：平台 ID 未铸（identity duty 未跑）→
// 显式错误退避重试，宁缺毋错（约束引用空 ID = 永不调度）。
func TestEnsureMissingPlatformID(t *testing.T) {
	h := newHarness(t)
	h.setMode(state.MetricsModeOn)
	if err := h.st.InTx(context.Background(), func(tx *state.Tx) error {
		return tx.SetMeta(context.Background(), state.MetaKeyPlatformNodeID, "")
	}); err != nil {
		t.Fatalf("clear meta: %v", err)
	}
	_, err := h.mgr.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "platform node id") {
		t.Fatalf("Ensure err = %v, want missing platform node id", err)
	}
}

// TestCheckHealth 健康检查面：unset = 无所欠恒绿（opt-in 缺省零常驻）；
// on + 服务缺失 = 红（收敛过渡态如实表达）；on + 三件在位 + 拨测失败 = 红
// （查询面降级诚实面）；拨测恢复 = 绿。
func TestCheckHealth(t *testing.T) {
	h := newHarness(t)
	h.setMode(state.MetricsModeUnset)
	if err := h.mgr.CheckHealth(); err != nil {
		t.Fatalf("CheckHealth(unset) = %v, want nil", err)
	}

	h.setMode(state.MetricsModeOn)
	if err := h.mgr.CheckHealth(); err == nil || !strings.Contains(err.Error(), "not deployed yet") {
		t.Fatalf("CheckHealth(missing services) = %v, want converging red", err)
	}

	if _, err := h.mgr.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	// 健康拨测未装配（nil）= 部署在位即健康。
	if err := h.mgr.CheckHealth(); err != nil {
		t.Fatalf("CheckHealth(deployed, no probe) = %v, want nil", err)
	}
	// 健康拨测失败 = 红。
	h.mgr.WithHealth(func(_ context.Context) error { return errors.New("connection refused") })
	if err := h.mgr.CheckHealth(); err == nil || !strings.Contains(err.Error(), "health probe failed") {
		t.Fatalf("CheckHealth(probe fail) = %v, want red", err)
	}
	// 拨测恢复 = 绿。
	h.mgr.WithHealth(func(_ context.Context) error { return nil })
	if err := h.mgr.CheckHealth(); err != nil {
		t.Fatalf("CheckHealth(probe ok) = %v, want nil", err)
	}
}

// TestDeploymentStatus 视图面：三件按固定序投影在位/缺失。
func TestDeploymentStatus(t *testing.T) {
	h := newHarness(t)
	h.setMode(state.MetricsModeOn)
	st, err := h.mgr.DeploymentStatus(context.Background())
	if err != nil {
		t.Fatalf("DeploymentStatus: %v", err)
	}
	if len(st.Components) != 3 {
		t.Fatalf("components = %+v, want 3", st.Components)
	}
	for _, c := range st.Components {
		if c.Exists {
			t.Fatalf("component %+v must not exist before enable", c)
		}
	}
	if _, err := h.mgr.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	st, err = h.mgr.DeploymentStatus(context.Background())
	if err != nil {
		t.Fatalf("DeploymentStatus 2: %v", err)
	}
	wantImages := map[string]string{
		CAdvisorServiceName:     DefaultCAdvisorImage,
		NodeExporterServiceName: DefaultNodeExporterImage,
		VictoriaServiceName:     DefaultVictoriaMetricsImage,
	}
	for i, name := range ComponentNames() {
		c := st.Components[i]
		if c.Name != name || !c.Exists || c.Image != wantImages[name] {
			t.Fatalf("components[%d] = %+v, want %s deployed with pinned image", i, c, name)
		}
	}
}

// stateOf 把 spec 投影为实况形态（与 realDockerClient.ServiceInspect 的
// 投影同构——fake 注入用）。
func stateOf(spec swarm.ServiceSpec) ServiceState {
	out := ServiceState{Exists: true, Version: 1}
	out.fillFrom(spec)
	return out
}

// fillFrom 用期望 spec 填充实况投影（realDockerClient 投影同构）。
func (s *ServiceState) fillFrom(spec swarm.ServiceSpec) {
	if cs := spec.TaskTemplate.ContainerSpec; cs != nil {
		s.Image = cs.Image
		s.Args = append([]string{}, cs.Args...)
		for _, m := range cs.Mounts {
			s.MountSources = append(s.MountSources, m.Source)
			s.MountTargets = append(s.MountTargets, m.Target)
		}
		for _, c := range cs.Configs {
			s.ConfigNames = append(s.ConfigNames, c.ConfigName)
		}
		if cs.Healthcheck != nil {
			s.HealthTest = append([]string{}, cs.Healthcheck.Test...)
		}
	}
	if pl := spec.TaskTemplate.Placement; pl != nil {
		s.Constraints = append([]string{}, pl.Constraints...)
	}
	if spec.Mode.Global != nil {
		s.Global = true
	}
	if spec.Mode.Replicated != nil && spec.Mode.Replicated.Replicas != nil {
		s.Replicas = *spec.Mode.Replicated.Replicas
	}
	if task := spec.TaskTemplate; task.Resources != nil && task.Resources.Limits != nil {
		s.MemoryBytes = task.Resources.Limits.MemoryBytes
	}
	for _, n := range spec.TaskTemplate.Networks {
		s.Networks = append(s.Networks, n.Target)
	}
}
