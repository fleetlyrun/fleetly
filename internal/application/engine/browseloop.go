package engine

// Browse 会话收敛环（台账行 × 注册表活体双真，ADR-0051）：browseStep 单次
// 推进 = 行集 ↔ 注册表对账（未知行采纳——重启恢复；流失行拆除）→ 到期/
// 空闲回收（Runtime.Remove + 删行）→ 存量会话 Ensure（签名短路）。
// 受理/grant/路由发布面见 browse.go。

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/data/browse"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbbrowser"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// browseIdleTTL 是空闲回收窗（entry/authorize 接触即续活；重启后
// lastTouch = 恢复时刻——10min 新窗口，诚实）。
const browseIdleTTL = 10 * time.Minute

// browseStep 是 browse 收敛环的单次推进：行集 ↔ 注册表对账（未知行采纳
// ——重启恢复；流失行拆除）→ 到期/空闲回收（Runtime.Remove + 删行）→
// 存量会话 Ensure（签名短路）。
func (e *Engine) browseStep(ctx context.Context) {
	if !e.opts.Data.Browse.Configured() {
		return // 面停用：无行可收（受理已拒），零成本拍
	}
	rows, err := e.browseRepo.List(ctx, e.db.Runner())
	if err != nil {
		e.log.Error("browse step: list ledger", "err", err)
		return
	}
	now := e.clock.Now()
	byID := map[string]*browse.Session{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	// 注册表补齐（重启/崩溃后恢复：grant 重铸、lastTouch = 恢复时刻）。
	for _, r := range rows {
		if e.browse.has(r.ID) {
			continue
		}
		if exp, err := r.ExpiryTime(); err == nil && now.After(exp) {
			continue // 已到期：走下方回收路径，不复活
		}
		e.adoptBrowseRow(ctx, r, now)
	}
	// 注册表流失行（行被外部删除等）：丢弃注册态，下一拍载体由...诚实
	// 边界——行是台账真源，行没了注册态随之丢弃（无回收目标）。
	e.browse.mu.Lock()
	for id := range e.browse.sessions {
		if _, ok := byID[id]; !ok {
			delete(e.browse.sessions, id)
			delete(e.browse.ensure, id)
		}
	}
	e.browse.mu.Unlock()
	// 逐会话：回收或 Ensure。
	for _, r := range rows {
		if ctx.Err() != nil {
			return
		}
		if e.browseSessionDone(r, now) {
			e.teardownBrowseSession(ctx, r)
			continue
		}
		e.reconcileBrowseSession(ctx, r, now)
	}
}

// has 报告注册表是否持有该会话（P3 批 4 下放：原 Engine.hasBrowseSession，
// 纯注册表读——只触 mu/sessions 域内态）。
func (x *browses) has(id string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	_, ok := x.sessions[id]
	return ok
}

// adoptBrowseRow 从台账行恢复注册态（重启路径；grant 重铸——旧 cookie
// 全部失效，用户重开会话）。
func (e *Engine) adoptBrowseRow(ctx context.Context, r *browse.Session, now time.Time) {
	b, ok := dbbrowser.For(r.Engine)
	if !ok {
		e.log.Error("browse step: engine left the browser registry", "session", r.ID, "engine", r.Engine)
		return
	}
	team, err := e.projectTeam(ctx, r.ProjectID)
	if err != nil {
		e.log.Error("browse step: resolve project", "session", r.ID, "err", err)
		return
	}
	grant, err := browseGrantBytes()
	if err != nil {
		e.log.Error("browse step: mint grant", "session", r.ID, "err", err)
		return
	}
	created := now
	if c, err := r.CreatedTime(); err == nil {
		created = c
	}
	name := e.databaseNameOf(ctx, r.DatabaseID)
	e.browse.mu.Lock()
	e.browse.sessions[r.ID] = &browseSession{
		id: r.ID, teamID: team, projectID: r.ProjectID, databaseID: r.DatabaseID,
		engineName: r.Engine, browser: b, readOnly: r.ReadOnly, grant: grant,
		createdAt: created, expiresAt: created.Add(BrowseHardTTL), lastTouch: now,
		databaseRow: name,
	}
	e.browse.mu.Unlock()
}

// browseSessionDone 判定回收条件：行到期（硬 TTL）或注册表空闲超窗。
func (e *Engine) browseSessionDone(r *browse.Session, now time.Time) bool {
	if exp, err := r.ExpiryTime(); err == nil && now.After(exp) {
		return true
	}
	e.browse.mu.Lock()
	defer e.browse.mu.Unlock()
	s, ok := e.browse.sessions[r.ID]
	if !ok {
		return false // 尚未采纳（依赖缺失等）：留待下一拍，不删行
	}
	return now.Sub(s.lastTouch) > browseIdleTTL
}

// teardownBrowseSession 收口：拆载体 → 删行 → 清注册态 + Kick 路由发布。
func (e *Engine) teardownBrowseSession(ctx context.Context, r *browse.Session) {
	stepCtx, cancel := context.WithTimeout(ctx, e.opts.Ring.ManagedStepTimeout)
	defer cancel()
	unlockMaintenance := e.lockMaintenance()
	defer unlockMaintenance()
	ns := capability.NamespaceRef{Team: e.browse.teamOf(r.ID), Project: r.ProjectID, Browse: r.ID}
	if err := e.runtime.Remove(stepCtx, ns); err != nil {
		e.log.Error("browse teardown: remove", "session", r.ID, "err", err)
		// 载体拆除失败不删行——下一拍重试（宁留行不漏载体）。
		return
	}
	if err := e.browseRepo.Delete(stepCtx, e.db.Runner(), r.ID); err != nil {
		e.log.Error("browse teardown: delete ledger row", "session", r.ID, "err", err)
		return
	}
	e.browse.mu.Lock()
	delete(e.browse.sessions, r.ID)
	delete(e.browse.ensure, r.ID)
	e.browse.mu.Unlock()
	e.PublishRoutesNow()
}

// teamOf 读注册表里会话的 team（无注册态时空串——Remove 容忍；P3 批 4
// 下放：原 Engine.browseTeamOf，纯注册表读——只触 mu/sessions 域内态）。
func (x *browses) teamOf(id string) string {
	x.mu.Lock()
	defer x.mu.Unlock()
	if s, ok := x.sessions[id]; ok {
		return s.teamID
	}
	return ""
}

// databaseNameOf 回读库名（凭证 Secret 铸名的输入；注册恢复路径）。库名
// 不在台账行上（渲染期点查数据库行——列变更即方言重渲，行不冗余）。
func (e *Engine) databaseNameOf(ctx context.Context, databaseID string) string {
	row, err := e.databases.Get(ctx, e.db.Runner(), databaseID)
	if err != nil {
		return ""
	}
	return row.Name
}

// reconcileBrowseSession 收敛单会话：方言投影（Secret 解封）→ 签名短路
// → Ensure → 归属/期望登记。
func (e *Engine) reconcileBrowseSession(ctx context.Context, r *browse.Session, now time.Time) {
	e.browse.mu.Lock()
	s, ok := e.browse.sessions[r.ID]
	e.browse.mu.Unlock()
	if !ok {
		return // 采纳失败（依赖缺失）：留待下一拍
	}
	unlockMaintenance := e.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := context.WithTimeout(ctx, e.opts.Ring.ManagedStepTimeout)
	defer cancel()
	w, materials, err := e.projectBrowseWorkload(stepCtx, s)
	if err != nil {
		e.log.Error("browse reconcile: project workload", "session", s.id, "err", err)
		return
	}
	sig := managedFingerprint([]capability.Workload{w}) + "\x00" + materialsFingerprint(materials)
	if _, fresh := e.ensureFresh(e.browse.ensure, s.id, sig, now); fresh {
		return
	}
	ns := capability.NamespaceRef{Team: s.teamID, Project: s.projectID, Browse: s.id}
	if _, err := e.runtime.Ensure(stepCtx, ns, []capability.Workload{w}, capability.Generation(1), materials); err != nil {
		e.log.Error("browse reconcile: ensure", "session", s.id, "err", err)
		e.ensureForget(e.browse.ensure, s.id)
		return
	}
	e.ensureRemember(e.browse.ensure, s.id, ensureMemo{sig: sig, gen: 1, at: now})
	e.browse.mu.Lock()
	s.workload = w
	e.browse.mu.Unlock()
	// 归属/期望登记（观测路由 + 稳态看门狗：browse 载体死亡也报
	// workload.stopped——用户可感知重开）。
	e.obs.recordOwners(1, []capability.Workload{w}, func(capability.Workload) workloadOwner {
		return browseOwner(s.id)
	})
	e.expect.mu.Lock()
	e.expect.expected[browseOwner(s.id)] = 1
	e.expect.mu.Unlock()
}

// projectBrowseWorkload 投影浏览器 Workload：方言渲染（dbbrowser 单源）
// + Project 活跃网 + 寻址 + 材料解封（凭证 Secret 单真源回读）。
func (e *Engine) projectBrowseWorkload(ctx context.Context, s *browseSession) (capability.Workload, capability.Materials, error) {
	rendering, err := e.renderBrowseDialect(ctx, s)
	if err != nil {
		return capability.Workload{}, capability.Materials{}, err
	}
	w := capability.Workload{
		// Workload ID = 会话 ID（域内唯一；Ensure 走 create 路径）。
		ID:       s.id,
		Process:  s.browser.Name(),
		Image:    s.browser.Image(),
		Command:  rendering.Command,
		Env:      rendering.Env,
		Replicas: 1,
		Networks: e.projectNetworkFactsNamesOnly(ctx, s.projectID),
		Ports: []capability.WorkloadPort{
			{Port: s.browser.Port(), Protocol: capability.ProtocolHTTP},
		},
		// 交互会话崩溃自愈（RestartAlways = swarm any）；回收走
		// Runtime.Remove（会话收口是主动动词，与重启策略无冲突）。
		Restart:    capability.RestartAlways,
		StopGrace:  10 * time.Second,
		Addressing: []capability.Address{{Name: BrowseDNSName(s.id)}},
		Generation: 1,
	}
	return w, capability.Materials{SecretFiles: rendering.Files}, nil
}

// renderBrowseDialect 解封凭证并渲染方言：Secret database:<name> → 连接
// URL → Conn（user/password/host/port/db）→ browser.Render。
func (e *Engine) renderBrowseDialect(ctx context.Context, s *browseSession) (dbbrowser.Rendering, error) {
	if e.cipher == nil {
		return dbbrowser.Rendering{}, fmt.Errorf("browse requires the master key (data root keys/ missing)")
	}
	secName := DBCredentialSecretName(s.databaseRow)
	sec, err := e.secrets.GetByName(ctx, e.db.Runner(), s.projectID, secName)
	if err != nil {
		return dbbrowser.Rendering{}, fmt.Errorf("lookup credential secret %q: %w", secName, err)
	}
	plain, err := e.cipher.Open(sec.Ciphertext)
	if err != nil {
		return dbbrowser.Rendering{}, fmt.Errorf("decrypt credential secret %q: %w", secName, err)
	}
	conn, err := browseConnFromURL(string(plain))
	if err != nil {
		return dbbrowser.Rendering{}, fmt.Errorf("credential secret %q: %w", secName, err)
	}
	rendering, err := s.browser.Render(conn, s.readOnly)
	if err != nil {
		return dbbrowser.Rendering{}, fmt.Errorf("render %s dialect: %w", s.browser.Name(), err)
	}
	return rendering, nil
}

// browseConnFromURL 从连接串解出方言输入（host 是网内 DNS 名 db-<id>——
// 直用，浏览器与库同在项目网）。
func browseConnFromURL(connectURL string) (dbbrowser.Conn, error) {
	u, err := url.Parse(connectURL)
	if err != nil {
		return dbbrowser.Conn{}, fmt.Errorf("parse database connection url: %w", err)
	}
	if u.User == nil {
		return dbbrowser.Conn{}, fmt.Errorf("database connection url carries no credentials")
	}
	password, _ := u.User.Password()
	if password == "" {
		return dbbrowser.Conn{}, fmt.Errorf("database connection url carries no password")
	}
	host := u.Hostname()
	if host == "" {
		return dbbrowser.Conn{}, fmt.Errorf("database connection url carries no host")
	}
	var port int32
	if p := u.Port(); p != "" {
		var pv int
		if _, err := fmt.Sscanf(p, "%d", &pv); err != nil || pv <= 0 || pv > 65535 {
			return dbbrowser.Conn{}, fmt.Errorf("database connection url port %q invalid", p)
		}
		port = int32(pv)
	}
	return dbbrowser.Conn{
		Host: host, Port: port, User: u.User.Username(),
		Password: password, DB: strings.TrimPrefix(u.Path, "/"),
	}, nil
}
