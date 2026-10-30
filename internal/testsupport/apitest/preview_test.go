package apitest_test

// B1 W2a 预览管线的 handler 级全链测试（设计稿 2026-10-30 §1.3/§1.4）：
//   - 预览 → 带票 Deploy 全链（接 W2b 验烧——真铸票面）；
//   - CEL 零漂移（同一非法载荷对 Deploy 与 PreviewDeploy 的 protovalidate
//     拒绝逐规则一致——形状层单源）；
//   - 脱敏红线（零 env 值集/零 FLEETLY_DB_*/载荷规模）；
//   - rollout 三态 golden + 三类预警 golden（在途位次/现场漂移文案）；
//   - 空基线腿（首次部署全量 added，不照抄 ErrNotFound）；
//   - strict/isolate 口径差（预览 strict 降级 + 渲染 isolate 剥离的对照）；
//   - PreviewTeardown 预演 golden + TOCTOU（预览后加 route → 确认 STALE）；
//   - ScaleProcess API 面（受理/生效/哨兵）。

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	proxyv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/proxy/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

var previewGoldenUpdate = flag.Bool("update", false, "rewrite preview goldens")

// checkPreviewGolden 对表 golden（-update 重写；缺失即红——提示再生成）。
func checkPreviewGolden(t *testing.T, name, content string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *previewGoldenUpdate {
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600)) //nolint:gosec // 测试 golden 产物
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // 读取本包 testdata 自有夹具
	require.NoError(t, err, "golden missing; run go test ./internal/testsupport/apitest -update")
	assert.Equal(t, string(want), content, "preview golden drifted (design 2026-10-30 §1.3)")
}

// w2aDeployAndWaitSucceeded 部署并推进到 succeeded（假 runtime 观测 + 时钟
// 过 L3 窗——apitest_test.go TestDeployEndToEnd 同节拍；多进程形态对最近
// 一次 Ensure 的全 Workload 逐个上报 running）。
func w2aDeployAndWaitSucceeded(t *testing.T, h *apitest.Harness, ctx context.Context, appID string, req *deliveryv1.DeployRequest) *deliveryv1.DeployResponse {
	t.Helper()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	dep, err := dc.Deploy(ctx, req)
	require.NoError(t, err)
	// releasing 先于 Ensure 记录可见（状态迁移与载体下发同 step 两段）——
	// 等 releasing 落定再上报 L1（观测早于 L1 wait 布防会被丢弃）。
	require.Eventually(t, func() bool {
		got, err := dc.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == "releasing"
	}, 5e9, 2e7)
	require.Eventually(t, func() bool {
		return len(h.Runtime.Calls()) > 0
	}, 5e9, 2e7)
	calls := h.Runtime.Calls()
	// Spec 按进程名键控；L1 上报按 Workload ID（观测归属过滤的前缀锚）。
	for _, w := range calls[len(calls)-1].Spec {
		h.Runtime.ReportRunning(w.ID, capability.Generation(dep.GetDeployment().GetGeneration()))
	}
	require.Eventually(t, func() bool {
		got, err := dc.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == "observing"
	}, 5e9, 2e7)
	h.Clock.Advance(61 * time.Second)
	require.Eventually(t, func() bool {
		got, err := dc.GetDeployment(ctx, &deliveryv1.GetDeploymentRequest{Id: dep.GetDeployment().GetId()})
		return err == nil && got.GetDeployment().GetState() == "succeeded"
	}, 5e9, 2e7)
	return dep
}

