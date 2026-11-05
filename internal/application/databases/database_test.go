package databases

// Database 域卫星白盒（ADR-0029 验收锚；P5 wave-4 随域拆包自 engine 包
// 迁入，断言面零漂移）：投影面（模板钉版/项目网/寻址/卷/材料）、gen 重启
// 安全语义（指纹未变同号重放）、网集变化推进 gen 一次、凭证缺失的诚实
// 失败、收口拆载体与缓存清理、ADR-0064 归属回写（登记随回执 wire-gen）。
//
// 夹具（browses 同款）：hermetic 卫星（真 SQLite 台账 repo + Runtime 三动词
// 假面 + memo 协议端口本地实现）；facts 端口（解析面五族的核 resolve.go
// 公式）与 volumePinner 端口（核 intake 提升方法公式）持等价窄面隔离副本
//（通则⑥——留核侧 backupgate_test.go 持跨域序列与 fakeUtility/
// fakeObjectStore 隔离副本，两侧断言面零漂移）；观测窄接口经 database 轴
// 捕获盒断言。

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/application/engineports"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/database"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/material"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/network"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/secret"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/volume"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/keyring"
	dbrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/database"
	networkrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/network"
	projectrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/project"
	secretrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/secret"
	"github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/statetest"
	volumerepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/volume"
)

// 测试常量（engine 包同源夹具 ID——断言面零漂移）。
const (
	tProjectID   = "01JD0PROJ00000000000000000"
	tDatabaseID  = "01jd0db0000000000000000000"
	tDatabaseNm  = "shop"
	tControlNode = "01JD0NODE00000000000000000"
)

// fakeUtility 是 RuntimeUtility 假面（记录请求；可编程 stdout/失败；
// ADR-0097 起卷源标记 argv 自带假面——claim 形态回显内嵌标记 JSON 同源
// 恒过，marker 覆写注入异源态）。
// P5 wave-4 随域拆包自 engine/database_test.go 迁入（通则⑥——留核侧
// backupgate_test.go 持隔离副本，断言面零漂移）。
type fakeUtility struct {
	mu      sync.Mutex
	reqs    []capability.UtilityRequest
	stdins  []string
	stdout  []byte
	marker  []byte // 卷源标记覆写（非空 = 探测回读该值；异源/同源两态面）
	execErr error
}

func (f *fakeUtility) RunUtility(_ context.Context, req capability.UtilityRequest, stdout, stderr io.Writer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	if req.Input != nil {
		b, _ := io.ReadAll(req.Input.Content)
		f.stdins = append(f.stdins, string(b))
	}
	if f.execErr != nil {
		_, _ = io.WriteString(stderr, "boom-detail")
		return f.execErr
	}
	joined := strings.Join(req.Argv, " ")
	if strings.Contains(joined, dbtemplate.InstanceMarkerFile) {
		if len(f.marker) > 0 {
			_, _ = stdout.Write(f.marker)
			return nil
		}
		// claim 形态：回显脚本内嵌 printf %s '<json>' 载荷（与被确保库行
		// 同源——门恒过）。
		if i := strings.Index(joined, "printf %s '"); i >= 0 {
			rest := joined[i+len("printf %s '"):]
			if j := strings.Index(rest, "' >"); j >= 0 {
				_, _ = stdout.Write([]byte(rest[:j]))
			}
		}
		return nil
	}
	_, _ = stdout.Write(f.stdout)
	return nil
}

// setVolumeOriginMarker 覆写卷源标记探测回读（nil 清除）。
func (f *fakeUtility) setVolumeOriginMarker(marker []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marker = marker
}

func (f *fakeUtility) requests() []capability.UtilityRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.UtilityRequest, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// runtimeWithUtility 组合假 Runtime 与假 Utility（FacesOf 经组合面探测）。
type runtimeWithUtility struct {
	*fakeRuntime
	*fakeUtility
}

// fakeRuntime 是卫星消费面（Ensure/Remove/DescribeCluster）的假底座
// （browses 假面同款裁剪：wireGens 注入保留——ADR-0064 归属回写钉测面；
// DescribeCluster 供 volumePinnerBox 的节点解析；volumeCarriers 是
// RuntimeVolumeInspection 假面——卷键在场种子（ADR-0097 adoption 守卫
// 钉测面；空 = 全缺席））。
type fakeRuntime struct {
	capability.Runtime
	mu             sync.Mutex
	ensures        []ensureCall
	removed        []capability.NamespaceRef
	wireGens       map[string]capability.Generation
	failNext       bool
	clusterOv      bool
	clusterVw      capability.ClusterView
	volumeCarriers map[string]bool
}

// VolumeCarrierExists 实现 capability.RuntimeVolumeInspection（种子集查询）。
func (f *fakeRuntime) VolumeCarrierExists(_ context.Context, _ capability.NamespaceRef, volumeKey string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.volumeCarriers[volumeKey], nil
}

// seedVolumeCarrier 种下一枚卷键在场事实（adoption 守卫两态测试面）。
func (f *fakeRuntime) seedVolumeCarrier(volumeKey string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.volumeCarriers == nil {
		f.volumeCarriers = map[string]bool{}
	}
	f.volumeCarriers[volumeKey] = true
}

