package guards

// TestVolumeAttachmentKeySites（ADR-0097 决策 6 守卫）：钉死生产代码中
// VolumeAttachment.VolumeId / VolumeMount.VolumeID 的全部赋值点——卷载体键
// 值 = 卷行 id（entityid 形态）是同批立法，「防 name 回流」由两半承载：
//   - 白名单只增：新增赋值点不在册即红（新生产点必须显式登记并带理由）；
//   - 双向保鲜：在册点的锚串从文件中消失即红（合法点被删/改形须同步改表）。
//
// 白名单理由分三类：受理边界（用户寻址键 name 的唯一合法落点，下游受理
// 咽喉解析）、id 形透传/解析（运行时零 name 的承载面）、受管域平台内部键
// （Managed 工作负载的自建状态卷——非用户卷附件，键域与 ULID 无交集）。
// 测试文件不在扫描面（fixtures 自由构造双形）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// volumeKeySite 是一个合法赋值点的锚（file 相对仓根；anchor 是行内必含的
// 区分串——文件中找不到即红）。
type volumeKeySite struct {
	file   string
	anchor string
	why    string
}

var volumeKeySites = []volumeKeySite{
	// ---- 受理边界：name 形的唯一合法落点（下游 SubmitSpec 咽喉解析成行 id）----
	{"internal/application/composeintake/intake.go", "VolumeAttachment{VolumeId: name", "compose 受理语言的名引用（ADR-0092 name 边界寻址：用户寻址键在 intake 产出，SubmitSpec 咽喉解析成行 id 后进冻结体）"},
	// ---- id 形：解析与透传（运行时零 name）----
	{"internal/application/engine/volumes.go", "v.VolumeId = row.ID", "受理咽喉/装载点的双形解析改写（ADR-0097 决策 2）"},
	{"internal/application/engine/projection.go", "VolumeID: vol.GetVolumeId()", "投影纯透传（spec 已是 id 形）"},
	{"internal/application/databases/projection.go", "{VolumeID: dbVolumeID, Target: tpl.DataTarget()}", "数据库自动卷 = ensureDatabaseVolume 返回行 id（ADR-0097 决策 2）"},
	{"internal/application/backups/backup.go", "UtilityVolumeMount{VolumeID: vol.ID", "预置卷恢复挂卷锚 = 卷行 id"},
	{"internal/application/databases/origin.go", "UtilityVolumeMount{VolumeID: vol.ID", "库卷源标记门的挂卷锚 = 卷行 id（ADR-0097 决策 4，claim 与只读前置读两形态）"},
	{"internal/interfaces/grpc/fleetlygrpc/mapping.go", "VolumeId: v.VolumeID", "preview 面只读回显（透传已解析 id）"},
	// ---- 受管域平台内部键（非用户卷附件；固定键域与 ULID/用户名域无交集）----
	{"internal/infrastructure/providers/traefik/provider.go", "VolumeID: acmeVolumeID", "Managed traefik 的 ACME 状态卷平台内部键（ADR-0004 受管域自建载体）"},
	{"internal/infrastructure/providers/zot/provider.go", "{VolumeID: volumeID, Target: storageRoot}", "Managed zot 的存储卷平台内部键"},
	{"internal/infrastructure/providers/victorialogs/provider.go", "{VolumeID: volumeID, Target: storageRoot}", "Managed VictoriaLogs 的存储卷平台内部键"},
	{"internal/infrastructure/providers/victoriametrics/provider.go", "{VolumeID: volumeID, Target: storageRoot}", "Managed VictoriaMetrics 的存储卷平台内部键"},
}

// volumeKeyAssignRe 匹配两字段的赋值形态（复合字面量 `Field:` 与赋值
// `Field =`；读访问（Get*）不在射程）。
var volumeKeyAssignRe = regexp.MustCompile(`\b(VolumeId|VolumeID)\s*[:=]`)

func TestVolumeAttachmentKeySites(t *testing.T) {
	root := repoRoot(t)
	type hit struct{ file, line string }
	var hits []hit
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, aerr := filepath.Rel(root, path)
		if aerr != nil {
			return aerr
		}
		rel = filepath.ToSlash(rel)
		for _, line := range strings.Split(string(body), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // 注释行不在执法面
			}
			if volumeKeyAssignRe.MatchString(line) {
				hits = append(hits, hit{file: rel, line: trimmed})
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) < len(volumeKeySites) {
		t.Fatalf("scan found only %d assignment sites across %d allowlist entries — guard is blind, fix the scan", len(hits), len(volumeKeySites))
	}

	// 逐在册点核锚（双向保鲜的「合法点消失即红」半边）。
	for _, site := range volumeKeySites {
		body, rerr := os.ReadFile(filepath.Join(root, site.file))
		if rerr != nil {
			t.Errorf("allowlisted file %s unreadable: %v", site.file, rerr)
			continue
		}
		if !strings.Contains(string(body), site.anchor) {
			t.Errorf("allowlisted anchor %q no longer present in %s — update the guard table in the same commit (site: %s)", site.anchor, site.file, site.why)
		}
	}

	// 逐实际命中点查册（「新增点不在册即红」半边）。
	for _, h := range hits {
		found := false
		for _, site := range volumeKeySites {
			if h.file == site.file && strings.Contains(h.line, site.anchor) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unregistered volume key assignment in %s: %q — carrier keys are volume row ids (ADR-0097); register the site with a reason or route the value through the intake throat", h.file, h.line)
		}
	}
}