// TestPreviewDeployFullChain：预览（真铸票面）→ 同请求带票 Deploy 全链成功
// （W2b 验烧的 mint 半边接真源）；STALE 腿（预览后世界漂移——变量行变化）。
func TestPreviewDeployFullChain(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "w2a-full").GetId(), "web").GetId()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	// 基线部署（skip）到 succeeded。
	w2aDeployAndWaitSucceeded(t, h, ctx, appID, &deliveryv1.DeployRequest{
		Confirmation: yesSkip(), AppId: appID, Image: "nginx:1.27", ProcessName: "web",
	})

	// 预览同一变更（真铸票面——票绑操作者/载荷/基线四锚）。
	preview, err := dc.PreviewDeploy(ctx, &deliveryv1.PreviewDeployRequest{Deploy: &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.31", ProcessName: "web",
	}})
	require.NoError(t, err)
	assert.NotEmpty(t, preview.GetTicket())
	assert.Equal(t, int32(600), preview.GetTicketExpiresIn())
	require.NotEmpty(t, preview.GetSpecDiff(), "image change must diff against the succeeded baseline")
	require.Len(t, preview.GetWorkloadChanges(), 1)
	assert.Equal(t, deliveryv1.WorkloadRollout_WORKLOAD_ROLLOUT_REPLACE, preview.GetWorkloadChanges()[0].GetRollout())

	// 带票 Deploy：④digest/⑤base 与本临界区冻结体比对成立 → 受理成功。
	adm, err := dc.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.31", ProcessName: "web",
		Confirmation: &deliveryv1.Confirmation{Ticket: preview.GetTicket()},
	})
	require.NoError(t, err, "the previewed request with the minted ticket must admit")
	assert.Equal(t, "queued", adm.GetAdmission().GetOutcome())

	// 同票重发：tombstone 辨因 → 已确认过 MISMATCH。
	_, err = dc.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.31", ProcessName: "web",
		Confirmation: &deliveryv1.Confirmation{Ticket: preview.GetTicket()},
	})
	require.Error(t, err)
	assert.Equal(t, "E_CONFIRMATION_MISMATCH", codeOf(t, err))

	// STALE 腿：新预览后改变量环境（服务端合成输入漂移归 STALE，#7）。
	preview2, err := dc.PreviewDeploy(ctx, &deliveryv1.PreviewDeployRequest{Deploy: &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.32", ProcessName: "web",
	}})
	require.NoError(t, err)
	vars := structurev1.NewAppVariablesServiceClient(h.Conn)
	_, err = vars.PutAppVariable(ctx, &structurev1.PutAppVariableRequest{
		AppId: appID, Name: "feature_flag", Value: "on",
	})
	require.NoError(t, err)
	_, err = dc.Deploy(ctx, &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.32", ProcessName: "web",
		Confirmation: &deliveryv1.Confirmation{Ticket: preview2.GetTicket()},
	})
	require.Error(t, err)
	assert.Equal(t, "E_CONFIRMATION_STALE", codeOf(t, err))
}

// TestPreviewDeployCELYieldsIdenticalRejections：CEL 零漂移断言（设计稿
// §1.3 形状层单源——同一非法载荷对 Deploy 与 PreviewDeploy 的 protovalidate
// 拒绝逐规则一致；预览侧的路径前缀差（嵌套字段名）归一后逐字节比对）。
func TestPreviewDeployCELYieldsIdenticalRejections(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "w2a-cel").GetId(), "web").GetId()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	invalid := []*deliveryv1.DeployRequest{
		{AppId: appID, Image: "nginx:1.27", ComposeYaml: "services: {}\n"}, // 四源互斥
		{AppId: appID}, // 至少一源
		{AppId: appID, SpecFile: `{"processes":[{"name":"web","image":"nginx:1.27"}]}`, ProcessName: "web"}, // spec_file×单进程旗标
		{AppId: appID, Image: "nginx:1.27", HttpProbe: "/healthz", TcpProbe: 8080},                          // probe 互斥
		{AppId: appID, ComposeYaml: "services: {}\n", Env: map[string]string{"A": "B"}},                     // env×compose
	}
	normalize := strings.NewReplacer("deploy: ", "", "deploy.", "")
	for i, req := range invalid {
		_, derr := dc.Deploy(ctx, req)
		require.Error(t, derr, "case %d: Deploy must reject", i)
		assert.Equal(t, "E_INVALID_ARGUMENT", codeOf(t, derr), "case %d", i)
		_, perr := dc.PreviewDeploy(ctx, &deliveryv1.PreviewDeployRequest{Deploy: req})
		require.Error(t, perr, "case %d: PreviewDeploy must reject identically", i)
		assert.Equal(t, "E_INVALID_ARGUMENT", codeOf(t, perr), "case %d", i)
		assert.Equal(t, derr.Error(), normalize.Replace(perr.Error()),
			"case %d: the CEL rejection surface must be rule-identical (nested prefix normalized)", i)
	}
}