func (f *fakeRuntime) Describe() capability.ProviderDescriptor {
	return capability.ProviderDescriptor{Name: "fake-runtime", Capability: capability.KindRuntime, Version: "test"}
}

func (f *fakeRuntime) Health(context.Context) capability.HealthReport {
	return capability.HealthReport{Healthy: true}
}

func (f *fakeRuntime) Ensure(_ context.Context, ns capability.NamespaceRef, ws []capability.Workload, gen capability.Generation, m capability.Materials) (capability.EnsureReport, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	spec := map[string]capability.Workload{}
	for _, w := range ws {
		spec[w.Process] = w
	}
	f.ensures = append(f.ensures, ensureCall{NS: ns, Gen: gen, Spec: spec, Materials: m})
	if f.failNext {
		f.failNext = false
		return capability.EnsureReport{}, fmt.Errorf("injected ensure failure")
	}
	report := capability.EnsureReport{}
	if len(f.wireGens) > 0 {
		report.Gens = f.wireGens
	}
	return report, nil
}

func (f *fakeRuntime) Remove(_ context.Context, ns capability.NamespaceRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, ns)
	return nil
}

func (f *fakeRuntime) DescribeCluster(context.Context) (capability.ClusterView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.clusterOv {
		return f.clusterVw, nil
	}
	return capability.ClusterView{Nodes: []capability.NodeView{{
		NodeID: tControlNode, CarrierID: "swarmmanager", Role: "manager", Available: true,
	}}}, nil
}

func (f *fakeRuntime) calls() []ensureCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ensureCall, len(f.ensures))
	copy(out, f.ensures)
	return out
}

func (f *fakeRuntime) removedSnapshot() []capability.NamespaceRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]capability.NamespaceRef, len(f.removed))
	copy(out, f.removed)
	return out
}

// ensureCall 是一次 Ensure 的记录面。
type ensureCall struct {
	NS        capability.NamespaceRef
	Gen       capability.Generation
	Spec      map[string]capability.Workload
	Materials capability.Materials
}

// memoBox 是 memo 协议端口的本地实现（与核 ensureMu 族等价的锁语义——
// 白盒夹具，browses 同款；重放节拍不参与本夹具的断言面）。
type memoBox struct {
	mu sync.Mutex
}

func (m *memoBox) fresh(memo map[string]engineports.Memo, key, sig string, _ time.Time) (engineports.Memo, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := memo[key]
	if !ok || rec.Sig != sig {
		return engineports.Memo{}, false
	}
	return rec, true
}

func (m *memoBox) remember(memo map[string]engineports.Memo, key string, rec engineports.Memo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	memo[key] = rec
}

func (m *memoBox) forget(memo map[string]engineports.Memo, key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(memo, key)
}

// obsBox 是观测窄接口（database 轴）的捕获盒：观测注入/读取面 + 归属/
// 期望登记捕获面（核绑定闭包铸造 databaseOwner——卫星侧按轴断言等价，
// ensuredledger 守卫执法核侧唯一写点）。
type obsBox struct {
	mu               sync.Mutex
	obs              map[string]capability.WorkloadEvent
	ownersRegistered []ownerRecord
	expected         map[string]uint64
	forgotExpected   map[string]bool
	forgotObs        map[string]bool
}

// ownerRecord 是一次归属登记的捕获面（databaseID + gen + 下发集）。
type ownerRecord struct {
	databaseID string
	gen        uint64
	ws         []capability.Workload
}

func newObsBox() *obsBox {
	return &obsBox{
		obs:            map[string]capability.WorkloadEvent{},
		expected:       map[string]uint64{},
		forgotExpected: map[string]bool{},
		forgotObs:      map[string]bool{},
	}
}

func (o *obsBox) inject(databaseID string, ev capability.WorkloadEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.obs[databaseID] = ev
}

func (o *obsBox) observationOf(databaseID string) (capability.WorkloadEvent, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	ev, ok := o.obs[databaseID]
	return ev, ok
}

func (o *obsBox) forgetObservation(databaseID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.obs, databaseID)
	o.forgotObs[databaseID] = true
}

func (o *obsBox) recordOwners(databaseID string, gen uint64, ws []capability.Workload) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ownersRegistered = append(o.ownersRegistered, ownerRecord{databaseID: databaseID, gen: gen, ws: ws})
}

func (o *obsBox) recordExpected(databaseID string, gen uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.expected[databaseID] = gen
}

func (o *obsBox) forgetExpected(databaseID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.expected, databaseID)
	o.forgotExpected[databaseID] = true
}

func (o *obsBox) snapshot() (expected map[string]uint64, owners []ownerRecord) {
	o.mu.Lock()
	defer o.mu.Unlock()
	expected = make(map[string]uint64, len(o.expected))
	for k, v := range o.expected {
		expected[k] = v
	}
	owners = append(owners, o.ownersRegistered...)
	return expected, owners
}

