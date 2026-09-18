package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/edgesets/edgefleet/internal/naming"
	"github.com/edgesets/edgefleet/internal/secrets"
	"github.com/edgesets/edgefleet/internal/state"
)

// newStateEnv 是 env/placement/nodes CLI 测试的独立环境：临时库 + 临时密钥
// + 既有应用。返回 db 路径、key 路径、应用名。
func newStateEnv(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	key := filepath.Join(dir, "edgefleet.key")
	st, err := state.Open(context.Background(), db)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.CreateApp(context.Background(), "", "my-api"); err != nil {
		t.Fatalf("create app: %v", err)
	}
	return db, key, "my-api"
}

// newKey 在临时路径生成主密钥（CLI 测试的密钥前置）。
func newKey(t *testing.T, path string) {
	t.Helper()
	if _, _, err := secrets.EnsureKey(path); err != nil {
		t.Fatalf("ensure key: %v", err)
	}
}

// TestCLIEnvSetListGetRm 验收 3/6：env 全生命周期——set 出 pending 回执、
// list 脱敏且标注状态位、get 显式读回明文、rm 删除；值不出现在 list 输出。
func TestCLIEnvSetListGetRm(t *testing.T) {
	db, key, app := newStateEnv(t)
	newKey(t, key)

	// set → pending 回执。
	code, out, errOut := runCLI(t, "env", "set", "--db", db, "--key", key, app, "DATABASE_URL", "postgres://secret-conn")
	if code != 0 {
		t.Fatalf("set: code=%d\nstdout=%s\nstderr=%s", code, out, errOut)
	}
	if !strings.Contains(out, "pending") || !strings.Contains(out, "DATABASE_URL") {
		t.Fatalf("set output = %q", out)
	}

	// list → 键名 + pending；值脱敏（负面断言：明文不出现在输出流）。
	code, out, errOut = runCLI(t, "env", "list", "--db", db, "--json", app)
	if code != 0 {
		t.Fatalf("list: code=%d\nstderr=%s", code, errOut)
	}
	if strings.Contains(out, "postgres://secret-conn") || strings.Contains(errOut, "postgres://secret-conn") {
		t.Fatal("env list 泄露值（脱敏破防）")
	}
	var listed struct {
		App  string `json:"app"`
		Vars []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
			Value  string `json:"value"`
		} `json:"vars"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("list json: %v\n%s", err, out)
	}
	if len(listed.Vars) != 1 || listed.Vars[0].Key != "DATABASE_URL" || listed.Vars[0].Status != "pending" {
		t.Fatalf("listed = %+v", listed)
	}
	if listed.Vars[0].Value == "postgres://secret-conn" {
		t.Fatal("masked value leaked")
	}

	// get → 显式读回明文。
	code, out, _ = runCLI(t, "env", "get", "--db", db, "--key", key, "--json", app, "DATABASE_URL")
	if code != 0 {
		t.Fatalf("get: code=%d", code)
	}
	var got struct {
		Value  string `json:"value"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Value != "postgres://secret-conn" {
		t.Fatalf("get = %q err=%v", out, err)
	}

	// rm → 删除；二次 get 失败（退出码 1）。
	code, out, _ = runCLI(t, "env", "rm", "--db", db, app, "DATABASE_URL")
	if code != 0 || !strings.Contains(out, "removed") {
		t.Fatalf("rm: code=%d out=%q", code, out)
	}
	code, _, errOut = runCLI(t, "env", "get", "--db", db, "--key", key, app, "DATABASE_URL")
	if code != 1 || !strings.Contains(errOut, "not found") {
		t.Fatalf("get after rm: code=%d stderr=%q", code, errOut)
	}
}

// TestCLIEnvPendingVisibility pending 语义展示：set 后行状态为 pending；
// 存储层验证（pending 不进 effective 合并输入）。
func TestCLIEnvPendingVisibility(t *testing.T) {
	db, key, app := newStateEnv(t)
	newKey(t, key)

	code, _, errOut := runCLI(t, "env", "set", "--db", db, "--key", key, app, "TOKEN", "v1")
	if code != 0 {
		t.Fatalf("set: %s", errOut)
	}
	st, err := state.Open(context.Background(), db)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = st.Close() }()
	a, err := st.GetAppByName(context.Background(), app)
	if err != nil {
		t.Fatalf("app: %v", err)
	}
	eff, err := st.EffectiveAppEnv(context.Background(), a.ID)
	if err != nil || len(eff) != 0 {
		t.Fatalf("effective rows = %+v err=%v, want empty (pending 未部署不生效)", eff, err)
	}
	rows, err := st.ListAppEnv(context.Background(), a.ID)
	if err != nil || len(rows) != 1 || rows[0].Status != state.EnvStatusPending {
		t.Fatalf("rows = %+v err=%v", rows, err)
	}
}

