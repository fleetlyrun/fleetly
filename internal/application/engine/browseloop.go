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
func (b *browses) browseStep(ctx context.Context) {
	if !b.browseConfig().Configured() {
		return // 面停用：无行可收（受理已拒），零成本拍
	}
	rows, err := b.repos.Browse.List(ctx)
	if err != nil {
		b.log.Error("browse step: list ledger", "err", err)
		return
	}
	now := b.clock.Now()
	byID := map[string]*browse.Session{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	// 注册表补齐（重启/崩溃后恢复：grant 重铸、lastTouch = 恢复时刻）。
	for _, r := range rows {
		if b.has(r.ID) {
			continue
		}
		if exp, err := r.ExpiryTime(); err == nil && now.After(exp) {
			continue // 已到期：走下方回收路径，不复活
		}
		b.adoptBrowseRow(ctx, r, now)
	}
	// 注册表流失行（行被外部删除等）：丢弃注册态，下一拍载体由...诚实
	// 边界——行是台账真源，行没了注册态随之丢弃（无回收目标）。memo 清
	// 账走 ensureForget 端口（ensureMu 侧统一入口，P4 wave-3b 直删债清偿）。
	b.mu.Lock()
	for id := range b.sessions {
		if _, ok := byID[id]; !ok {
			delete(b.sessions, id)
			b.ensureForget(b.ensure, id)
		}
	}
	b.mu.Unlock()
	// 逐会话：回收或 Ensure。
	for _, r := range rows {
		if ctx.Err() != nil {
			return
		}
		if b.browseSessionDone(r, now) {
			b.teardownBrowseSession(ctx, r)
			continue
		}
		b.reconcileBrowseSession(ctx, r, now)
	}
}

// has 报告注册表是否持有该会话（P3 批 4 下放：原 Engine.hasBrowseSession，
// 纯注册表读——只触 mu/sessions 域内态）。
func (b *browses) has(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.sessions[id]
	return ok
}

// adoptBrowseRow 从台账行恢复注册态（重启路径；grant 重铸——旧 cookie
// 全部失效，用户重开会话）。
func (b *browses) adoptBrowseRow(ctx context.Context, r *browse.Session, now time.Time) {
	browser, ok := dbbrowser.For(r.Engine)
	if !ok {
		b.log.Error("browse step: engine left the browser registry", "session", r.ID, "engine", r.Engine)
		return
	}
	team, err := b.projectTeam(ctx, r.ProjectID)
	if err != nil {
		b.log.Error("browse step: resolve project", "session", r.ID, "err", err)
		return
	}
	grant, err := browseGrantBytes()
	if err != nil {
		b.log.Error("browse step: mint grant", "session", r.ID, "err", err)
		return
	}
	created := now
	if c, err := r.CreatedTime(); err == nil {
		created = c
	}
	name := b.databaseNameOf(ctx, r.DatabaseID)
	b.mu.Lock()
	b.sessions[r.ID] = &browseSession{
		id: r.ID, teamID: team, projectID: r.ProjectID, databaseID: r.DatabaseID,
		engineName: r.Engine, browser: browser, readOnly: r.ReadOnly, grant: grant,
		createdAt: created, expiresAt: created.Add(BrowseHardTTL), lastTouch: now,
		databaseRow: name,
	}
	b.mu.Unlock()
}

// browseSessionDone 判定回收条件：行到期（硬 TTL）或注册表空闲超窗。
func (b *browses) browseSessionDone(r *browse.Session, now time.Time) bool {
	if exp, err := r.ExpiryTime(); err == nil && now.After(exp) {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.sessions[r.ID]
	if !ok {
		return false // 尚未采纳（依赖缺失等）：留待下一拍，不删行
	}
	return now.Sub(s.lastTouch) > browseIdleTTL
}

// teardownBrowseSession 收口：拆载体 → 删行 → 清注册态 + Kick 路由发布。
func (b *browses) teardownBrowseSession(ctx context.Context, r *browse.Session) {
	stepCtx, cancel := b.boundedStep(ctx)
	defer cancel()
	unlockMaintenance := b.lockMaintenance()
	defer unlockMaintenance()
	ns := capability.NamespaceRef{Team: b.teamOf(r.ID), Project: r.ProjectID, Browse: r.ID}
	if err := b.runtime.Remove(stepCtx, ns); err != nil {
		b.log.Error("browse teardown: remove", "session", r.ID, "err", err)
		// 载体拆除失败不删行——下一拍重试（宁留行不漏载体）。
		return
	}
	if err := b.repos.Browse.Delete(stepCtx, r.ID); err != nil {
		b.log.Error("browse teardown: delete ledger row", "session", r.ID, "err", err)
		return
	}
	b.mu.Lock()
	delete(b.sessions, r.ID)
	b.mu.Unlock()
	b.ensureForget(b.ensure, r.ID)
	b.publishRoutesNow()
}

// teamOf 读注册表里会话的 team（无注册态时空串——Remove 容忍；P3 批 4
// 下放：原 Engine.browseTeamOf，纯注册表读——只触 mu/sessions 域内态）。
func (b *browses) teamOf(id string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.sessions[id]; ok {
		return s.teamID
	}
	return ""
}

// databaseNameOf 回读库名（凭证 Secret 铸名的输入；注册恢复路径）。库名
// 不在台账行上（渲染期点查数据库行——列变更即方言重渲，行不冗余）。
func (b *browses) databaseNameOf(ctx context.Context, databaseID string) string {
	row, err := b.repos.Databases.Get(ctx, databaseID)
	if err != nil {
		return ""
	}
	return row.Name
}

// reconcileBrowseSession 收敛单会话：方言投影（Secret 解封）→ 签名短路
// → Ensure → 归属/期望登记。
func (b *browses) reconcileBrowseSession(ctx context.Context, r *browse.Session, now time.Time) {
	b.mu.Lock()
	s, ok := b.sessions[r.ID]
	b.mu.Unlock()
	if !ok {
		return // 采纳失败（依赖缺失）：留待下一拍
	}
	unlockMaintenance := b.lockMaintenance()
	defer unlockMaintenance()
	stepCtx, cancel := b.boundedStep(ctx)
	defer cancel()
	w, materials, err := b.projectBrowseWorkload(stepCtx, s)
	if err != nil {
		b.log.Error("browse reconcile: project workload", "session", s.id, "err", err)
		return
	}
	sig := managedFingerprint([]capability.Workload{w}) + "\x00" + materialsFingerprint(materials)
	if _, fresh := b.ensureFresh(b.ensure, s.id, sig, now); fresh {
		return
	}
	ns := capability.NamespaceRef{Team: s.teamID, Project: s.projectID, Browse: s.id}
	if _, err := b.runtime.Ensure(stepCtx, ns, []capability.Workload{w}, capability.Generation(1), materials); err != nil {
		b.log.Error("browse reconcile: ensure", "session", s.id, "err", err)
		b.ensureForget(b.ensure, s.id)
		return
	}
	b.ensureRemember(b.ensure, s.id, ensureMemo{sig: sig, gen: 1, at: now})
	b.mu.Lock()
	s.workload = w
	b.mu.Unlock()
	// 归属/期望登记（观测路由 + 稳态看门狗：browse 载体死亡也报
	// workload.stopped——用户可感知重开）。P4 wave-2a 经观测窄接口端口
	//（recordOwners/recordExpected），卫星不直写 obs/expect 内部。
	b.recordOwners(1, []capability.Workload{w}, func(capability.Workload) workloadOwner {
		return browseOwner(s.id)
	})
	b.recordExpected(browseOwner(s.id), 1)
}

// projectBrowseWorkload 投影浏览器 Workload：方言渲染（dbbrowser 单源）
// + Project 活跃网 + 寻址 + 材料解封（凭证 Secret 单真源回读）。
func (b *browses) projectBrowseWorkload(ctx context.Context, s *browseSession) (capability.Workload, capability.Materials, error) {
	rendering, err := b.renderBrowseDialect(ctx, s)
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
		Networks: b.projectNetworkFactsNamesOnly(ctx, s.projectID),
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
func (b *browses) renderBrowseDialect(ctx context.Context, s *browseSession) (dbbrowser.Rendering, error) {
	if b.cipher == nil {
		return dbbrowser.Rendering{}, fmt.Errorf("browse requires the master key (data root keys/ missing)")
	}
	secName := DBCredentialSecretName(s.databaseRow)
	sec, err := b.repos.Secrets.GetByName(ctx, s.projectID, secName)
	if err != nil {
		return dbbrowser.Rendering{}, fmt.Errorf("lookup credential secret %q: %w", secName, err)
	}
	plain, err := b.cipher.Open(sec.Ciphertext)
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