// forgotten 报告两侧 forget 族端口是否已按 database 轴清账。
func (o *obsBox) forgotten(databaseID string) (expectedGone, obsGone bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.forgotExpected[databaseID], o.forgotObs[databaseID]
}

// fixture 是 databases 卫星白盒夹具的组装面。
type fixture struct {
	d      *Databases
	rt     *fakeRuntime
	ut     *fakeUtility
	stores transact.Stores
	clock  state.Clock
	run    state.Runner
	cipher *material.Cipher
	obs    *obsBox
}

// newFixture 组装卫星夹具：真 SQLite 台账 repo + 假 Runtime（utility 可
// 选）+ 全端口本地实现（memo/facts/volumePinner/观测窄接口——核公式
// 等价隔离副本，通则⑥）。落一条 running 起步的库行（engine 侧
// newDatabaseUtilityFixture 同源形态）。
func newFixture(t *testing.T, engineName, connectURL string, withUtility bool) *fixture {
	t.Helper()
	db, clock := statetest.New(t)
	run := db.Runner()
	rt := &fakeRuntime{}
	cipher, err := keyring.LoadCipher(t.TempDir())
	require.NoError(t, err)
	stores := transact.Stores{
		Projects: projectrepo.New(clock, run), Networks: networkrepo.New(clock, run),
		Secrets: secretrepo.New(clock, run), Databases: dbrepo.New(clock, run),
		Volumes: volumerepo.New(clock, run),
	}
	ctx := context.Background()
	require.NoError(t, projectrepo.New(clock, run).Create(ctx, &project.Project{
		ID: tProjectID, Name: "shop", TeamID: "default",
	}))
	require.NoError(t, networkrepo.New(clock, run).Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000001", ProjectID: tProjectID, Name: "default",
	}))
	secName := engineports.DBCredentialSecretName(tDatabaseNm)
	ct, err := cipher.Seal([]byte(connectURL))
	require.NoError(t, err)
	require.NoError(t, secretrepo.New(clock, run).Upsert(ctx, &secret.Secret{
		ID: "01JD0SEC000000000000000001", ProjectID: tProjectID, Name: secName,
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(connectURL)),
	}))
	require.NoError(t, dbrepo.New(clock, run).Create(ctx, &database.Database{
		ID: tDatabaseID, ProjectID: tProjectID, Name: tDatabaseNm,
		Engine: engineName, CredentialsRef: secName,
		BackupIntervalSecs: 86400, BackupRetentionSecs: 604800,
	}))

	memo := &memoBox{}
	obs := newObsBox()
	// utility 面恒装配（ADR-0097 卷源标记门 fail-closed 依赖；rotate 备份
	// 面可编程 stdout 不变），withUtility 只控制非标记 argv 的缺省回显。
	u := &fakeUtility{}
	if withUtility {
		u.stdout = []byte("UTILITY-OK")
	}
	rtFace := capability.Runtime(runtimeWithUtility{rt, u})
	fx := &fixture{rt: rt, stores: stores, clock: clock, run: run, cipher: cipher, obs: obs, ut: u}
	fx.d = New(Deps{
		Runtime: rtFace,
		Repos:   stores,
		Cipher:  cipher,
		Utility: u,
		Log:     slog.New(slog.DiscardHandler), // 夹具零噪音（错误路径日志面有 e2e 覆盖）
		Clock:   clock,
		LockMaintenance: func() func() {
			return func() {}
		},
		BoundedStep: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithTimeout(ctx, 30*time.Second)
		},
		EnsureFresh:                  memo.fresh,
		EnsureRemember:               memo.remember,
		EnsureForget:                 memo.forget,
		ProjectTeam:                  func(context.Context, string) (string, error) { return "default", nil },
		ProjectTeamOf:                func(string) string { return "default" },
		ProjectNetworkFacts:          fx.listNetworks,
		ProjectNetworkFactsNamesOnly: fx.listNetworksNamesOnly,
		EnsureDatabaseVolume:         fx.ensureDatabaseVolume,
		DatabasePassword:             fx.databasePassword,
		PinVolumes:                   fx.pinVolumes,
		ApplyVolumePinning:           fx.applyVolumePinning,
		GuardVolumeCarriers:          fx.guardVolumeCarriers,
		ControlPlaneNode:             func(context.Context) (string, error) { return tControlNode, nil },
		KickDatabases:                func() {},
		RecordOwners:                 obs.recordOwners,
		RecordExpected:               obs.recordExpected,
		ForgetExpected:               obs.forgetExpected,
		ForgetObservation:            obs.forgetObservation,
		ObservationOf:                obs.observationOf,
		EnsureMemo:                   make(map[string]engineports.Memo),
	})
	return fx
}

// listNetworks 是解析面 projectNetworkFacts 的等价窄面（核 resolve.go 公式
// 的隔离副本，通则⑥——gen 推进断言依赖真实行集）。
func (fx *fixture) listNetworks(ctx context.Context, projectID string) ([]string, map[string]bool) {
	rows, err := fx.stores.Networks.ListByProject(ctx, projectID)
	if err != nil {
		return nil, nil
	}
	names := make([]string, 0, len(rows))
	egress := map[string]bool{}
	for _, n := range rows {
		names = append(names, n.Name)
		if n.EgressNone {
			egress[n.Name] = true
		}
	}
	return names, egress
}