// TestPreviewDeployRedaction：脱敏红线断言（ADR-0071 决策 5 同款，设计稿
// §1.3 A1）——载荷零 env 值集（至多 name 集）、零 FLEETLY_DB_*（预览停在
// projectRevision，绝不进 materialize 段）、零代戳；载荷规模合理。
func TestPreviewDeployRedaction(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "w2a-redact").GetId(), "web").GetId()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	// 平台变量（合成输入——值不得进载荷）。
	vars := structurev1.NewAppVariablesServiceClient(h.Conn)
	_, err := vars.PutAppVariable(ctx, &structurev1.PutAppVariableRequest{
		AppId: appID, Name: "platform_secret", Value: "plat-val-w2a-do-not-leak",
	})
	require.NoError(t, err)

	body := `{"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27",` +
		`"env":{"SESSION_SECRET":"env-val-w2a-do-not-leak"},"database_refs":["pg"],"secret_refs":["creds"]}]}`
	preview, err := dc.PreviewDeploy(ctx, &deliveryv1.PreviewDeployRequest{Deploy: &deliveryv1.DeployRequest{
		AppId: appID, SpecFile: body,
	}})
	require.NoError(t, err)

	// env 名集可见、值面红acted（diff 输入侧先行——含空基线子树 blob 形态）。
	envNamed := false
	for _, e := range preview.GetSpecDiff() {
		if strings.Contains(e.GetNewValue(), "SESSION_SECRET:(redacted)") {
			envNamed = true
		}
	}
	require.True(t, envNamed, "the env name must appear in the diff with a redacted value")

	// 全载荷序列化：零 env 值/零平台变量值/零 FLEETLY_DB_*（材料注入红线
	// ——预览投影停在 projectRevision）/规模合理。
	raw, err := protojson.Marshal(preview)
	require.NoError(t, err)
	lower := strings.ToLower(string(raw))
	assert.Contains(t, string(raw), "SESSION_SECRET", "env names (the name set) stay visible")
	for _, banned := range []string{"env-val-w2a-do-not-leak", "plat-val-w2a-do-not-leak", "FLEETLY_DB_"} {
		assert.NotContains(t, lower, strings.ToLower(banned), "preview payload must not carry %q", banned)
	}
	assert.Less(t, len(raw), 64*1024, "preview payload stays summary-sized")
	// WorkloadChange 无 env 值面（摘要面：image/command/replicas/探针/卷/网）。
	for _, ch := range preview.GetWorkloadChanges() {
		assert.NotContains(t, ch.GetHealthcheck(), "do-not-leak")
	}
}

// TestPreviewDeployEmptyBaseline：首次部署空基线形态（设计稿 §1.3——全量
// added、渲染侧空集，不照抄 render 的 ErrNotFound 路径）。
func TestPreviewDeployEmptyBaseline(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "w2a-empty").GetId(), "web").GetId()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	preview, err := dc.PreviewDeploy(ctx, &deliveryv1.PreviewDeployRequest{Deploy: &deliveryv1.DeployRequest{
		AppId: appID, Image: "nginx:1.27", ProcessName: "web",
	}})
	require.NoError(t, err, "first-deploy preview is the all-added shape, not ErrNotFound")
	require.NotEmpty(t, preview.GetSpecDiff())
	for _, e := range preview.GetSpecDiff() {
		assert.Empty(t, e.GetOldValue(), "empty baseline: every entry is added")
	}
	require.Len(t, preview.GetWorkloadChanges(), 1)
	ch := preview.GetWorkloadChanges()[0]
	assert.Equal(t, deliveryv1.WorkloadChangeAction_WORKLOAD_CHANGE_ACTION_ADDED, ch.GetAction())
	assert.Equal(t, int64(0), ch.GetReplicasFrom())
	assert.Equal(t, int64(1), ch.GetReplicasTo())
}

