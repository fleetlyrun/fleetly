package cmd

// Identity 动词 golden 双形态（F0.22 身份面）：whoami/login/users/roles/
// teams/tokens/audit 全链（同夹具顺序流；凭据文件经 FLEETLY_CREDENTIALS
// 指向 temp——golden 决不触碰真实用户配置目录）。

import (
	"path/filepath"
	"regexp"
	"testing"
)

var (
	secretRe  = regexp.MustCompile(`(flt_|fltinv_)[A-Za-z0-9_-]+`)
	tailIDRef = regexp.MustCompile(`\(id ([0-9A-HJKMNP-TV-Z]{26})\)`)
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
		{"roles create", []string{"roles", "create", "--scope", "deployments:write", "deployer"}},
		{"roles list", []string{"roles", "list"}},
		{"teams create", []string{"teams", "create", "shop"}},
		{"teams list", []string{"teams", "list"}},
		{"tokens create", []string{"tokens", "create", "--role", "builtin-admin", "--user", "GOLDEN_USER", "alice-cli"}},
		{"tokens list", []string{"tokens", "list"}},
		{"users invite", []string{"users", "invite", "--role", "builtin-member"}},
		{"audit", []string{"audit", "--limit", "50"}},
	}

	var inviteSecret string
	// jsonRoundOverrides 是 --json 轮的位置替换（create/invite 类换名撞
	// 唯一约束；list/读类幂等直跑）。
	jsonRoundOverrides := map[string]map[int]string{
		"users create":  {4: "alice-json"},
		"roles create":  {4: "deployer-json"},
		"teams create":  {2: "shop-json"},
		"tokens create": {6: "alice-cli-json"},
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