func (fx *fixture) listNetworksNamesOnly(ctx context.Context, projectID string) []string {
	names, _ := fx.listNetworks(ctx, projectID)
	return names
}

// ensureDatabaseVolume 是 facts 端口卷登记族的等价窄面（核 resolve.go
// 公式的隔离副本，通则⑥——NotFound 即建；ADR-0097 起返回行）。
func (fx *fixture) ensureDatabaseVolume(ctx context.Context, row *database.Database) (*volume.Volume, error) {
	if existing, err := fx.stores.Volumes.GetByName(ctx, row.ProjectID, row.Name); err == nil {
		return existing, nil
	} else if !errors.Is(err, state.ErrNotFound) {
		return nil, err
	}
	created := &volume.Volume{ProjectID: row.ProjectID, Name: row.Name}
	if err := fx.stores.Volumes.Create(ctx, created); err != nil {
		return nil, err
	}
	return created, nil
}

// databasePassword 是 facts 端口凭证解析族的等价窄面（核 resolve.go 公式
// 的隔离副本，通则⑥——Secret 解封 + 连接串密码回读）。
func (fx *fixture) databasePassword(ctx context.Context, row *database.Database) (string, error) {
	sec, err := fx.stores.Secrets.GetByName(ctx, row.ProjectID, row.CredentialsRef)
	if err != nil {
		return "", fmt.Errorf("lookup credential secret %q: %w", row.CredentialsRef, err)
	}
	plain, err := fx.cipher.Open(sec.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("decrypt credential secret %q: %w", row.CredentialsRef, err)
	}
	return dbtemplate.PasswordFromURL(string(plain))
}

// pinVolumes 是 volumePinner 端口的等价窄面（核 intake 提升方法 materials.go
// 公式的隔离副本，通则⑥——未钉卷首挂锚定到首个可用 manager；ADR-0097 起
// VolumeID 是卷行 id，按行 id 取行、Pin 落行名）。
func (fx *fixture) pinVolumes(ctx context.Context, ws []capability.Workload, projectID string) error {
	var pending []*volume.Volume
	for _, w := range ws {
		for _, v := range w.Volumes {
			row, err := fx.stores.Volumes.Get(ctx, v.VolumeID)
			if err != nil {
				return fmt.Errorf("volume %q: %w", v.VolumeID, err)
			}
			if row.PinnedNodeID == "" {
				pending = append(pending, row)
			}
		}
	}
	if len(pending) == 0 {
		return nil
	}
	view, err := fx.rt.DescribeCluster(ctx)
	if err != nil {
		return err
	}
	nodeID := ""
	for _, n := range view.Nodes {
		if n.Available && n.NodeID != "" {
			nodeID = n.NodeID
			break
		}
	}
	if nodeID == "" {
		return fmt.Errorf("no available node to pin volumes to")
	}
	for _, row := range pending {
		if err := fx.stores.Volumes.Pin(ctx, projectID, row.Name, nodeID); err != nil {
			return fmt.Errorf("pin volume %q: %w", row.Name, err)
		}
	}
	return nil
}

// applyVolumePinning 是 volumePinner 端口的等价窄面（核 intake 提升方法
// 公式的隔离副本，通则⑥——钉住合并进 Placement；按行 id 取行）。
func (fx *fixture) applyVolumePinning(ctx context.Context, ws []capability.Workload, projectID string) error {
	for i := range ws {
		w := &ws[i]
		for _, v := range w.Volumes {
			row, err := fx.stores.Volumes.Get(ctx, v.VolumeID)
			if err != nil {
				return fmt.Errorf("volume %q: %w", v.VolumeID, err)
			}
			if row.PinnedNodeID == "" {
				continue
			}
			seen := false
			for _, id := range w.Placement.NodeIDs {
				if id == row.PinnedNodeID {
					seen = true
				}
			}
			if !seen {
				w.Placement.NodeIDs = append(w.Placement.NodeIDs, row.PinnedNodeID)
			}
		}
	}
	return nil
}

// errLegacyVolumeCarrierFixture 是 adoption 守卫镜像的哨兵（核
// engine.ErrLegacyVolumeCarrier 的隔离副本，通则⑥——卫星包不 import 核）。
var errLegacyVolumeCarrierFixture = errors.New("legacy name-keyed volume carrier present (fixture)")

