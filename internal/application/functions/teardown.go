// teardown.go 是 Function 聚合收口（设计 v3 §4.3 收口序②；TeardownDatabase
// 同族）：per 载体拆除 + 缓存清理。行 tombstone 与 function_revisions 终局
// 全清由 API 受理事务承载（任务④）——先拆后落账（ADR-0023 序语义）。
package functions

import (
	"context"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// TeardownFunction 拆除函数载体并清理缓存（幂等重入安全：Remove 幂等，
// 缓存清理对缺席键零操作）。
func (f *Functions) TeardownFunction(ctx context.Context, id string) error {
	row, err := f.repos.Functions.Get(ctx, id)
	if err != nil {
		return err // ErrNotFound → API 404（TeardownDatabase 同口径）
	}
	team, err := f.projectTeam(ctx, row.ProjectID)
	if err != nil {
		return err
	}
	ns := capability.NamespaceRef{Team: team, Project: row.ProjectID, Function: row.ID, FunctionName: row.Name}
	// 维护互斥读半边（ADR-0046）：Remove 是载体写动词，与网络重建全序
	// 串行——半拆网与半拆载体不得交错。锁等待不占步预算。
	unlockMaintenance := f.lockMaintenance()
	defer unlockMaintenance()
	// Remove 带界（API 请求路径挂死会卡住调用本身）。
	rctx, rcancel := f.boundedStep(ctx)
	removeErr := f.runtime.Remove(rctx, ns)
	rcancel()
	if removeErr != nil {
		return fmt.Errorf("function teardown: %w", removeErr)
	}
	f.forgetObservation(row.ID)
	f.forgetExpected(row.ID)
	f.ensureForget(f.ensure, row.ID) // 签名随域收口作废（同 ID 永不复用，防御性清理）
	return nil
}
