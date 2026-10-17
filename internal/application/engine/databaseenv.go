package engine

// 托管库引用注入（ProcessSpec.database_refs → FLEETLY_DB_<NAME>_* env）：
// materialize 序列在 Ensure 前解析——值取凭证单源（credentials_ref Secret
// 的连接串，ADR-0029 决策 6）+ DatabaseDNSName 铸名，**不进 Spec/Revision/
// 日志**（ADR-0014，与 secret_refs 文件材料同纪律）。引用缺失 = 部署期
// 精确失败带名（创建托管实例的提示随行）。变量集：
//
//	FLEETLY_DB_<NAME>_URL / _HOST / _PORT / _USER / _PASSWORD / _DATABASE
//
// <NAME> = 引用名的 env 形态（kebab → UPPER_SNAKE）。

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// injectDatabaseEnv 按 spec 的 process database_refs 把 FLEETLY_DB_* 合并进
// Workload Env（在 Ensure 下发前；同进程多引用即多组前缀并存）。缺库/引擎
// 未知/凭证解不开都精确失败——静默缺变量会让 init job 的空值哨兵变成唯一
// 诊断面（torchwood compose 的 fail-loud 形态）。
func (e *Engine) injectDatabaseEnv(ctx context.Context, spec *specv1.AppSpec, ws []capability.Workload, projectID string) error {
	byName := make(map[string]*specv1.ProcessSpec, len(spec.GetProcesses()))
	for _, p := range spec.GetProcesses() {
		byName[p.GetName()] = p
	}
	for i := range ws {
		w := &ws[i] // 指针就地改（gosec G602 对 range 索引误报；applyVolumePinning 同形态）
		if err := e.applyDatabaseEnv(ctx, w, byName[w.Process], projectID); err != nil {
			return err
		}
	}
	return nil
}

// applyDatabaseEnv 对单 Workload 合并其进程声明的 FLEETLY_DB_* env。
func (e *Engine) applyDatabaseEnv(ctx context.Context, w *capability.Workload, p *specv1.ProcessSpec, projectID string) error {
	if p == nil || len(p.GetDatabaseRefs()) == 0 {
		return nil
	}
	for _, ref := range p.GetDatabaseRefs() {
		row, err := e.repos.Databases.GetByName(ctx, projectID, ref)
		if err != nil {
			return fmt.Errorf("database %q (database_refs): not found — create the managed database first (%w)", ref, err)
		}
		password, err := e.databasePassword(ctx, row)
		if err != nil {
			return fmt.Errorf("database %q (database_refs): %w", ref, err)
		}
		tpl, ok := dbtemplate.For(row.Engine)
		if !ok {
			return fmt.Errorf("database %q (database_refs): unknown engine %q", ref, row.Engine)
		}
		connURL, err := DatabaseConnectionURL(row.Engine, row.ID, password)
		if err != nil {
			return fmt.Errorf("database %q (database_refs): %w", ref, err)
		}
		host := DatabaseDNSName(row.ID)
		user, database := "", ""
		if u, perr := url.Parse(connURL); perr == nil {
			if u.User != nil {
				user = u.User.Username()
			}
			database = strings.TrimPrefix(u.Path, "/")
		}
		tok := dbEnvToken(ref)
		if w.Env == nil {
			w.Env = map[string]string{}
		}
		w.Env["FLEETLY_DB_"+tok+"_URL"] = connURL
		w.Env["FLEETLY_DB_"+tok+"_HOST"] = host
		w.Env["FLEETLY_DB_"+tok+"_PORT"] = strconv.Itoa(int(tpl.Meta().Port))
		w.Env["FLEETLY_DB_"+tok+"_USER"] = user
		w.Env["FLEETLY_DB_"+tok+"_PASSWORD"] = password
		w.Env["FLEETLY_DB_"+tok+"_DATABASE"] = database
	}
	return nil
}

// dbEnvToken 引用名的 env 形态（torchwood-pg → TORCHWOOD_PG）。
func dbEnvToken(ref string) string {
	return strings.ToUpper(strings.ReplaceAll(ref, "-", "_"))
}