// guardVolumeCarriers 是 adoption 守卫端口的等价窄面（核 engine/volumes.go
// GuardVolumeCarriers 公式的隔离副本，通则⑥——新键缺席而旧名键在场即拒）。
func (fx *fixture) guardVolumeCarriers(ctx context.Context, ns capability.NamespaceRef, ws []capability.Workload, projectID string) error {
	seen := map[string]bool{}
	for _, w := range ws {
		for _, v := range w.Volumes {
			if seen[v.VolumeID] {
				continue
			}
			seen[v.VolumeID] = true
			row, err := fx.stores.Volumes.Get(ctx, v.VolumeID)
			if err != nil {
				return fmt.Errorf("adoption guard: resolve volume %q: %w", v.VolumeID, err)
			}
			current, err := fx.rt.VolumeCarrierExists(ctx, ns, row.ID)
			if err != nil {
				return fmt.Errorf("adoption guard: probe carrier for volume %q: %w", row.Name, err)
			}
			if current {
				continue
			}
			legacy, err := fx.rt.VolumeCarrierExists(ctx, ns, row.Name)
			if err != nil {
				return fmt.Errorf("adoption guard: probe legacy carrier for volume %q: %w", row.Name, err)
			}
			if legacy {
				return fmt.Errorf("%w: volume %q", errLegacyVolumeCarrierFixture, row.Name)
			}
		}
	}
	return nil
}

// 收敛：Ensure 到 Database 域 ns + 模板钉版投影 + 凭证材料 + 卷挂载 +
// db-<id> 寻址。
func TestDatabaseReconcileConverges(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()

	fx.d.Step(ctx)
	fx.d.Step(ctx) // 幂等：再次收敛同号重放

	calls := fx.rt.calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	assert.Equal(t, capability.NamespaceRef{Team: "default", Project: tProjectID, Database: tDatabaseID, DatabaseName: tDatabaseNm}, last.NS)

	w := last.Spec["postgres"]
	require.NotNil(t, w, "workload keyed by template engine name")
	assert.Equal(t, tDatabaseID, w.ID)
	assert.Equal(t, "postgres:18-trixie@sha256:bd5dfa494a5a259704365813dbfa5fa6b3e69be7348d43b547f540c730478583", w.Image)
	assert.Equal(t, []string{"default"}, w.Networks, "database attaches the project's active networks")
	assert.Equal(t, []capability.Address{{Name: engineports.DatabaseDNSName(tDatabaseID)}}, w.Addressing)
	require.Len(t, w.Volumes, 1)
	// 载体键 = 卷行 id（ADR-0097：库名确定性公式解析出行，运行时零 name）。
	volRow, verr := fx.stores.Volumes.GetByName(ctx, tProjectID, tDatabaseNm)
	require.NoError(t, verr)
	assert.Equal(t, volRow.ID, w.Volumes[0].VolumeID, "volume carrier key = volume row id (ADR-0097)")
	assert.True(t, entityid.LooksMinted(w.Volumes[0].VolumeID), "carrier key must be minted-id shaped (name backflow guard)")
	assert.Equal(t, "/var/lib/postgresql", w.Volumes[0].Target, "parent mount: 18+ layout entrypoint compatible")
	// 数据面停止宽限（升级/轮换滚动替换的优雅收口窗）：编排器缺省 10s
	// 硬杀窗会把 WAL 留在损坏态（staging pgvector 事故实证，2026-10-03）。
	assert.Equal(t, 60*time.Second, w.StopGrace, "database workload must carry an explicit generous stop grace")
	assert.Equal(t, "/run/secrets/"+dbtemplate.PasswordFile, w.Env["POSTGRES_PASSWORD_FILE"])
	assert.Equal(t, int64(1), w.Replicas)
	require.NotNil(t, w.Healthcheck)
	// 引擎原生 exec 探针（模板单源；通用 TCP 方言的 nc 假设不成立，
	// staging 真机实证 2026-10-02）。
	assert.Equal(t, []string{"pg_isready", "-h", "127.0.0.1", "-p", "5432", "-U", "fleetly", "-d", "fleetly"}, w.Healthcheck.Exec)
	// 密码文件材料（值来自连接串回读——单真源）。
	assert.Equal(t, []byte("secretpw"), last.Materials.SecretFiles[dbtemplate.PasswordFile])
	// 首挂钉住合并进 Placement（fake 集群默认一节点可用）。
	assert.Equal(t, []string{tControlNode}, w.Placement.NodeIDs)

	// 行状态推进：gen=1 落行（重读行——本地结构不随环推进）；观测
	// running 后状态翻 running。
	fresh, err := fx.stores.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Equal(t, uint64(1), fresh.Generation)
	assert.NotEmpty(t, fresh.SpecFingerprint, "spec fingerprint persisted alongside the generation")
	fx.obs.inject(tDatabaseID, capability.WorkloadEvent{
		WorkloadID: tDatabaseID, Generation: 1, State: capability.WorkloadRunning,
	})
	fx.d.Step(ctx)
	fresh, err = fx.stores.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Equal(t, database.StatusRunning, fresh.Status)
}

// gen 重启安全语义：指纹未变多 tick 不重下发（载体不滚；N1 C16 短路——
// 稳态零 Ensure 调用）；网集变化推进 gen 一次后在新值稳定（manageds 侧
// TestManagedGenerationStableAcrossTicks 钉死语义的用户域版）。
func TestDatabaseGenerationStableAcrossTicks(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()

	for i := 0; i < 4; i++ {
		fx.d.Step(ctx)
	}
	calls := fx.rt.calls()
	require.Len(t, calls, 1, "unchanged inputs must short-circuit to a single Ensure (C16)")
	assert.Equal(t, capability.Generation(1), calls[0].Gen, "unchanged projection stays on its generation")

	// 新项目网 → 网集变化 → 短路失效，恰好一次重下发（gen 推进），随后稳定。
	require.NoError(t, networkrepo.New(fx.clock, fx.run).Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000002", ProjectID: tProjectID, Name: "internal",
	}))
	for i := 0; i < 3; i++ {
		fx.d.Step(ctx)
	}
	calls = fx.rt.calls()
	require.Len(t, calls, 2, "network-set change must produce exactly one new Ensure")
	assert.Greater(t, uint64(calls[1].Gen), uint64(calls[0].Gen), "network-set change must advance the generation")
	assert.Len(t, calls[1].Spec["postgres"].Networks, 2)
}