// TestPreviewRolloutGolden：滚动后果判定三态 golden（zero_touch/scale_only/
// replace+策略——内容锚定等价纯 spec 侧，设计稿 §1.3）。
func TestPreviewRolloutGolden(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "w2a-roll").GetId(), "web").GetId()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	w2aDeployAndWaitSucceeded(t, h, ctx, appID, &deliveryv1.DeployRequest{
		Confirmation: yesSkip(), AppId: appID, Image: "nginx:1.27", ProcessName: "web",
	})

	preview := func(t *testing.T, req *deliveryv1.DeployRequest) *deliveryv1.PreviewDeployResponse {
		t.Helper()
		out, err := dc.PreviewDeploy(ctx, &deliveryv1.PreviewDeployRequest{Deploy: req})
		require.NoError(t, err)
		return out
	}
	var sb strings.Builder
	line := func(format string, args ...any) { sb.WriteString(fmt.Sprintf(format, args...) + "\n") }

	// zero_touch：同内容重放（内容等价跳过）。
	p := preview(t, &deliveryv1.DeployRequest{AppId: appID, Image: "nginx:1.27", ProcessName: "web"})
	line("== zero_touch ==")
	line("diff_entries=%d", len(p.GetSpecDiff()))
	for _, ch := range p.GetWorkloadChanges() {
		line("change %s action=%s rollout=%s image=%s replicas=%d->%d", ch.GetWorkloadId(), ch.GetAction(), ch.GetRollout(), ch.GetImage(), ch.GetReplicasFrom(), ch.GetReplicasTo())
	}

	// scale_only：仅副本异（spec_file 声明 replicas=3，镜像同）。
	p = preview(t, &deliveryv1.DeployRequest{AppId: appID, SpecFile: `{"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27","replicas":3}]}`})
	line("== scale_only ==")
	for _, ch := range p.GetWorkloadChanges() {
		line("change %s action=%s rollout=%s replicas=%d->%d", ch.GetWorkloadId(), ch.GetAction(), ch.GetRollout(), ch.GetReplicasFrom(), ch.GetReplicasTo())
	}

	// replace + blue-green 策略窗（镜像异 + 策略 BLUE_GREEN——策略面经
	// BLUE_GREEN_WINDOW warning 透出）。
	p = preview(t, &deliveryv1.DeployRequest{AppId: appID, SpecFile: `{"source":{"image":{"ref":"nginx:1.31"}},"processes":[{"name":"web","image":"nginx:1.31","strategy":"DEPLOY_STRATEGY_BLUE_GREEN"}]}`})
	line("== replace_blue_green ==")
	for _, ch := range p.GetWorkloadChanges() {
		line("change %s action=%s rollout=%s image=%s", ch.GetWorkloadId(), ch.GetAction(), ch.GetRollout(), ch.GetImage())
	}

	// removed：进程缺席于提案（REMOVED = 载体拆除）。
	p = preview(t, &deliveryv1.DeployRequest{AppId: appID, SpecFile: `{"source":{"image":{"ref":"busybox:1.37"}},"processes":[{"name":"other","image":"busybox:1.37"}]}`})
	line("== removed ==")
	for _, ch := range p.GetWorkloadChanges() {
		line("change %s action=%s rollout=%s replicas=%d->%d", ch.GetWorkloadId(), ch.GetAction(), ch.GetRollout(), ch.GetReplicasFrom(), ch.GetReplicasTo())
	}

	checkPreviewGolden(t, "preview-rollout.golden", strings.ReplaceAll(sb.String(), appID, "<app>"))
}

