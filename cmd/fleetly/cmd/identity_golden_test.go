package cmd

// Identity 动词 golden 双形态（F0.22 身份面）：whoami/login/users/roles/
// teams/tokens/audit 全链（同夹具顺序流；凭据文件经 FLEETLY_CREDENTIALS
// 指向 temp——golden 决不触碰真实用户配置目录）。

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	identityv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/identity/v1"
)

var (
	// secretRe 是出货面凭据扫描锚（H10：节点个体 relay 凭证 fltrelay_
	// 与前缀族同批——golden 误把节点凭证打进出货面即红）。
	secretRe  = regexp.MustCompile(`(flt_|fltinv_|fltrelay_)[A-Za-z0-9_-]+`)
	tailIDRef = regexp.MustCompile(`\(id ([a-z0-9][a-z0-9-]*)\)`)
)

func TestGoldenIdentityVerbs(t *testing.T) {
	h := newGoldenHarness(t)
	t.Setenv("FLEETLY_CREDENTIALS", filepath.Join(t.TempDir(), "credentials"))

	var userID, tokenID string
	steps := []struct {
		verb string
		args []string
	}{
		{"whoami", []string{"whoami"}},
		{"login", []string{"login", "--token", "GOLDEN_TOKEN"}},
		{"users create", []string{"users", "create", "--role", "builtin-admin", "alice"}},
		{"users list", []string{"users", "list"}},
		// 密码凭证面（C6 第一期）：初始密码建用户 → set-password 重置 →
		// 密码 login（服务端铸 Token；secret 只落 credentials 不出 stdout）。
		{"users create password", []string{"users", "create", "--role", "builtin-member", "--password", "init-pw-1", "bob"}},
		{"users set-password", []string{"users", "set-password", "--password", "s3cret-pw", "GOLDEN_USER"}},
		{"login password", []string{"login", "--name", "alice", "--password", "s3cret-pw"}},
		{"roles create", []string{"roles", "create", "--scope", "deployments:write", "deployer"}},
		{"roles list", []string{"roles", "list"}},
		{"teams create", []string{"teams", "create", "shop"}},
		{"teams list", []string{"teams", "list"}},
		{"tokens create", []string{"tokens", "create", "--role", "builtin-admin", "--user", "GOLDEN_USER", "alice-cli"}},
		{"tokens list", []string{"tokens", "list"}},
		{"users invite", []string{"users", "invite", "--role", "builtin-member"}},
		{"audit", []string{"audit", "--limit", "50"}},
		{"audit before", []string{"audit", "--before", "10", "--limit", "50"}},
	}

	var inviteSecret string
	// jsonRoundOverrides 是 --json 轮的位置替换（create/invite 类换名撞
	// 唯一约束；list/读类幂等直跑）。
	jsonRoundOverrides := map[string]map[int]string{
		"users create":          {4: "alice-json"},
		"users create password": {6: "bob-json"},
		"roles create":          {4: "deployer-json"},
		"teams create":          {2: "shop-json"},
		"tokens create":         {6: "alice-cli-json"},
	}
	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			args := st.args
			for i, a := range args {
				switch a {
				case "GOLDEN_TOKEN":
					args[i] = h.Token
				case "GOLDEN_USER":
					args[i] = userID
				}
			}
			code, out, stderr := runCLI(t, args...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s: code=%d stderr=%q", st.verb, code, stderr)
			}
			if m := tailIDRef.FindStringSubmatch(out); m != nil && st.verb == "users create" {
				userID = m[1]
			}
			if st.verb == "tokens create" {
				if m := tailIDRef.FindStringSubmatch(out); m != nil {
					tokenID = m[1]
				}
			}
			if st.verb == "users invite" {
				inviteSecret = secretRe.FindString(out)
				if inviteSecret == "" {
					t.Fatalf("invite output missing secret: %q", out)
				}
			}
			compareGolden(t, goldenFile(st.verb), normalizeGolden(out))

			// --json 轮（机器契约双形态）。
			jsonArgs := append([]string{}, args...)
			if over, ok := jsonRoundOverrides[st.verb]; ok {
				for i, v := range over {
					jsonArgs[i] = v
				}
			}
			code, out, stderr = runCLI(t, append(jsonArgs, "--json")...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s --json: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb)+"-json", normalizeGolden(out))
		})
	}

	// 审计导出面（append-only 归档路径）：--export 以 before_seq 翻页，
	// 每行一条 protojson——页大小 2 必然跨多页；再与单页大 limit 的全量
	// 读取逐条比对（翻页不漏不重）。golden 在收尾写操作之前生成，条目
	// 与上方 audit 步骤同集。
	t.Run("audit export", func(t *testing.T) {
		code, out, stderr := runCLI(t, "audit", "--export", "--limit", "2")
		if code != 0 || stderr != "" {
			t.Fatalf("audit --export: code=%d stderr=%q", code, stderr)
		}
		compareGolden(t, "audit-export", normalizeGolden(out))
		exported := parseAuditJSONL(t, out)
		if len(exported) < 3 {
			t.Fatalf("export with --limit 2 must span multiple pages, got %d entries", len(exported))
		}

		code, fullOut, stderr := runCLI(t, "audit", "--limit", "1000", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("audit --limit 1000 --json: code=%d stderr=%q", code, stderr)
		}
		var full identityv1.ListAuditResponse
		if err := protojson.Unmarshal([]byte(fullOut), &full); err != nil {
			t.Fatalf("parse full audit page: %v", err)
		}
		if len(exported) != len(full.GetEntries()) {
			t.Fatalf("export paged %d entries, single page has %d", len(exported), len(full.GetEntries()))
		}
		for i, e := range exported {
			if e.GetId() == "" || e.GetSeq() <= 0 || e.GetAction() == "" {
				t.Fatalf("export line %d missing identity fields: %v", i+1, e)
			}
			want := full.GetEntries()[i]
			if e.GetSeq() != want.GetSeq() || e.GetId() != want.GetId() {
				t.Fatalf("export line %d = (%d,%s), want (%d,%s)",
					i+1, e.GetSeq(), e.GetId(), want.GetSeq(), want.GetId())
			}
			if i > 0 && e.GetSeq() >= exported[i-1].GetSeq() {
				t.Fatalf("export seq must be strictly descending: line %d seq %d >= %d",
					i+1, e.GetSeq(), exported[i-1].GetSeq())
			}
		}
	})

	// --export 与 --json 同给：导出胜出（同一 JSONL，无人类表格/JSON 信封）。
	t.Run("audit export json", func(t *testing.T) {
		code, out, stderr := runCLI(t, "audit", "--export", "--limit", "2", "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("audit --export --json: code=%d stderr=%q", code, stderr)
		}
		code, plain, stderr := runCLI(t, "audit", "--export", "--limit", "2")
		if code != 0 || stderr != "" {
			t.Fatalf("audit --export: code=%d stderr=%q", code, stderr)
		}
		if out != plain {
			t.Fatalf("--export --json must win and match plain export\n--json:\n%s\nplain:\n%s", out, plain)
		}
	})

	// --before 是导出首页游标（严格更旧）：--limit 1 强制逐行翻页。
	t.Run("audit export before", func(t *testing.T) {
		code, out, stderr := runCLI(t, "audit", "--export", "--before", "10", "--limit", "1")
		if code != 0 || stderr != "" {
			t.Fatalf("audit --export --before: code=%d stderr=%q", code, stderr)
		}
		compareGolden(t, "audit-export-before", normalizeGolden(out))
		entries := parseAuditJSONL(t, out)
		if len(entries) < 2 {
			t.Fatalf("--limit 1 on a multi-row fixture must page, got %d entries", len(entries))
		}
		if first := entries[0].GetSeq(); first != 9 {
			t.Fatalf("--before 10 must start at seq 9, got %d", first)
		}
		for i, e := range entries {
			if e.GetSeq() >= 10 {
				t.Fatalf("export line %d seq %d must be < before cursor 10", i+1, e.GetSeq())
			}
			if i > 0 && e.GetSeq() >= entries[i-1].GetSeq() {
				t.Fatalf("export seq must be strictly descending: line %d seq %d >= %d",
					i+1, e.GetSeq(), entries[i-1].GetSeq())
			}
		}
	})

	// 过滤条件在导出翻页全程生效（source=api 仅两条登录行；--limit 1 跨页）。
	t.Run("audit export filters", func(t *testing.T) {
		code, out, stderr := runCLI(t, "audit", "--export", "--source", "api", "--limit", "1")
		if code != 0 || stderr != "" {
			t.Fatalf("audit --export --source api: code=%d stderr=%q", code, stderr)
		}
		entries := parseAuditJSONL(t, out)
		if len(entries) != 2 {
			t.Fatalf("source=api export must page through the 2 login rows, got %d", len(entries))
		}
		for i, e := range entries {
			if e.GetSource() != "api" {
				t.Fatalf("export line %d source = %q, want api", i+1, e.GetSource())
			}
			if e.GetAction() != "user.login" {
				t.Fatalf("export line %d action = %q, want user.login", i+1, e.GetAction())
			}
		}
	})

	// 空结果：无表头、无信封，stdout 全空（归档脚本可安全重定向）。
	t.Run("audit export empty", func(t *testing.T) {
		code, out, stderr := runCLI(t, "audit", "--export", "--before", "1")
		if code != 0 || stderr != "" {
			t.Fatalf("audit --export --before 1: code=%d stderr=%q", code, stderr)
		}
		if out != "" {
			t.Fatalf("empty export must write nothing, got %q", out)
		}
	})

	// 依赖前序状态的收尾步骤（吊销要 token id、accept 要邀请 secret）。
	t.Run("tokens revoke", func(t *testing.T) {
		if tokenID == "" {
			t.Fatal("no token id captured")
		}
		code, out, stderr := runCLI(t, "tokens", "revoke", tokenID)
		if code != 0 || stderr != "" {
			t.Fatalf("tokens revoke: code=%d stderr=%q", code, stderr)
		}
		compareGolden(t, "tokens-revoke", normalizeGolden(out))
	})
	t.Run("tokens revoke json", func(t *testing.T) {
		code, out, stderr := runCLI(t, "tokens", "revoke", tokenID, "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("tokens revoke --json: code=%d stderr=%q", code, stderr)
		}
		compareGolden(t, "tokens-revoke-json", normalizeGolden(out))
	})
	t.Run("users accept", func(t *testing.T) {
		if inviteSecret == "" {
			t.Fatal("no invitation secret captured")
		}
		code, out, stderr := runCLI(t, "users", "accept", "--token", inviteSecret, "--name", "carol")
		if code != 0 || stderr != "" {
			t.Fatalf("users accept: code=%d stderr=%q", code, stderr)
		}
		compareGolden(t, "users-accept", normalizeGolden(out))
	})
	t.Run("users accept json", func(t *testing.T) {
		// 已消费：断言失败面（单次使用）也是机器契约。
		code, _, stderr := runCLI(t, "users", "accept", "--token", inviteSecret, "--name", "dave", "--json")
		if code != 1 {
			t.Fatalf("second accept must fail with exit 1, got %d", code)
		}
		compareGolden(t, "users-accept-json", normalizeGolden(stderr))
	})
}

// TestAuditPageLimit 钉死导出分页的页大小钳制（与服务端首页/上限同口径）：
// 非正值回默认 100，超上限截到 1000。
func TestAuditPageLimit(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want int32
	}{
		{-1, 100}, {0, 100}, {1, 1}, {100, 100}, {1000, 1000}, {1001, 1000},
	} {
		if got := auditPageLimit(tc.in); got != tc.want {
			t.Errorf("auditPageLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// parseAuditJSONL 把 --export 输出逐行按 protojson 解析（每行一条
// AuditEntry）：JSONL 形状与字段契约由 protojson 本身执法。
func parseAuditJSONL(t *testing.T, out string) []*identityv1.AuditEntry {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	entries := make([]*identityv1.AuditEntry, 0, len(lines))
	for i, line := range lines {
		if line == "" {
			t.Fatalf("audit export line %d is empty", i+1)
		}
		e := &identityv1.AuditEntry{}
		if err := protojson.Unmarshal([]byte(line), e); err != nil {
			t.Fatalf("audit export line %d is not protojson: %v\nline: %s", i+1, err, line)
		}
		entries = append(entries, e)
	}
	return entries
}