// ADR-0064 归属回写（2026-10-18 upgrade 矩阵实证的假 drift 根因钉）：指纹
// 变化推进 gen 后，Provider 内容等价 skip 让载体保留现存代次（wire-gen 回
// 执 < call-gen）——归属/期望登记必须按回执 wire-gen 记账；按 call-gen 记
// 账则 Watch 对照必然失配、发假 workload.drift_detected。卫星侧按 database
// 轴捕获登记参数断言（核 obs/expect 的落账面由 ensuredledger 守卫执法唯一
// 写点，per-carrier 锚与登记同源同拍）。
func TestDatabaseRegistrationUsesWireGenFromReport(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()

	fx.d.Step(ctx)
	expected, _ := fx.obs.snapshot()
	assert.Equal(t, uint64(1), expected[tDatabaseID])

	// 指纹变化（新网入集）→ gen 推进 2；Provider 回执 wire-gen=1（内容等价
	// skip：载体实际停在 1）。
	fx.rt.wireGens = map[string]capability.Generation{tDatabaseID: 1}
	require.NoError(t, networkrepo.New(fx.clock, fx.run).Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000003", ProjectID: tProjectID, Name: "pinned",
	}))
	fx.d.Step(ctx)

	expected, owners := fx.obs.snapshot()
	registered := expected[tDatabaseID]
	assert.Equal(t, uint64(1), registered,
		"registration must follow the report wire-gen, not the call generation")
	require.NotEmpty(t, owners, "owner registration must have been recorded")
	assert.Equal(t, tDatabaseID, owners[len(owners)-1].databaseID)
	assert.Equal(t, uint64(1), owners[len(owners)-1].gen,
		"per-carrier registration rides the same wire-gen (same tick, same source)")
}

// R13-L3（S16-c8）：状态推进与归属登记同律——观测对照必须按回执
// wire-gen，而非 Ensure 的 call-gen。内容等价 skip 让载体停在旧代次时，
// 观测（wire-gen 标注）与 call-gen 恒不匹配 → 状态永久停在 pending。
func TestDatabaseStatusAdvanceUsesWireGenFromReport(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()

	fx.d.Step(ctx) // gen=1 落行
	// 指纹变化（新网入集）→ call-gen 推进 2；Provider 内容等价 skip，回执
	// wire-gen=1（载体实际停在 1）——观测以 wire-gen 标注。
	fx.rt.wireGens = map[string]capability.Generation{tDatabaseID: 1}
	require.NoError(t, networkrepo.New(fx.clock, fx.run).Create(ctx, &network.Network{
		ID: "01JD0NET000000000000000004", ProjectID: tProjectID, Name: "wiregen-status",
	}))
	fx.obs.inject(tDatabaseID, capability.WorkloadEvent{
		WorkloadID: tDatabaseID, Generation: 1, State: capability.WorkloadRunning,
	})
	fx.d.Step(ctx) // Ensure 走 call-gen 2，回执 wire-gen 1

	calls := fx.rt.calls()
	require.Len(t, calls, 2)
	require.Equal(t, capability.Generation(2), calls[1].Gen, "the fixture must advance the call generation")
	fresh, err := fx.stores.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Equal(t, database.StatusRunning, fresh.Status,
		"status must advance against the wire-gen the carrier actually reports, not the call generation")
}

// N1 C16：签名短路的短路拍不停状态收敛——观测 running 到达后，跳过 Ensure
// 的拍照样把行状态推进 running（观测缓存独立于 Ensure）。
func TestDatabaseShortCircuitKeepsStatusAdvancing(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()

	fx.d.Step(ctx)
	require.Len(t, fx.rt.calls(), 1)

	// 短路拍：观测 running@gen1 到达（Watch 流路径），Ensure 不重发而状态照推。
	fx.obs.inject(tDatabaseID, capability.WorkloadEvent{
		WorkloadID: tDatabaseID, Generation: 1, State: capability.WorkloadRunning,
	})
	fx.d.Step(ctx)
	assert.Len(t, fx.rt.calls(), 1, "the short-circuited tick must not re-Ensure")
	fresh, err := fx.stores.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Equal(t, database.StatusRunning, fresh.Status, "status must advance on the skipped tick")

	// 凭证轮换（密文变化）→ 短路失效重下发（新密码材料随 Ensure）。
	ct, err := fx.cipher.Seal([]byte("postgresql://fleetly:newpw@db-01jd0db0000000000000000000:5432/fleetly"))
	require.NoError(t, err)
	require.NoError(t, secretrepo.New(fx.clock, fx.run).Upsert(ctx, &secret.Secret{
		ID: "01JD0SEC000000000000000001", ProjectID: tProjectID,
		Name: engineports.DBCredentialSecretName(tDatabaseNm), Ciphertext: ct,
		Fingerprint: material.Fingerprint([]byte("postgresql://fleetly:newpw@db-01jd0db0000000000000000000:5432/fleetly")),
	}))
	fx.d.Step(ctx)
	calls := fx.rt.calls()
	require.Len(t, calls, 2, "credential rotation must invalidate the short-circuit")
	assert.Equal(t, []byte("newpw"), calls[1].Materials.SecretFiles[dbtemplate.PasswordFile])
}

