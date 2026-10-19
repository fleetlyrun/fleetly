package apitest_test

// 资源轴事实 fail-closed（R08-M8，S14-c7）：Project 归属解析失败（行被删/
// 存储故障）→ 写动词整单拒绝，零 secret 行/事件/审计；平台级事实（无 Team
// 轴）仍按显式口径对全体 Team 可见。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/apptemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/structure/project"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// failingProjectRepo 只覆写 Get（注入归属解析故障；其余方法走嵌入接口）。
type failingProjectRepo struct {
	project.Repo
	err error
}

func (f failingProjectRepo) Get(context.Context, string) (*project.Project, error) {
	return nil, f.err
}

// factBounds 记录事件/审计面行数（拒绝零副作用断言）。
func factBounds(t *testing.T, h *apitest.Harness) (seq int64, audits int) {
	t.Helper()
	seq, err := h.Services.OutboxEvents.LastSeq(context.Background())
	require.NoError(t, err)
	entries, err := h.Services.Audits.List(context.Background(), 1000)
	require.NoError(t, err)
	return seq, len(entries)
}

func TestFactOwnershipFailureRejectsWriteWithoutFacts(t *testing.T) {
	h := apitest.NewManual(t)
	owner := h.Ctx()
	projectID := h.CreateProject(t, "factclose").GetId()
	secrets := structurev1.NewSecretsServiceClient(h.Conn)

	// 基线：正常 put 成功（事件/审计落行）。
	_, err := secrets.PutSecret(owner, &structurev1.PutSecretRequest{ProjectId: projectID, Name: "baseline", Value: "v"})
	require.NoError(t, err)
	seqBefore, auditsBefore := factBounds(t, h)

	// 注入归属解析故障（等价于 Project 行在授权后被删/DB 瞬时故障）：写动词
	// 整单拒绝——E_INTERNAL，且 secret 行/事件/审计均不落（R08-M8 fail-closed）。
	h.Services.Projects = failingProjectRepo{Repo: h.Services.Projects, err: errors.New("injected ownership failure")}
	_, err = secrets.PutSecret(owner, &structurev1.PutSecretRequest{ProjectId: projectID, Name: "leaked", Value: "v"})
	require.Error(t, err)
	assert.Equal(t, codes.Internal, status.Code(err), "unresolvable event ownership must reject the write")

	list, err := secrets.ListSecrets(owner, &structurev1.ListSecretsRequest{ProjectId: projectID})
	require.NoError(t, err)
	names := map[string]bool{}
	for _, s := range list.GetSecrets() {
		names[s.GetName()] = true
	}
	assert.True(t, names["baseline"])
	assert.False(t, names["leaked"], "denied write must not persist")

	seqAfter, auditsAfter := factBounds(t, h)
	assert.Equal(t, seqBefore, seqAfter, "denied write must not emit outbox rows")
	assert.Equal(t, auditsBefore, auditsAfter, "denied write must not append audit rows")
}

// TestHardDeletedProjectRejectsWrite：Project 行被硬删（归属查询 ErrNotFound）
// → 写动词在受理面拒绝且零副作用（不留 Team 空事实）。
func TestHardDeletedProjectRejectsWrite(t *testing.T) {
	h := apitest.NewManual(t)
	owner := h.Ctx()
	projectID := h.CreateProject(t, "gone").GetId()
	secrets := structurev1.NewSecretsServiceClient(h.Conn)
	seqBefore, auditsBefore := factBounds(t, h)

	_, err := h.DB.Runner().ExecContext(context.Background(), `DELETE FROM projects WHERE id = ?`, projectID)
	require.NoError(t, err)

	_, err = secrets.PutSecret(owner, &structurev1.PutSecretRequest{ProjectId: projectID, Name: "ghost", Value: "v"})
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err), "a hard-deleted project is rejected before any write")

	seqAfter, auditsAfter := factBounds(t, h)
	assert.Equal(t, seqBefore, seqAfter)
	assert.Equal(t, auditsBefore, auditsAfter)
}

// TestPlatformFactsRemainTeamVisible：平台级事实（无 Team 轴，经
// platformFact 显式构造）仍对全体 Team 可见；资源轴事件不外溢。
func TestPlatformFactsRemainTeamVisible(t *testing.T) {
	h := apitest.NewManual(t)
	owner := h.Ctx()
	// 第二个 Team（过滤口径的观测方）。
	teamB, err := identityv1.NewTeamsServiceClient(h.Conn).CreateTeam(owner, &identityv1.CreateTeamRequest{Name: "beta"})
	require.NoError(t, err)

	// 平台级事件：模板目录刷新（platformFact；team 留 ''）。
	builtin, err := apptemplate.Builtin()
	require.NoError(t, err)
	nginx := builtin[0]
	for _, e := range builtin {
		if e.Name == "nginx" {
			nginx = e
		}
	}
	manifest, err := json.Marshal(apptemplate.Manifest{Version: 1, Templates: []apptemplate.Entry{nginx}})
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(manifest)
	}))
	defer srv.Close()
	h.Services.TemplatesCatalogURL = srv.URL
	_, err = deliveryv1.NewTemplatesServiceClient(h.Conn).RefreshTemplates(owner, &deliveryv1.RefreshTemplatesRequest{})
	require.NoError(t, err)
	const event = "template.refreshed"

	// 资源轴事件：owner 在 default Team 建项目（team=default）。
	resourceProject := h.CreateProject(t, "team-a-only").GetId()

	// 以 Team B 的可见域过滤：平台级行可见、他队资源轴行不可见。
	evts, err := h.Services.OutboxEvents.ListAfter(context.Background(), 0, 1000, teamB.GetTeam().GetId())
	require.NoError(t, err)
	seenPlatform, seenForeign := false, false
	for _, ev := range evts {
		if ev.Name == event {
			seenPlatform = true
		}
		if ev.Aggregate == "project" && ev.AggregateID == resourceProject {
			seenForeign = true
		}
	}
	assert.True(t, seenPlatform, "platform-level fact (TeamID empty) stays visible to every team")
	assert.False(t, seenForeign, "resource-axis fact of another team must not leak")
}
