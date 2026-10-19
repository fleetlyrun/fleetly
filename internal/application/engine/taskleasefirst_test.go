package engine

// Owner Lease 首次布防（R04-M1，S16-c8）：resident + 显式属主的行在首拍
// 即铸 LeaseDeadline——"创建后从不续期"（客户端崩溃/自动化只建不续）必须
// 在 deadline + 宽限后排空回收，而不是永久保活（ADR-0012 的失联回收从
// 创建起生效）。既有用例都是先 RenewTask 或直插 UpdateLease 再推时钟，
// 创建后不续期的形态此前无覆盖。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fleetlyrun/fleetly/internal/domain/automation/task"
)

func TestTaskLeaseArmedOnFirstDrive(t *testing.T) {
	e, _, clock := newTestEngine(t)
	ctx := context.Background()
	taskID := "01JD0TASK000000000000000AR"
	spec := fmt.Sprintf(`{"schema_version":1,"task":{"id":"%s","project":"%s"},`+
		`"process":{"name":"run","image":"busybox:1.37","command":["/worker"]},`+
		`"desired_concurrency":1,"form":"resident"}`, taskID, tTaskProject)
	// 直接落行（不给 LeaseDeadline）：模拟创建受理面的形态——租约首铸的
	// 责任在引擎收敛环（首拍布防）。
	require.NoError(t, e.repos.Tasks.Create(ctx, &task.Task{
		ID: taskID, ProjectID: tTaskProject, Name: "pool", Form: task.FormResident,
		State: task.StateActive, Spec: []byte(spec), DesiredConcurrency: 1,
		OwnerTokenID: "01JD0TOKEN0000000000000AR", DNSName: TaskDNSName(taskID),
	}))

	e.taskStep(ctx)
	armed := getTaskRow(t, e, taskID)
	require.NotEmpty(t, armed.LeaseDeadline,
		"a resident task with an explicit owner must be armed on its first drive (create-without-renew must expire)")
	assert.Equal(t, task.StateActive, armed.State, "arming the lease must not drain a live pool in the same tick")

	// 不续期 → deadline + 宽限后 → 排空（lease_expired 语义）。
	clock.Advance(e.opts.Task.TaskLeaseInterval + e.opts.Task.TaskLeaseGrace + time.Second)
	e.taskStep(ctx)
	assert.Equal(t, task.StateDraining, getTaskRow(t, e, taskID).State,
		"a never-renewed resident pool must drain after deadline+grace")
	assert.Contains(t, eventNames(t, e, taskID), "lease.expired")
}