// N1 C16：凭证 Secret 读失败 = 不 Ensure 不落签名（诚实失败语义与短路
// 互不破坏：失败拍清签名，恢复后下拍重下发）。
func TestDatabaseCredentialFailureClearsSignature(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()

	fx.d.Step(ctx)
	require.Len(t, fx.rt.calls(), 1)

	// 软删凭证 → 短路拍预读失败 → 不 Ensure。
	require.NoError(t, secretrepo.New(fx.clock, fx.run).SoftDelete(ctx, tProjectID, engineports.DBCredentialSecretName(tDatabaseNm)))
	fx.d.Step(ctx)
	assert.Len(t, fx.rt.calls(), 1, "credential lookup failure must not reach Ensure")

	// 恢复凭证 → 下拍重下发（签名此前已被失败拍拒绝）。软删行不复用
	// ID（Upsert 走 GetByName 活跃行判定，旧 ID 已占主键）。
	ct, err := fx.cipher.Seal([]byte(url))
	require.NoError(t, err)
	require.NoError(t, secretrepo.New(fx.clock, fx.run).Upsert(ctx, &secret.Secret{
		ID: "01JD0SEC0000000000000000R1", ProjectID: tProjectID,
		Name: engineports.DBCredentialSecretName(tDatabaseNm), Ciphertext: ct,
		Fingerprint: material.Fingerprint([]byte(url)),
	}))
	fx.d.Step(ctx)
	require.Len(t, fx.rt.calls(), 2, "recovered credentials must re-Ensure (no stale signature)")
}

// redis 模板：requirepass 配置文件材料 + 干净 argv（不落明文）。
func TestDatabaseRedisTemplateMaterials(t *testing.T) {
	const url = "redis://:redispw@db-01jd0db0000000000000000000:6379/0" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "redis", url, false)
	ctx := context.Background()

	fx.d.Step(ctx)
	calls := fx.rt.calls()
	require.NotEmpty(t, calls)
	last := calls[len(calls)-1]
	w := last.Spec["redis"]
	assert.Equal(t, "redis:8.2@sha256:b0b01167ff47bc3868e56a967895a26be4230891a1f42a78ab61cdd227accc8d", w.Image)
	assert.Equal(t, []string{"redis-server", "/run/secrets/" + dbtemplate.RedisConfFile}, w.Command)
	conf := string(last.Materials.SecretFiles[dbtemplate.RedisConfFile])
	assert.Contains(t, conf, "requirepass redispw")
	assert.Contains(t, conf, "appendonly yes")
	assert.Equal(t, []string{"redis-cli", "-p", "6379", "ping"}, w.Healthcheck.Exec)
}

// pgvector 模板：上游镜像 + 首启建扩展的 init 脚本命令。
func TestDatabasePgvectorTemplateRenders(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "pgvector", url, false)
	ctx := context.Background()

	fx.d.Step(ctx)
	calls := fx.rt.calls()
	require.NotEmpty(t, calls)
	w := calls[len(calls)-1].Spec["pgvector"]
	assert.Equal(t, "pgvector/pgvector:0.8.7-pg18-trixie@sha256:5895949dfcdf308eb0cd31c950c93c14895c77f33fbedfecf1fa511ab1d4bcd2", w.Image)
	require.Len(t, w.Command, 3)
	assert.Contains(t, w.Command[2], "CREATE EXTENSION IF NOT EXISTS vector")
	assert.Contains(t, w.Command[2], "docker-entrypoint.sh")
}

// 凭证 Secret 缺失 = 诚实失败：不 Ensure（载体保持现状），不推进 gen。
func TestDatabaseCredentialMissingBlocksEnsure(t *testing.T) {
	fx := newFixture(t, "postgres", "postgresql://u:p@h:5432/d", false) //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	ctx := context.Background()
	require.NoError(t, secretrepo.New(fx.clock, fx.run).SoftDelete(ctx, tProjectID, engineports.DBCredentialSecretName(tDatabaseNm)))

	fx.d.Step(ctx)
	assert.Empty(t, fx.rt.calls(), "credential failure must not reach Ensure")

	fresh, err := fx.stores.Databases.Get(ctx, tDatabaseID)
	require.NoError(t, err)
	assert.Equal(t, uint64(0), fresh.Generation)
	assert.Equal(t, database.StatusPending, fresh.Status)
}

