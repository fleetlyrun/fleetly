package buckettemplate

// buckettemplate 单测（B5，ADR-0096 决策 7 / design §2.6：digest 钉定门禁
// 同构 dbtemplate TestImageDigestsPinned——pubtemplate 未立独立门禁的缺课
// 本批随 bucketstore 补齐〔单版矩阵最小形态〕）。

import (
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// TestSiloVersionMatrix 钉单版矩阵不变量：恰一枚、Default 恒真、非弃用、
// ID/Tag 同源（RELEASE.<ID> 前缀）。
func TestSiloVersionMatrix(t *testing.T) {
	if want := []string{"silo"}; strings.Join(Engines(), ",") != strings.Join(want, ",") {
		t.Fatalf("engines = %v, want %v", Engines(), want)
	}
	specs, ok := VersionsFor("silo")
	if !ok || len(specs) != 1 {
		t.Fatalf("silo must carry exactly one version, got %v (ok=%v)", specs, ok)
	}
	s := specs[0]
	if !s.Default {
		t.Fatalf("the single silo version must be the default (id %q)", s.ID)
	}
	if s.Deprecated {
		t.Fatalf("the single silo version must not be deprecated at registration")
	}
	if !strings.HasPrefix(s.Tag, "RELEASE."+s.ID) {
		t.Fatalf("version id %q must be the month short name of tag %q", s.ID, s.Tag)
	}
}

// TestSiloImageDigestPinned 钉 digest 形态（sha256: 64 hex——真值对账走
// scripts/digests.sh，ADR-0094 决策 6 SOP）。
func TestSiloImageDigestPinned(t *testing.T) {
	tpl, err := For("silo", "")
	if err != nil {
		t.Fatalf("For(silo): %v", err)
	}
	img := tpl.Image()
	if !strings.HasPrefix(img, "pgsty/silo:RELEASE.") || !strings.Contains(img, "@sha256:") {
		t.Fatalf("image %q must be repo:tag@sha256 pinned", img)
	}
	d := tpl.ImageDigest()
	if !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
		t.Fatalf("digest %q must be sha256:<64 hex>", d)
	}
	if !strings.HasSuffix(img, "@"+d) {
		t.Fatalf("Image() digest segment must equal ImageDigest(): %q vs %q", img, d)
	}
}

// TestSiloFaces 钉模板面：端口/数据目录/探针/命令/材料空面/零旋钮。
func TestSiloFaces(t *testing.T) {
	tpl, err := For("silo", "")
	if err != nil {
		t.Fatalf("For(silo): %v", err)
	}
	if got := tpl.Meta(); got.Port != 9000 || got.Version != "2026-10" {
		t.Fatalf("meta = %+v, want port 9000 version 2026-10", got)
	}
	if tpl.DataTarget() != "/data" {
		t.Fatalf("data target = %q, want /data", tpl.DataTarget())
	}
	if tpl.ToolUser() != "" {
		t.Fatalf("silo declares no tool user (image default root — B8-fix zero behavior change), got %q", tpl.ToolUser())
	}
	probe := tpl.Probe()
	if len(probe) == 0 || probe[0] != "curl" {
		t.Fatalf("probe must be the curl exec form (live-settled: image carries curl, no wget), got %v", probe)
	}
	if knobs := tpl.Knobs(); len(knobs) != 0 {
		t.Fatalf("silo v1 carries no knobs, got %v", knobs)
	}
	env, command, err := tpl.Workload(SiloPlatformAccessKey, "secret0123456789", nil)
	if err != nil {
		t.Fatalf("workload: %v", err)
	}
	if len(command) != 3 || command[0] != "silo" || command[1] != "server" || command[2] != "/data" {
		t.Fatalf("command = %v, want silo server /data", command)
	}
	if len(env) != 2 {
		t.Fatalf("env must carry exactly the two root credential vars, got %v", env)
	}
	if env[siloEnvRootUser] != SiloPlatformAccessKey {
		t.Fatalf("root user env must carry the platform access key")
	}
	files, err := tpl.Materials(nil)
	if err != nil || len(files) != 0 {
		t.Fatalf("materials must render an empty file face, got %v (err %v)", files, err)
	}
}

// TestSiloConnMaterialRoundTrip 钉写读同源律：ConnMaterial 铸 →
// ParseConnMaterial 回读三段等值；坏形态精确失败。
func TestSiloConnMaterialRoundTrip(t *testing.T) {
	tpl, err := For("silo", "")
	if err != nil {
		t.Fatalf("For(silo): %v", err)
	}
	host := "bkt-01hx"
	mat := tpl.ConnMaterial(host, SiloPlatformAccessKey, "sk0123456789abcdef")
	parsed, err := ParseConnMaterial(mat)
	if err != nil {
		t.Fatalf("parse %q: %v", mat, err)
	}
	if parsed.Endpoint != host+":9000" || parsed.AccessKey != SiloPlatformAccessKey || parsed.SecretKey != "sk0123456789abcdef" {
		t.Fatalf("round trip drifted: %+v", parsed)
	}
	if again := RenderConnMaterial(parsed); again != mat {
		t.Fatalf("re-render must be byte-stable: %q vs %q", again, mat)
	}
	for _, bad := range []string{
		"",
		"endpoint=bkt:9000",
		"endpoint=bkt:9000;access_key=fleetly",
		"endpoint=bkt:9000;access_key=fleetly;secret_key=",
		"endpoint=bkt:9000;access_key=fleetly;secret_key=sk;extra=1",
		"endpoint bkt:9000;access_key=fleetly;secret_key=sk",
		"endpoint=bkt:9000;endpoint=bkt:9001;access_key=fleetly;secret_key=sk",
	} {
		if _, err := ParseConnMaterial(bad); err == nil {
			t.Fatalf("parse(%q) must fail", bad)
		}
	}
}

// TestSiloCredentialGates 钉凭证闸：AK 最短 3/字符集；SK 过渲染安全闸；
// RotatePassword 声明式（Argv 空）。
func TestSiloCredentialGates(t *testing.T) {
	tpl, err := For("silo", "")
	if err != nil {
		t.Fatalf("For(silo): %v", err)
	}
	if _, _, err := tpl.Workload("ab", "secret0123456789", nil); err == nil {
		t.Fatalf("short access key must be refused")
	}
	if _, _, err := tpl.Workload("fleetly", "bad;secret", nil); err == nil {
		t.Fatalf("secret key outside the render-safe set must be refused")
	}
	spec, err := tpl.RotatePassword("bkt-01hx", "current0123456789", "next0123456789")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if len(spec.Argv) != 0 {
		t.Fatalf("silo rotation is declarative (env re-delivery roll), got argv %v", spec.Argv)
	}
	if _, err := tpl.ApplyDynamicSettings("bkt-01hx", "secret0123456789", map[string]string{"any": "1"}); err != enginetemplate.ErrNoDynamicFace {
		t.Fatalf("silo carries no dynamic face, got %v", err)
	}
}

// TestSplitSettingsEmpty 零旋钮下分流恒空（喂入律帮手的族内齐形）。
func TestSplitSettingsEmpty(t *testing.T) {
	static, dynamic := SplitSettings(nil, map[string]string{"anything": "1"})
	if len(static) != 0 || len(dynamic) != 0 {
		t.Fatalf("zero-knob family must split to empty sets, got %v/%v", static, dynamic)
	}
}
