package engine

import (
	"context"
	"encoding/json"

	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// errorf 构造注册表错误码信封（引擎内部的统一错误出口；code 只允许注册
// 表内 E_ 码——apperr 构造期 fail-fast 保证）。
func errorf(code, format string, args ...any) *apperr.Error {
	return apperr.New(code, format, args...)
}

// appErrOf 提取错误的信封形态：*apperr.Error 原样（补 deployment 归属）；
// 其余按 E_RUNTIME_UNAVAILABLE 兜底（引擎只产出注册码错误——兜底路径防御
// 非信封错误外溢）。
func appErrOf(err error, deploymentID string) *apperr.Error {
	var ae *apperr.Error
	if asAppErr(err, &ae) && ae != nil {
		return ae.WithDeploymentID(deploymentID)
	}
	return errorf("E_RUNTIME_UNAVAILABLE", "%v", err).WithDeploymentID(deploymentID)
}

// asAppErr 是 errors.As 的窄化包装（引擎只关心 *apperr.Error）。
func asAppErr(err error, target **apperr.Error) bool {
	for err != nil {
		if e, ok := err.(*apperr.Error); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// appendEvent 在事务内追加平台事件（Outbox：与业务写同事务；payload 由 kv
// 构造脱敏 JSON——值只允许字符串，secret 永不进事件，state-model §2.9）。
func appendEvent(ctx context.Context, tx *state.Tx, name, subject string, kv ...string) error {
	payload := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		payload[kv[i]] = kv[i+1]
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		raw = []byte("{}")
	}
	_, err = tx.AppendEvent(ctx, state.Event{
		Name:    name,
		Subject: subject,
		Payload: string(raw),
	})
	return err
}

// deploymentEvent / appEvent / placementEvent 是事件主体便捷形态。
func deploymentEvent(ctx context.Context, tx *state.Tx, name, deploymentID string, kv ...string) error {
	return appendEvent(ctx, tx, name, "deployment:"+deploymentID, append([]string{"deployment", deploymentID}, kv...)...)
}

func appEvent(ctx context.Context, tx *state.Tx, name, appName string, kv ...string) error {
	return appendEvent(ctx, tx, name, "app:"+appName, append([]string{"app", appName}, kv...)...)
}

func placementEvent(ctx context.Context, tx *state.Tx, name, appName, appID, reason string) error {
	return appendEvent(ctx, tx, name, "app:"+appName,
		"app", appName, "app_id", appID, "reason", reason)
}

// auditDeployment 在事务内写部署审计行（fail-closed：与业务写同事务）。
func auditDeployment(ctx context.Context, tx *state.Tx, actor, action, deploymentID, result, errorCode, diff string) error {
	return tx.WriteAudit(ctx, state.AuditEntry{
		Actor:       actor,
		Action:      action,
		Target:      "deployment:" + deploymentID,
		Result:      result,
		ErrorCode:   errorCode,
		DiffSummary: diff,
	})
}