// 收口：Runtime.Remove 到 Database 域 + 观测窄接口清理（forget 族沿
// database 轴断言——核 obs/expect 清账面等价）。
func TestTeardownDatabaseRemovesCarriers(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()
	fx.d.Step(ctx)
	require.NotEmpty(t, fx.rt.calls())

	require.NoError(t, fx.d.TeardownDatabase(ctx, tDatabaseID))
	ns := capability.NamespaceRef{Team: "default", Project: tProjectID, Database: tDatabaseID, DatabaseName: tDatabaseNm}
	require.Contains(t, fx.rt.removedSnapshot(), ns)

	_, hasObs := fx.obs.observationOf(tDatabaseID)
	assert.False(t, hasObs, "observation cache must be dropped")
	clearedExpected, clearedObs := fx.obs.forgotten(tDatabaseID)
	assert.True(t, clearedExpected, "expected anchor must be forgotten along the database axis")
	assert.True(t, clearedObs, "observation entry must be forgotten along the database axis")

	// 不存在/已删：NotFound（对齐 TeardownApp 口径）。
	err := fx.d.TeardownDatabase(ctx, "01JD0MISSING0000000000000X")
	assert.ErrorIs(t, err, state.ErrNotFound)
}

// 连接串铸造 ↔ 密码回读（单真源往返）。
func TestDatabaseConnectionURLRoundTrip(t *testing.T) {
	pg, err := DatabaseConnectionURL("postgres", tDatabaseID, "p: w@rd")
	require.NoError(t, err)
	assert.Equal(t,
		fmt.Sprintf("postgresql://fleetly:p%%3A%%20w%%40rd@%s:5432/fleetly", engineports.DatabaseDNSName(tDatabaseID)), pg)
	password, err := dbtemplate.PasswordFromURL(pg)
	require.NoError(t, err)
	assert.Equal(t, "p: w@rd", password)

	rd, err := DatabaseConnectionURL("redis", tDatabaseID, "redispw")
	require.NoError(t, err)
	assert.Equal(t, "redis://:redispw@db-01jd0db0000000000000000000:6379/0", rd)
	password, err = dbtemplate.PasswordFromURL(rd)
	require.NoError(t, err)
	assert.Equal(t, "redispw", password)

	// F2.1 矩阵扩展：mysql/mongo 同一往返（密码特殊字符走百分号编码）。
	my, err := DatabaseConnectionURL("mysql", tDatabaseID, "p: w@rd")
	require.NoError(t, err)
	assert.Equal(t,
		fmt.Sprintf("mysql://fleetly:p%%3A%%20w%%40rd@%s:3306/fleetly", engineports.DatabaseDNSName(tDatabaseID)), my)
	password, err = dbtemplate.PasswordFromURL(my)
	require.NoError(t, err)
	assert.Equal(t, "p: w@rd", password)

	mo, err := DatabaseConnectionURL("mongo", tDatabaseID, "mongopw")
	require.NoError(t, err)
	assert.Equal(t,
		fmt.Sprintf("mongodb://fleetly:mongopw@%s:27017/fleetly", engineports.DatabaseDNSName(tDatabaseID)), mo)
	password, err = dbtemplate.PasswordFromURL(mo)
	require.NoError(t, err)
	assert.Equal(t, "mongopw", password)

	_, err = DatabaseConnectionURL("oracle", tDatabaseID, "x")
	assert.Error(t, err, "engine value domain is closed by the template registry")
}

// TestDatabaseReconcileAdoptionGuard（ADR-0097 决策 3 的卫星面）：收敛环在
// Ensure 前执法存量 adoption——旧名载体（卷名键）在场且新键（行 id）缺席
// = 拒绝确保（载体保持现状、gen 不推进）；新键在场即正常收敛。
func TestDatabaseReconcileAdoptionGuard(t *testing.T) {
	const url = "postgresql://fleetly:secretpw@db-01jd0db0000000000000000000:5432/fleetly" //nolint:gosec // G101 误报：测试夹具 URL，非真凭证
	fx := newFixture(t, "postgres", url, false)
	ctx := context.Background()

	// 旧名载体在场、新键缺席：拒绝确保（零 Ensure 调用）。
	fx.rt.seedVolumeCarrier(tDatabaseNm)
	fx.d.Step(ctx)
	assert.Empty(t, fx.rt.calls(), "legacy name-keyed carrier must refuse the ensure (carriers untouched)")

	// 运行侧迁移完成（旧名清理 + 新键在场）：收敛恢复。
	volRow, err := fx.stores.Volumes.GetByName(ctx, tProjectID, tDatabaseNm)
	require.NoError(t, err)
	fx.rt.seedVolumeCarrier(volRow.ID)
	fx.d.Step(ctx)
	calls := fx.rt.calls()
	require.NotEmpty(t, calls, "current id-keyed carrier lets the database converge")
	require.Len(t, calls[len(calls)-1].Spec["postgres"].Volumes, 1)
	assert.Equal(t, volRow.ID, calls[len(calls)-1].Spec["postgres"].Volumes[0].VolumeID,
		"carrier key = volume row id (ADR-0097)")
}