// TestCLIEnvKeyGuardrails 密钥纪律：缺 key 且未 --create-key → 拒绝；应用
// 不存在 → 拒绝。
func TestCLIEnvKeyGuardrails(t *testing.T) {
	db, key, app := newStateEnv(t)
	// key 文件不存在 + 无 --create-key → 退出 1 且文案带指引。
	code, _, errOut := runCLI(t, "env", "set", "--db", db, "--key", key, app, "K", "v")
	if code != 1 || !strings.Contains(errOut, "master key") {
		t.Fatalf("missing key: code=%d stderr=%q", code, errOut)
	}
	// 应用不存在。
	newKey(t, key)
	code, _, errOut = runCLI(t, "env", "set", "--db", db, "--key", key, "no-such-app", "K", "v")
	if code != 1 || !strings.Contains(errOut, "not found") {
		t.Fatalf("missing app: code=%d stderr=%q", code, errOut)
	}
}

// seedPlacement 准备已绑定 + 已登记卷的应用（placement show 夹具）。
func seedPlacement(t *testing.T, db, app string) string {
	t.Helper()
	st, err := state.Open(context.Background(), db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()
	a, err := st.GetAppByName(context.Background(), app)
	if err != nil {
		t.Fatalf("app: %v", err)
	}
	platformID := "n_" + "01TESTNODE"
	if _, err := st.BindPlacement(context.Background(), state.PlacementWrite{
		AppID: a.ID, PlatformNodeID: platformID,
		Source: state.PlacementSourcePlatform, Pinned: true,
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	volName, err := naming.VolumeName(app, "data", a.ID)
	if err != nil {
		t.Fatalf("vol name: %v", err)
	}
	if _, _, err := st.RegisterAppVolume(context.Background(), state.VolumeWrite{
		AppID: a.ID, Key: "data", Name: volName,
		PlatformNodeID: platformID, MountPath: "/var/lib/data",
	}); err != nil {
		t.Fatalf("register: %v", err)
	}
	return platformID
}

// TestCLIPlacementShowUnbound 未绑定应用（无卷自由调度）是 placement show
// 的合法展示态（实机验证回归：不得把 ErrPlacementNotFound 当错误上抛）。
func TestCLIPlacementShowUnbound(t *testing.T) {
	db, _, app := newStateEnv(t)

	code, out, errOut := runCLI(t, "placement", "show", "--db", db, "--json", app)
	if code != 0 {
		t.Fatalf("show unbound: code=%d\nstderr=%s", code, errOut)
	}
	if strings.Contains(out, "not found") {
		t.Fatalf("unbound app must render, not error: %s", out)
	}
	var got struct {
		App     string `json:"app"`
		Bound   bool   `json:"bound"`
		NodeID  string `json:"node_id"`
		Volumes []any  `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if got.App != app || got.Bound || got.NodeID != "" || len(got.Volumes) != 0 {
		t.Fatalf("unbound show = %+v", got)
	}

	// 人读形态含 free-to-schedule 语义。
	code, out, _ = runCLI(t, "placement", "show", "--db", db, app)
	if code != 0 || !strings.Contains(out, "unbound") {
		t.Fatalf("human unbound output = %q code=%d", out, code)
	}
}

// TestCLIPlacementShow 验收 6：placement show 展示绑定 + 卷 + 约束（JSON
// 与人读双形态）。
func TestCLIPlacementShow(t *testing.T) {
	db, _, app := newStateEnv(t)
	platformID := seedPlacement(t, db, app)

	code, out, errOut := runCLI(t, "placement", "show", "--db", db, "--json", app)
	if code != 0 {
		t.Fatalf("show: code=%d\nstderr=%s", code, errOut)
	}
	var got struct {
		App        string `json:"app"`
		Bound      bool   `json:"bound"`
		NodeID     string `json:"node_id"`
		State      string `json:"state"`
		Source     string `json:"source"`
		Constraint string `json:"constraint"`
		Volumes    []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if !got.Bound || got.NodeID != platformID || got.State != "bound" || got.Source != "platform" {
		t.Fatalf("show = %+v", got)
	}
	if got.Constraint != "node.labels.edgefleet.node-id == "+platformID {
		t.Fatalf("constraint = %q", got.Constraint)
	}
	if len(got.Volumes) != 1 || got.Volumes[0].Key != "data" {
		t.Fatalf("volumes = %+v", got.Volumes)
	}

	// 人读形态含约束行。
	code, out, _ = runCLI(t, "placement", "show", "--db", db, app)
	if code != 0 || !strings.Contains(out, "constraint: node.labels.edgefleet.node-id == "+platformID) {
		t.Fatalf("human output = %q code=%d", out, code)
	}
}

// TestCLINodesLs 验收 6：nodes ls 只读观测缓存（JSON 含 stale/observed_at；
// 文案不出现「心跳」——state-model §2.2 wording 纪律）。
func TestCLINodesLs(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	st, err := state.Open(context.Background(), db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	swarmID := "swarm-test"
	if err := st.SyncNodeObservations(context.Background(), []state.SubstrateNode{{
		SwarmNodeID: swarmID, Hostname: "srv-01", State: "ready", Availability: "active",
		IsManager: true, Labels: map[string]string{"edgefleet.node-id": "n_test"},
	}}, time.Now()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	_ = st.Close()

	code, out, errOut := runCLI(t, "nodes", "ls", "--db", db, "--json")
	if code != 0 {
		t.Fatalf("ls: code=%d stderr=%s", code, errOut)
	}
	var got struct {
		Nodes []struct {
			SwarmNodeID string `json:"swarm_node_id"`
			PlatformID  string `json:"platform_id"`
			Hostname    string `json:"hostname"`
			State       string `json:"state"`
			Stale       bool   `json:"stale"`
		} `json:"nodes"`
		Cache bool `json:"cache"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out)
	}
	if !got.Cache || len(got.Nodes) != 1 {
		t.Fatalf("nodes = %+v", got)
	}
	n := got.Nodes[0]
	if n.SwarmNodeID != swarmID || n.PlatformID != "n_test" || n.Hostname != "srv-01" || n.State != "ready" || n.Stale {
		t.Fatalf("node = %+v", n)
	}
	// 人读形态：缓存语义与 docker node 指引。
	code, out, _ = runCLI(t, "nodes", "ls", "--db", db)
	if code != 0 || !strings.Contains(out, "observation cache") || !strings.Contains(out, "docker node") {
		t.Fatalf("human = %q", out)
	}
	// wording 纪律：不出现心跳字样。
	if strings.Contains(strings.ToLower(out), "heartbeat") || strings.Contains(out, "心跳") {
		t.Fatalf("node output claims heartbeat: %q", out)
	}
}

// TestCLIPlanPlatformOverrideWarning 验收 3 的 plan 侧：--db 打开状态库时
// 平台覆盖键并入 W_ENV_PLATFORM_OVERRIDE 警告；无 --db 不出。
func TestCLIPlanPlatformOverrideWarning(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "state.db")
	key := filepath.Join(dir, "edgefleet.key")
	st, err := state.Open(context.Background(), db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	app, err := st.CreateApp(context.Background(), "", "my-api")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	box, _, err := secrets.EnsureKey(key)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	cipher, err := box.Encrypt([]byte("platform-value"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if _, err := st.SetAppEnv(context.Background(), app.ID, "LOG_LEVEL", string(cipher), "platform"); err != nil {
		t.Fatalf("set env: %v", err)
	}
	_ = st.Close()

	// compose 与平台层同键 LOG_LEVEL → plan 警告。
	target := writeFixture(t, `
name: my-api
services:
  web:
    image: nginx:1.27
    environment:
      LOG_LEVEL: debug
`)
	code, out, errOut := runCLI(t, "plan", "--db", db, "--json", target)
	if code != 2 {
		t.Fatalf("plan: code=%d\nstderr=%s", code, errOut)
	}
	if !strings.Contains(out, "W_ENV_PLATFORM_OVERRIDE") {
		t.Fatalf("plan 缺平台覆盖告警:\n%s", out)
	}
	if !strings.Contains(out, "LOG_LEVEL") {
		t.Fatalf("告警缺键名:\n%s", out)
	}
	// 脱敏：密文/明文不入输出。
	if strings.Contains(out, "platform-value") || strings.Contains(out, string(cipher)) {
		t.Fatal("plan 泄露 env 值/密文")
	}

	// 无 --db：同 compose 无该警告。
	code, out, _ = runCLI(t, "plan", "--json", target)
	if code != 2 || strings.Contains(out, "W_ENV_PLATFORM_OVERRIDE") {
		t.Fatalf("no-db plan: code=%d\n%s", code, out)
	}
}
