package compose

// fleetly.databases label 契约测试（E4 托管数据库，managed-databases
// §2.4/D-DB-4）：登记后保留命名空间放行、逗号分隔解析（trim/排序）、形态
// 违规 fail-loud（空条目/字符集/重复——E_COMPOSE_UNSUPPORTED + reason）、
// 归一化字段进 spec（参与 spec_hash——label 变更即期望态变更）。

import (
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/apperr"
)

func loadDatabases(t *testing.T, label string) (*Spec, error) {
	t.Helper()
	content := "name: dbapp\nservices:\n  web:\n    image: repo/web:1" + label + "\n"
	spec, _, err := Load(t.Context(), writeCompose(t, content))
	return spec, err
}

func asAppErrDatabases(t *testing.T, err error) *apperr.Error {
	t.Helper()
	var ae *apperr.Error
	if !errorsAsDatabases(err, &ae) || ae == nil {
		t.Fatalf("error is not an apperr envelope: %v", err)
	}
	return ae
}

func errorsAsDatabases(err error, target **apperr.Error) bool {
	if e, ok := err.(*apperr.Error); ok {
		*target = e
		return true
	}
	return false
}

// TestDatabasesLabelParsing 合法声明：单实例、多实例（逗号+空白混排）、
// trim 后归一（排序字典序——书写顺序不影响归一化形态）。
func TestDatabasesLabelParsing(t *testing.T) {
	single, err := loadDatabases(t, "\n    labels:\n      fleetly.databases: \"pg1\"\n")
	if err != nil {
		t.Fatalf("single: %v", err)
	}
	if got := single.Services[0].Databases; len(got) != 1 || got[0] != "pg1" {
		t.Fatalf("single = %v, want [pg1]", got)
	}

	multi, err := loadDatabases(t, "\n    labels:\n      fleetly.databases: \"pg2 , pg-prod,  pg1\"\n")
	if err != nil {
		t.Fatalf("multi: %v", err)
	}
	want := []string{"pg-prod", "pg1", "pg2"}
	if got := multi.Services[0].Databases; len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("multi = %v, want %v (trimmed and sorted)", got, want)
	}
}

// TestDatabasesLabelMalformedRejected 形态契约 fail-loud：空条目、非法字符
// 集（大写/前导符号——与库实例名/app 名同规则）、同服务重复声明一律在
// Load 期以 E_COMPOSE_UNSUPPORTED 拒绝（reason 上下文细分）。
func TestDatabasesLabelMalformedRejected(t *testing.T) {
	cases := []struct {
		label  string
		reason string
	}{
		{"\"pg1,,pg2\"", "empty_entry"},
		{"\" pg1 , \"", "empty_entry"},
		{"\"PG1\"", "invalid_name"},
		{"\"-pg1\"", "invalid_name"},
		{"\"pg.1\"", "invalid_name"},
		{"\"pg1,pg1\"", "duplicate_entry"},
		{"\"pg-prod,pg_prod,pg-prod\"", "duplicate_entry"},
	}
	for _, c := range cases {
		_, err := loadDatabases(t, "\n    labels:\n      fleetly.databases: "+c.label+"\n")
		if err == nil {
			t.Errorf("label %s accepted (want rejection)", c.label)
			continue
		}
		ae := asAppErrDatabases(t, err)
		if ae.Code() != "E_COMPOSE_UNSUPPORTED" {
			t.Errorf("label %s: code = %s, want E_COMPOSE_UNSUPPORTED", c.label, ae.Code())
		}
		if got := ae.Context()["reason"]; got != c.reason {
			t.Errorf("label %s: reason = %q, want %q", c.label, got, c.reason)
		}
		if !strings.Contains(err.Error(), LabelDatabases) {
			t.Errorf("label %s: error does not name the label: %v", c.label, err)
		}
	}
}

// TestDatabasesLabelKnownNamespace 登记 fleetly.databases 后保留命名空间守
// 卫放行该键；其它未知 fleetly.* 键依旧拒绝（负面对照）。
func TestDatabasesLabelKnownNamespace(t *testing.T) {
	if _, err := loadDatabases(t, "\n    labels:\n      fleetly.databases: \"pg1\"\n"); err != nil {
		t.Fatalf("known key rejected: %v", err)
	}
	_, err := loadDatabases(t, "\n    labels:\n      fleetly.unknown-key: \"x\"\n")
	if err == nil || !strings.Contains(err.Error(), "E_LABEL_RESERVED") {
		t.Fatalf("unknown fleetly.* key: err = %v, want E_LABEL_RESERVED", err)
	}
}

// TestDatabasesLabelNormalizedAndHashed 归一化形态进 canonical JSON（参与
// spec_hash）：label 增删改变哈希；书写顺序不同的同一实例集哈希稳定。
func TestDatabasesLabelNormalizedAndHashed(t *testing.T) {
	with, err := loadDatabases(t, "\n    labels:\n      fleetly.databases: \"pg1\"\n")
	if err != nil {
		t.Fatalf("with: %v", err)
	}
	if !rawJSONContains(t, with, `"databases":["pg1"]`) {
		t.Fatal("normalized snapshot must carry the databases list (desired-hash participates)")
	}
	without, err := loadDatabases(t, "")
	if err != nil {
		t.Fatalf("without: %v", err)
	}
	if len(without.Services[0].Databases) != 0 {
		t.Fatal("absent label must normalize to an empty list")
	}
	if with.SpecHash == without.SpecHash {
		t.Fatal("fleetly.databases label did not change the spec hash")
	}
	// 顺序无关：同一实例集的两种书写 → 同一 spec_hash。
	a, err := loadDatabases(t, "\n    labels:\n      fleetly.databases: \"pg1,pg2\"\n")
	if err != nil {
		t.Fatalf("order a: %v", err)
	}
	b, err := loadDatabases(t, "\n    labels:\n      fleetly.databases: \"pg2,pg1\"\n")
	if err != nil {
		t.Fatalf("order b: %v", err)
	}
	if a.SpecHash != b.SpecHash {
		t.Fatalf("order-sensitive normalization: %s vs %s", a.SpecHash, b.SpecHash)
	}
}
