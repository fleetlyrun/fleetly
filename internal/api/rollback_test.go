package api

// RollbackDeployment 的引用寻址回归（2026-09-25 teamadmin 走查实录）：
// 原始引用（Console 主路径的路由参数 = 平台 id）透传引擎使既有应用的回滚
// 恒 E_ROLLBACK_NO_TARGET 且文案裸显平台 id（「app <ULID> does not
// exist」）。修复后 handler 把解析后的三段限定形下发引擎（GetAppByName
// 兼容限定形——跨项目同名形态不落歧义面），回滚正常入队且入队行归属寻址
// 的 app。

import (
	"context"
	"encoding/json"
	"testing"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/engine"
	"github.com/fleetlyrun/fleetly/internal/state"
)

func TestRollbackDeploymentResolvesAppReferenceWithoutRawID(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	st := env.st

	app, err := st.CreateApp(ctx, "", "demo", env.fixtureProject.ID, env.fixtureProject.TeamID)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}
	// 第二团队/项目下同名 app——staging demo 同款跨项目重名形态（引擎按名
	// 重解析的歧义面；限定形寻址免疫）。
	dup := seedDuplicateNameApp(t, st, app.Name)

	// 回滚目标面：revision 行 + 该 revision 的 succeeded 部署快照行（入队
	// 时 resolveRollbackTarget 取最新 revision、rollbackDeployments 反查
	// 可重放 succeeded 部署——两者缺一回滚恒 E_ROLLBACK_NO_TARGET）。
	var revID string
	if err := st.InTx(ctx, func(tx *state.Tx) error {
		rev, rerr := tx.CreateRevision(ctx, state.RevisionWrite{
			AppID:             app.ID,
			ComposeNormalized: "{}",
			Overlay:           "{}",
			DesiredHash:       "seeded-a",
		})
		revID = rev.ID
		return rerr
	}); err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	spec := engine.ServiceSpec{
		Name:     "fleetly-tfixture-fixture-demo-web",
		Image:    "nginx:1.27",
		Replicas: 1,
		ServiceLabels: map[string]string{
			state.LabelManaged: state.ManagedLabelValue,
			state.LabelApp:     app.QualifiedName(),
		},
	}
	raw, err := json.Marshal([]engine.ServiceSpec{spec})
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	ct, err := env.box.Encrypt(raw)
	if err != nil {
		t.Fatalf("encrypt snapshot: %v", err)
	}
	rec, err := st.CreateDeployment(ctx, state.DeployRecord{
		AppID:       app.ID,
		AppName:     app.Name,
		Kind:        "deploy",
		RevisionID:  revID,
		DesiredSpec: string(ct),
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	if err := st.InTx(ctx, func(tx *state.Tx) error {
		// revision_id 由引擎成功时回填（CreateDeployment 恒落 NULL）——夹具
		// 与 status 一并直写，构造「succeeded + revision 可重放」形态。
		_, err := tx.ExecContext(ctx,
			`UPDATE deployments SET status = 'succeeded', revision_id = ? WHERE id = ?`,
			revID, rec.ID)
		return err
	}); err != nil {
		t.Fatalf("seed succeeded row: %v", err)
	}

	cl := serverv1.NewDeploymentsServiceClient(env.conn)
	refs := map[string]string{
		"id":        app.ID,
		"qualified": app.QualifiedName(),
	}
	for label, ref := range refs {
		resp, err := cl.RollbackDeployment(authCtx(ctx, env.admTok), &serverv1.RollbackDeploymentRequest{App: ref})
		if err != nil {
			// 修复前 id 形态：引擎 GetAppByName(<ULID>) miss →
			// E_ROLLBACK_NO_TARGET「app <ULID> does not exist」（文案裸显
			// 平台 id）。
			t.Fatalf("[%s] RollbackDeployment(%q): %v", label, ref, err)
		}
		if resp.GetDeploymentId() == "" || resp.GetStatus() != string(state.DeployQueued) {
			t.Fatalf("[%s] rollback receipt = %+v, want queued deployment", label, resp)
		}
		rec, err := st.GetDeployment(ctx, resp.GetDeploymentId())
		if err != nil {
			t.Fatalf("[%s] load rollback row: %v", label, err)
		}
		if rec.AppID != app.ID || rec.Kind != "rollback" {
			t.Fatalf("[%s] rollback row app=%s kind=%s, want app %s kind rollback", label, rec.AppID, rec.Kind, app.ID)
		}
		if rec.RecoveryOf == "" {
			t.Fatalf("[%s] rollback row recovery_of empty, want the succeeded source deployment", label)
		}
	}
	// 重名兄弟行零写入（限定形寻址把回滚行落在寻址的 app 上，不串流）。
	siblingRows, err := st.ListAppDeployments(ctx, dup.ID, 10)
	if err != nil {
		t.Fatalf("list duplicate sibling deployments: %v", err)
	}
	if got := len(siblingRows); got != 0 {
		t.Fatalf("duplicate sibling deployments = %d, want 0 (rollback addressed the right app row)", got)
	}
}
