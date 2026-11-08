package cmd

// apps deployments/deployment golden（立案②，staging 记录·六十二发现②/③）：
// apps 组的部署行观测面——list 双形态（ID/STATE/GEN/ERROR 行表，error 截
// 断 ~80）+ get 双形态（error 完整正文）；空列表与未知 app 拒绝面。行经
// domain repo 直插（builds logs golden 同款先例）——读面只消费行，不经引
// 擎状态机（时钟/驱动次数零敏感，golden 逐字确定）。

import (
	"context"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/model/entityid"
	deploymentrepo "github.com/fleetlyrun/fleetly/internal/infrastructure/persistence/deployment"
	"github.com/fleetlyrun/fleetly/internal/testsupport/apitest"
)

// seedDeploymentLine 在 App 名下直插一条部署行并迁移到终态 state（mut 落
// error 等 Transit 可变字段），返回部署 ID。出生态恒 queued（admission
// 受理形态）；Create 自铸 "<app>-d<seq>" ID 与 seq（ADR-0092/0070）。
func seedDeploymentLine(t *testing.T, h *apitest.Harness, repo deployment.Repo, appID, fromRev, toRev string,
	gen, fromGen uint64, terminal deployment.State, mut func(*deployment.Deployment)) string {
	t.Helper()
	d := &deployment.Deployment{
		AppID: appID, FromRevision: fromRev, ToRevision: toRev,
		State:      deployment.StateQueued,
		Generation: gen, FromGeneration: fromGen,
	}
	if err := repo.Create(context.Background(), d); err != nil {
		t.Fatalf("seed deployment row: %v", err)
	}
	if terminal != deployment.StateQueued {
		if err := repo.Transit(context.Background(), d.ID,
			[]deployment.State{deployment.StateQueued}, terminal, mut); err != nil {
			t.Fatalf("seed deployment terminal transit: %v", err)
		}
	}
	return d.ID
}

func TestGoldenAppsDeployments(t *testing.T) {
	h := newGoldenHarness(t)
	code, out, stderr := runCLI(t, "projects", "create", "appdeps")
	if code != 0 || stderr != "" {
		t.Fatalf("seed project: code=%d stderr=%q", code, stderr)
	}
	projectID := extractTailID(out)
	code, out, stderr = runCLI(t, "apps", "create", "--project", projectID, "web")
	if code != 0 || stderr != "" {
		t.Fatalf("seed app: code=%d stderr=%q", code, stderr)
	}
	appID := extractTailID(out)

	// 行集钉 staging 排查形态（记录·六十二发现③的 d 序叙事）：succeeded
	// 基线 → 两笔 failed（实因各异——短错误原文进列、长错误截断）→ 一笔
	// queued 在途。revision 引用走确定性派生律（entityid.Revision）。
	repo := deploymentrepo.New(h.Clock, h.DB.Runner())
	r1 := entityid.Revision(appID, 1)
	seedDeploymentLine(t, h, repo, appID, "", r1, 1, 0, deployment.StateSucceeded, nil)
	seedDeploymentLine(t, h, repo, appID, r1, entityid.Revision(appID, 2), 2, 1,
		deployment.StateFailed, func(m *deployment.Deployment) {
			m.Error = "health gate L1 timed out waiting for workloads to become ready"
		})
	longFailedID := seedDeploymentLine(t, h, repo, appID, r1, entityid.Revision(appID, 3), 3, 1,
		deployment.StateFailed, func(m *deployment.Deployment) {
			// >80 字符：list 列截断形态的载体（全文由 get 形 golden 钉）。
			m.Error = `resolve app project spec: volume "uploads-data" is not defined in this project (legacy-named keys were replaced by ULID ids; recreate the volume or update the declaration)`
		})
	seedDeploymentLine(t, h, repo, appID, r1, entityid.Revision(appID, 4), 4, 1,
		deployment.StateQueued, nil)

	code, out, stderr = runCLI(t, "apps", "deployments", "--app", appID)
	if code != 0 || stderr != "" {
		t.Fatalf("apps deployments: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "apps-deployments", normalizeGolden(out))

	code, out, stderr = runCLI(t, "apps", "deployments", "--app", appID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps deployments --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "apps-deployments-json", normalizeGolden(out))

	// get 形：长错误行的全文下钻（正文不截断 + from/to revision + 时间戳）。
	code, out, stderr = runCLI(t, "apps", "deployment", longFailedID)
	if code != 0 || stderr != "" {
		t.Fatalf("apps deployment: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "apps-deployment", normalizeGolden(out))

	code, out, stderr = runCLI(t, "apps", "deployment", longFailedID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("apps deployment --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "apps-deployment-json", normalizeGolden(out))

	// 空列表：从未部署的 App——表头独占的诚实形态。
	code, out, stderr = runCLI(t, "apps", "create", "--project", projectID, "bare")
	if code != 0 || stderr != "" {
		t.Fatalf("seed bare app: code=%d stderr=%q", code, stderr)
	}
	bareID := extractTailID(out)
	code, out, stderr = runCLI(t, "apps", "deployments", "--app", bareID)
	if code != 0 || stderr != "" {
		t.Fatalf("apps deployments (empty): code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "apps-deployments-empty", normalizeGolden(out))

	// 未知 app 拒绝：铸造形 ULID（小写字母域）短路本地解析，服务端行锚
	// E_NOT_FOUND 信封（error_id 随机，normalize 占位化）。--json 轮同信封
	//（错误面与输出形态无关）。
	const unknownAppID = "01aaaaaaaaaaaaaaaaaaaaaaaa"
	code, out, stderr = runCLI(t, "apps", "deployments", "--app", unknownAppID)
	if code == 0 || out != "" {
		t.Fatalf("apps deployments (unknown app): code=%d out=%q stderr=%q", code, out, stderr)
	}
	compareGolden(t, "apps-deployments-unknown-app", normalizeGolden(stderr))

	code, out, stderr = runCLI(t, "apps", "deployments", "--app", unknownAppID, "--json")
	if code == 0 || out != "" {
		t.Fatalf("apps deployments --json (unknown app): code=%d out=%q stderr=%q", code, out, stderr)
	}
	compareGolden(t, "apps-deployments-unknown-app-json", normalizeGolden(stderr))
}
