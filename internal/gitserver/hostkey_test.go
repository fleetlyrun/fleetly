package gitserver

// FZ-12 host key 指纹台账测试（v0.3 W3-S2，rbac-teams §6 裁决 D-W0-8）：
//   - 首启装载：台账静默建账（零事件零审计）；
//   - 二次装载同值：无动作（不重复记）；
//   - 换钥（文件重建）：装载指纹 ≠ 台账 → 审计 + 事件 git.hostkey_changed
//     随台账更新落库（actor=system，diff 带新旧指纹）；
//   - Fingerprint() 现读投影：文件在位 = SHA256:…；文件缺失 = 空串。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// countHostKeyChanged 统计事件/审计流里的 git.hostkey_changed 出现次数，
// 并回传事件/审计断言材料。
type hostKeyChangedObs struct {
	events  int
	audits  int
	payload string
	subject string
	actor   string
	summary string
}

func observeHostKeyChanged(t *testing.T, st *state.Store) hostKeyChangedObs {
	t.Helper()
	ctx := context.Background()
	obs := hostKeyChangedObs{}
	events, err := st.EventsSince(ctx, 0, 1000)
	if err != nil {
		t.Fatalf("EventsSince: %v", err)
	}
	for _, e := range events {
		if e.Name == "git.hostkey_changed" {
			obs.events++
			obs.payload = e.Payload
			obs.subject = e.Subject
		}
	}
	audits, err := st.RecentAudits(ctx, 100)
	if err != nil {
		t.Fatalf("RecentAudits: %v", err)
	}
	for _, a := range audits {
		if a.Action == "git.hostkey_changed" {
			obs.audits++
			obs.actor = a.Actor
			obs.summary = a.DiffSummary
		}
	}
	return obs
}

// TestHostKeyLedgerFirstBootSilent 首启装载：台账写入、零事件零审计；
// 二次装载同值零重复。
func TestHostKeyLedgerFirstBootSilent(t *testing.T) {
	src, st, _, _ := newTestSource(t, 0)
	ctx := context.Background()

	signer, err := src.ensureHostKey(ctx)
	if err != nil {
		t.Fatalf("ensureHostKey: %v", err)
	}
	_ = signer
	if !strings.HasPrefix(src.Fingerprint(), "SHA256:") {
		t.Fatalf("Fingerprint() = %q, want SHA256: prefix", src.Fingerprint())
	}
	ledger, err := st.LoadGitHostKeyFingerprint(ctx)
	if err != nil {
		t.Fatalf("LoadGitHostKeyFingerprint: %v", err)
	}
	if ledger != src.Fingerprint() {
		t.Fatalf("ledger = %q, want loaded fingerprint %q", ledger, src.Fingerprint())
	}

	obs := observeHostKeyChanged(t, st)
	if obs.events != 0 || obs.audits != 0 {
		t.Fatalf("first boot must be silent: events=%d audits=%d", obs.events, obs.audits)
	}

	// 二次装载（同 key 复用路径）：同值无动作。
	if _, err := src.ensureHostKey(ctx); err != nil {
		t.Fatalf("second ensureHostKey: %v", err)
	}
	obs = observeHostKeyChanged(t, st)
	if obs.events != 0 || obs.audits != 0 {
		t.Fatalf("same-key reload must stay silent: events=%d audits=%d", obs.events, obs.audits)
	}
}

// TestHostKeyLedgerChangeDetected 换钥（文件重建）：再次装载指纹与台账
// 不同 → 事件 + 审计 + 台账更新（actor=system，diff 带新旧指纹）。
func TestHostKeyLedgerChangeDetected(t *testing.T) {
	src, st, _, _ := newTestSource(t, 0)
	ctx := context.Background()

	if _, err := src.ensureHostKey(ctx); err != nil {
		t.Fatalf("first ensureHostKey: %v", err)
	}
	oldFP := src.Fingerprint()

	// 换钥 = host key 文件重建（删除后再装载生成新钥）。
	if err := os.Remove(src.hostKeyPath()); err != nil {
		t.Fatalf("remove host key file: %v", err)
	}
	if _, err := src.ensureHostKey(ctx); err != nil {
		t.Fatalf("second ensureHostKey after rebuild: %v", err)
	}
	newFP := src.Fingerprint()
	if newFP == "" || newFP == oldFP {
		t.Fatalf("rebuilt fingerprint %q must differ from previous %q", newFP, oldFP)
	}

	ledger, err := st.LoadGitHostKeyFingerprint(ctx)
	if err != nil {
		t.Fatalf("LoadGitHostKeyFingerprint: %v", err)
	}
	if ledger != newFP {
		t.Fatalf("ledger = %q, want new fingerprint %q", ledger, newFP)
	}

	obs := observeHostKeyChanged(t, st)
	if obs.events != 1 || obs.audits != 1 {
		t.Fatalf("key change must emit exactly one event+audit: events=%d audits=%d", obs.events, obs.audits)
	}
	if obs.actor != "system" {
		t.Fatalf("hostkey_changed audit actor = %q, want system", obs.actor)
	}
	if obs.subject != "platform:git" {
		t.Fatalf("event subject = %q, want platform:git", obs.subject)
	}
	for _, want := range []string{oldFP, newFP} {
		if !strings.Contains(obs.payload, want) || !strings.Contains(obs.summary, want) {
			t.Fatalf("diff must carry both fingerprints: payload=%s summary=%s", obs.payload, obs.summary)
		}
	}
}

// TestFingerprintAbsentWithoutHostKey 文件缺失 = 空串（只读不生成——披露
// 面不产生副作用）。
func TestFingerprintAbsentWithoutHostKey(t *testing.T) {
	src, _, _, _ := newTestSource(t, 0)
	if got := src.Fingerprint(); got != "" {
		t.Fatalf("Fingerprint() without host key = %q, want empty", got)
	}
	// 确认没有副作用：文件未被生成。
	if _, err := os.Stat(src.hostKeyPath()); !os.IsNotExist(err) {
		t.Fatalf("Fingerprint() must not create the host key file: stat err=%v", err)
	}
}

// TestHostKeyPathFallback hostKeyPath 缺省回落 <root>/host_ed25519；显式
// git.host_key_file 优先。
func TestHostKeyPathFallback(t *testing.T) {
	src, _, _, dir := newTestSource(t, 0)
	if want := filepath.Join(dir, "git", "host_ed25519"); src.hostKeyPath() != want {
		t.Fatalf("hostKeyPath() = %q, want %q", src.hostKeyPath(), want)
	}
	src.cfg.HostKeyFile = filepath.Join(dir, "custom", "host.key")
	if got := src.hostKeyPath(); got != src.cfg.HostKeyFile {
		t.Fatalf("hostKeyPath() = %q, want explicit config path", got)
	}
}
