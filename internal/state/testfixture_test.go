package state

// v0.3 W2-S3 归属管道的包内测试夹具（归属必填——资产行创建前的两步播种
// 收敛于此）。
//
// 播种走**裸 SQL 直插**（绕过 CreateTeam/CreateProject 的审计与事件）——
// 大量事件计数断言（EnterPhase/EnterDbPhase/部署入队单写点测试）以「播种
// 零事件」为前提；团队/项目行的写入通道本身由 teams_test/projects_test 的
// 正路测试覆盖，此处不重复。

import (
	"context"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"
)

// seedFixtureProject 播种独立团队 + 项目（返回项目行；零事件零审计）。
func seedFixtureProject(t *testing.T, st *Store) Project {
	t.Helper()
	suffix := strings.ToLower(ulid.Make().String())[10:26]
	teamID, projID := ulid.Make().String(), ulid.Make().String()
	if err := st.InTx(context.Background(), func(tx *Tx) error {
		if _, err := tx.ExecContext(context.Background(),
			`INSERT INTO teams (id, slug, name, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
			teamID, "t"+suffix, "fixture team", "fixture", nowNano()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(context.Background(),
			`INSERT INTO projects (id, team_id, slug, name, description, created_at) VALUES (?, ?, ?, ?, '', ?)`,
			projID, teamID, "p"+suffix, "fixture project", nowNano()); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed fixture project: %v", err)
	}
	return Project{ID: projID, TeamID: teamID, Slug: "p" + suffix, Name: "fixture project"}
}

// seedApp 在独立夹具项目下播种应用行（err 恒 nil——失败 t.Fatalf）。
func seedApp(t *testing.T, st *Store, name string) App {
	t.Helper()
	proj := seedFixtureProject(t, st)
	app, err := st.CreateApp(context.Background(), "", name, proj.ID, proj.TeamID)
	if err != nil {
		t.Fatalf("seed app %s: %v", name, err)
	}
	return app
}

// seedAppE 是 seedApp 的 (app, nil error) 签名形态（旧 CreateApp 三参调用
// 的机械迁移通道——既有 if err != nil 守卫保持合法且永不触发）。
func seedAppE(t *testing.T, st *Store, name string) (App, error) {
	t.Helper()
	return seedApp(t, st, name), nil
}