// TestPreviewWarningsGolden：三类预警 golden（ADMISSION_PRECHECK/
// IN_FLIGHT_DEPLOYMENT〔位次〕/LIVE_DRIFT〔文案〕）+ REPLICAS_CHANGE 随行。
func TestPreviewWarningsGolden(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "w2a-warn").GetId(), "web").GetId()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	w2aDeployAndWaitSucceeded(t, h, ctx, appID, &deliveryv1.DeployRequest{
		Confirmation: yesSkip(), AppId: appID, Image: "nginx:1.27", ProcessName: "web",
	})

	// LIVE_DRIFT：现场副本 2 ≠ 声明 1（2026-10-30 真机案例形态——手动扩
	// 副本被重部署回拉；B1 收掉的是惊讶不是收敛语义）。提案声明 replicas
	// 缺省 1（与基线一致——纯 drift 对照腿）。
	h.Runtime.SetObservedReplicas(appID+"-web", 2)

	// ADMISSION_PRECHECK：preDeployJobs 裸网名（入队即拒项——与 Submit
	// 单源谓词，预览期透出）。
	driftSpec := `{"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27"}],` +
		`"pre_deploy_jobs":[{"name":"migrate","ttl":"300s","process":{"image":"migrate:1","networks":["no-such-net"]}}]}`
	preview, err := dc.PreviewDeploy(ctx, &deliveryv1.PreviewDeployRequest{Deploy: &deliveryv1.DeployRequest{
		AppId: appID, SpecFile: driftSpec,
	}})
	require.NoError(t, err)

	var sb strings.Builder
	for _, w := range preview.GetWarnings() {
		sb.WriteString(w2aWarningLine(w) + "\n")
	}
	// IN_FLIGHT 位次腿：手动夹具（queued 停留）下预览——在途/排队计数与
	// 预期 outcome/位次。
	hm := apitest.NewManual(t)
	mctx := hm.Ctx()
	mApp := hm.CreateApp(t, hm.CreateProject(t, "w2a-inflight").GetId(), "web").GetId()
	mdc := deliveryv1.NewDeploymentsServiceClient(hm.Conn)
	_, err = mdc.Deploy(mctx, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: mApp, Image: "nginx:1.27", ProcessName: "web"})
	require.NoError(t, err)
	mp, err := mdc.PreviewDeploy(mctx, &deliveryv1.PreviewDeployRequest{Deploy: &deliveryv1.DeployRequest{
		AppId: mApp, Image: "nginx:1.31", ProcessName: "web",
	}})
	require.NoError(t, err)
	sb.WriteString("== inflight ==\n")
	for _, w := range mp.GetWarnings() {
		sb.WriteString(w2aWarningLine(w) + "\n")
	}
	checkPreviewGolden(t, "preview-warnings.golden", strings.ReplaceAll(sb.String(), appID, "<app>"))
}

// w2aWarningLine 渲染单条预警（kind 枚举名截尾 + detail 全文）。
func w2aWarningLine(w *deliveryv1.PreviewWarning) string {
	kind := strings.TrimPrefix(w.GetKind().String(), "PREVIEW_WARNING_KIND_")
	return "warning " + kind + ": " + w.GetDetail()
}

// TestPreviewStrictIsolateDiff：strict/isolate 口径差进断言（设计稿 §1.3
// ——部署链/预览提案恒 strict，渲染恒 isolate）：已成功基线挂 approved
// peer → 撤销后预览同内容——预览侧 ADMISSION_PRECHECK 透出未批准引用
// （strict 拒因），渲染侧（GetAppWorkloads isolate）剥离该网络。
func TestPreviewStrictIsolateDiff(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	networks := structurev1.NewNetworksServiceClient(h.Conn)
	receiver := h.CreateProject(t, "w2a-peer-recv")
	consumer := h.CreateProject(t, "w2a-peer-use")
	net, err := networks.CreateNetwork(ctx, &structurev1.CreateNetworkRequest{ProjectId: receiver.GetId(), Name: "bus"})
	require.NoError(t, err)
	declared, err := networks.DeclareNetworkPeer(ctx, &structurev1.DeclareNetworkPeerRequest{
		NetworkId: net.GetNetwork().GetId(), PeerProjectId: consumer.GetId(),
	})
	require.NoError(t, err)
	_, err = networks.ApproveNetworkPeer(ctx, &structurev1.ApproveNetworkPeerRequest{Id: declared.GetPeer().GetId()})
	require.NoError(t, err)

	appID := h.CreateApp(t, consumer.GetId(), "web").GetId()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	ref := "project:" + receiver.GetId() + "/bus"
	spec := `{"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27","networks":["` + ref + `"]}]}`
	w2aDeployAndWaitSucceeded(t, h, ctx, appID, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: appID, SpecFile: spec})

	// 撤销批准：渲染 isolate 剥离；预览 strict 透出拒因（入队即拒项）。
	_, err = networks.RevokeNetworkPeer(ctx, &structurev1.RevokeNetworkPeerRequest{Id: declared.GetPeer().GetId()})
	require.NoError(t, err)

	rc := deliveryv1.NewRevisionsServiceClient(h.Conn)
	live, err := rc.GetAppWorkloads(ctx, &deliveryv1.GetAppWorkloadsRequest{AppId: appID})
	require.NoError(t, err)
	require.Len(t, live.GetWorkloads(), 1)
	assert.NotContains(t, live.GetWorkloads()[0].GetNetworks(), ref,
		"isolate render strips the unapproved cross-project reference")

	preview, err := dc.PreviewDeploy(ctx, &deliveryv1.PreviewDeployRequest{Deploy: &deliveryv1.DeployRequest{
		AppId: appID, SpecFile: spec,
	}})
	require.NoError(t, err)
	found := false
	for _, w := range preview.GetWarnings() {
		if w.GetKind() == deliveryv1.PreviewWarningKind_PREVIEW_WARNING_KIND_ADMISSION_PRECHECK &&
			strings.Contains(w.GetDetail(), ref) {
			found = true
		}
	}
	assert.True(t, found, "strict preview surfaces the unapproved cross-project reference as a precheck warning")
}

