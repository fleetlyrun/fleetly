package api

// ExecService（Web 终端受理面，E7 W5-S6）单测：功能开关门
//（E_TERMINAL_DISABLED——terminal.enabled=false 的诚实拒绝）、app 存在性、
// ticket 绑定（调用方 token + app/service）与响应投影。scope 矩阵
//（terminal 独立 scope、admin 蕴含）在 auth_test.go 的登记断言覆盖。

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	serverv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/server/v1"
	"github.com/fleetlyrun/fleetly/internal/apperr"
	"github.com/fleetlyrun/fleetly/internal/execrelay"
	"github.com/fleetlyrun/fleetly/internal/secrets"
	"github.com/fleetlyrun/fleetly/internal/state"
)

// fakeTerminalTasks 是 execrelay.TaskSource 的最小假实现（本面不触底座——
// ticket 受理不解析任务）。
type fakeTerminalTasks struct{}

func (fakeTerminalTasks) ListTaskRuntimes(_ context.Context, _ string) ([]execrelay.TaskRuntime, error) {
	return nil, nil
}

func (fakeTerminalTasks) NodeHostnames(_ context.Context) (map[string]string, error) {
	return nil, nil
}

func newTerminalTestEnv(t *testing.T, enabled bool) (*ExecService, *state.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := state.Open(context.Background(), filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("state.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, _, err := secrets.EnsureKey(filepath.Join(dir, "test.key")); err != nil {
		t.Fatalf("EnsureKey: %v", err)
	}
	hub := execrelay.NewHub(execrelay.HubConfig{
		Store:   st,
		Tasks:   fakeTerminalTasks{},
		Tickets: execrelay.NewTicketStore(0),
		Enabled: func() bool { return enabled },
	})
	return NewExecService(st, hub), st
}

func principalCtx(tokenID string) context.Context {
	return context.WithValue(context.Background(), principalKey{}, Principal{TokenID: tokenID, Scopes: []string{ScopeAdmin}})
}

func TestExecServiceDisabledRejects(t *testing.T) {
	svc, _ := newTerminalTestEnv(t, false)
	_, err := svc.CreateTerminalTicket(principalCtx("tok1"),
		&serverv1.CreateTerminalTicketRequest{App: "demo", Service: "web"})
	ae, ok := errAsAppErr(err)
	if !ok || ae.Code() != "E_TERMINAL_DISABLED" {
		t.Fatalf("err = %v, want E_TERMINAL_DISABLED", err)
	}
}

func TestExecServiceAppNotFound(t *testing.T) {
	svc, _ := newTerminalTestEnv(t, true)
	_, err := svc.CreateTerminalTicket(principalCtx("tok1"),
		&serverv1.CreateTerminalTicketRequest{App: "nosuch", Service: "web"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want app-not-found 404 形态", err)
	}
}

func TestExecServiceTicketIssuedAndBound(t *testing.T) {
	svc, st := newTerminalTestEnv(t, true)
	ctx := context.Background()
	if _, err := st.CreateApp(ctx, "", "demo"); err != nil {
		t.Fatalf("seed app: %v", err)
	}
	resp, err := svc.CreateTerminalTicket(principalCtx("tok-e2e"),
		&serverv1.CreateTerminalTicketRequest{App: "demo", Service: "web"})
	if err != nil {
		t.Fatalf("CreateTerminalTicket: %v", err)
	}
	if resp.GetTicket() == "" || resp.GetExpiresAt() == nil {
		t.Fatalf("response missing ticket material: %+v", resp)
	}
	if resp.GetExpiresInSeconds() != execrelay.TicketTTLSeconds {
		t.Fatalf("expires_in_seconds = %d, want %d", resp.GetExpiresInSeconds(), execrelay.TicketTTLSeconds)
	}
	if resp.GetWebsocketPath() == "" || !strings.HasPrefix(resp.GetWebsocketPath(), execrelay.TerminalWSPath+"?ticket=") {
		t.Fatalf("websocket_path = %q", resp.GetWebsocketPath())
	}
	// 绑定校验：hub 的 ticket 表 redeem 出三元组。
	b, err := svc.hub.Tickets().Redeem(resp.GetTicket())
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if b.TokenID != "tok-e2e" || b.App != "demo" || b.Service != "web" {
		t.Fatalf("binding = %+v, want tok-e2e/demo/web", b)
	}
}

// errAsAppErr 提取 apperr 信封（errors.As 窄化）。
func errAsAppErr(err error) (*apperr.Error, bool) {
	var ae *apperr.Error
	ok := errors.As(err, &ae)
	return ae, ok && ae != nil
}