// TestPreviewTeardownGoldenAndTOCTOU：拆除预演 golden（级联清单 + 保留
// 清单 + 驱动权）与 TOCTOU 腿（预览后加 route → 确认 STALE——route 集
// 锚补齐的验证面）。
func TestPreviewTeardownGoldenAndTOCTOU(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	apps := structurev1.NewAppsServiceClient(h.Conn)
	volumes := structurev1.NewVolumesServiceClient(h.Conn)
	routes := proxyv1.NewRoutesServiceClient(h.Conn)
	hooks := deliveryv1.NewHooksServiceClient(h.Conn)
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)

	project := h.CreateProject(t, "w2a-teardown")
	appID := h.CreateApp(t, project.GetId(), "web").GetId()
	_, err := volumes.CreateVolume(ctx, &structurev1.CreateVolumeRequest{ProjectId: project.GetId(), Name: "scratch"})
	require.NoError(t, err)

	// 生效基线：spec_file 挂卷（保留清单 VOLUME 腿的引用面）。
	w2aDeployAndWaitSucceeded(t, h, ctx, appID, &deliveryv1.DeployRequest{Confirmation: yesSkip(),
		AppId: appID, SpecFile: `{"source":{"image":{"ref":"nginx:1.27"}},"processes":[{"name":"web","image":"nginx:1.27","volumes":[{"volume_id":"scratch","target":"/data"}]}]}`,
	})
	// hook 行（级联第二腿）+ route 行（级联第三腿 + TOCTOU 锚）。
	_, err = hooks.SetGitHook(ctx, &deliveryv1.SetGitHookRequest{AppId: appID, Repo: "https://github.com/acme/w2a-web.git"})
	require.NoError(t, err)
	_, err = routes.CreateRoute(ctx, &proxyv1.CreateRouteRequest{
		ProjectId: project.GetId(), Host: "w2a.teardown.test", AppId: appID, Process: "web", Port: 8000,
	})
	require.NoError(t, err)

	preview, err := dc.PreviewTeardown(ctx, &deliveryv1.PreviewTeardownRequest{AppId: appID})
	require.NoError(t, err)
	require.NotEmpty(t, preview.GetTicket())
	assert.Equal(t, int32(600), preview.GetTicketExpiresIn())
	assert.False(t, preview.GetDrivingConflict())

	var sb strings.Builder
	for _, item := range preview.GetCascade() {
		sb.WriteString("cascade " + item.GetKind().String() + ": " + item.GetName() + "\n")
	}
	for _, item := range preview.GetKept() {
		sb.WriteString("kept " + item.GetKind().String() + ": " + item.GetReason() + "\n")
	}
	checkPreviewGolden(t, "preview-teardown.golden", strings.ReplaceAll(sb.String(), appID, "<app>"))

	// TOCTOU：预览后新增 route → 确认时 route 集锚漂移 → STALE（清单从未
	// 展示的删除被拒绝，设计稿 §1.4 #8）。
	_, err = routes.CreateRoute(ctx, &proxyv1.CreateRouteRequest{
		ProjectId: project.GetId(), Host: "late.teardown.test", AppId: appID, Process: "web", Port: 8000,
	})
	require.NoError(t, err)
	_, err = apps.DeleteApp(ctx, &structurev1.DeleteAppRequest{Id: appID, Confirmation: &deliveryv1.Confirmation{Ticket: preview.GetTicket()}})
	require.Error(t, err)
	assert.Equal(t, "E_CONFIRMATION_STALE", codeOf(t, err))
}

// TestPreviewTeardownDrivingConflict：驱动权冲突透出（三层同谓词预检的
// 只读版——冲突在场仍出清单，不拒答）。
func TestPreviewTeardownDrivingConflict(t *testing.T) {
	h := apitest.NewManual(t)
	ctx := h.Ctx()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	appID := h.CreateApp(t, h.CreateProject(t, "w2a-drive").GetId(), "web").GetId()
	_, err := dc.Deploy(ctx, &deliveryv1.DeployRequest{Confirmation: yesSkip(), AppId: appID, Image: "nginx:1.27", ProcessName: "web"})
	require.NoError(t, err)

	preview, err := dc.PreviewTeardown(ctx, &deliveryv1.PreviewTeardownRequest{AppId: appID})
	require.NoError(t, err)
	assert.True(t, preview.GetDrivingConflict())
	assert.NotEmpty(t, preview.GetDrivingDetail())
	assert.NotEmpty(t, preview.GetTicket(), "the preview still mints a ticket under a driving conflict")
}

// TestScaleProcessAPI：ScaleProcess 全链（受理 → 生效 Spec 冻结 replicas →
// 渲染面收敛；未知进程/无基线哨兵；豁免确认——无票直发合法）。
func TestScaleProcessAPI(t *testing.T) {
	h := apitest.New(t)
	ctx := h.Ctx()
	appID := h.CreateApp(t, h.CreateProject(t, "w2a-scale").GetId(), "web").GetId()
	dc := deliveryv1.NewDeploymentsServiceClient(h.Conn)
	rc := deliveryv1.NewRevisionsServiceClient(h.Conn)

	// 无成功基线：E_NO_BASELINE。
	_, err := dc.ScaleProcess(ctx, &deliveryv1.ScaleProcessRequest{AppId: appID, Process: "web", Replicas: 2})
	require.Error(t, err)
	assert.Equal(t, "E_NO_BASELINE", codeOf(t, err))

	w2aDeployAndWaitSucceeded(t, h, ctx, appID, &deliveryv1.DeployRequest{
		Confirmation: yesSkip(), AppId: appID, Image: "nginx:1.27", ProcessName: "web",
	})

	// 无票直发合法（Restart/Scale 豁免族）：受理 queued。
	resp, err := dc.ScaleProcess(ctx, &deliveryv1.ScaleProcessRequest{AppId: appID, Process: "web", Replicas: 2})
	require.NoError(t, err)
	assert.Equal(t, "queued", resp.GetAdmission().GetOutcome())

	// 推进到 succeeded 后渲染面 replicas=2（scale-only overlay 生效）。
	// scale 部署的 L1 观测由夹具上报（假 runtime 不自发观测；时钟只在
	// observing 落定后过窗——早过窗会把 L1 门判成超时回滚）。
	require.Eventually(t, func() bool {
		calls := h.Runtime.Calls()
		if len(calls) == 0 {
			return false
		}
		last := calls[len(calls)-1]
		w, ok := last.Spec["web"]
		if !ok || w.Replicas != 2 {
			return false
		}
		for _, wl := range last.Spec {
			h.Runtime.ReportRunning(wl.ID, last.Gen)
		}
		list, lerr := dc.ListDeployments(ctx, &deliveryv1.ListDeploymentsRequest{AppId: appID})
		if lerr != nil {
			return false
		}
		for _, d := range list.GetDeployments() {
			if d.GetState() == "observing" {
				return true
			}
		}
		return false
	}, 5e9, 2e7)
	h.Clock.Advance(61 * time.Second)
	require.Eventually(t, func() bool {
		ws, err := rc.GetAppWorkloads(ctx, &deliveryv1.GetAppWorkloadsRequest{AppId: appID})
		return err == nil && len(ws.GetWorkloads()) == 1 && ws.GetWorkloads()[0].GetReplicas() == 2
	}, 5e9, 2e7)

	// 未知进程：E_NOT_FOUND。
	_, err = dc.ScaleProcess(ctx, &deliveryv1.ScaleProcessRequest{AppId: appID, Process: "no-such", Replicas: 1})
	require.Error(t, err)
	assert.Equal(t, "E_NOT_FOUND", codeOf(t, err))
}
