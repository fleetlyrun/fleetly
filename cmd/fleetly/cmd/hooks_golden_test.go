package cmd

// Hooks 动词 golden 双形态（F0.13 CLI 面）：set（首配铸造，secret 一次）
// → get（配置面 + 前缀形态）→ set 再跑（不换 Token）→ rotate（双换）。

import (
	"regexp"
	"testing"
)

var hookSecretRe = regexp.MustCompile(`flthook_[A-Za-z0-9_-]{4,}`)

func TestGoldenHooksVerbs(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	type step struct {
		verb string
		args []string
	}
	steps := []step{
		{"hooks set", []string{"hooks", "set", "--app", appID, "--repo", "https://github.com/acme/shop.git",
			"--branch", "main", "--dockerfile", "Dockerfile", "--watch", "web/", "--watch", "libs/core"}},
		{"hooks get", []string{"hooks", "get", "--app", appID}},
	}
	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			code, out, stderr := runCLI(t, st.args...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb), normalizeGolden(out))

			code, out, stderr = runCLI(t, append(st.args, "--json")...)
			if code != 0 || stderr != "" {
				t.Fatalf("%s --json: code=%d stderr=%q", st.verb, code, stderr)
			}
			compareGolden(t, goldenFile(st.verb)+"-json", normalizeGolden(out))
		})
	}

	// 再配置不换 Token（secret 字段空）；rotate 双换（新 secret 一次）。
	t.Run("hooks set update", func(t *testing.T) {
		code, out, stderr := runCLI(t, "hooks", "set", "--app", appID, "--repo", "https://github.com/acme/shop.git")
		if code != 0 || stderr != "" {
			t.Fatalf("hooks set update: code=%d stderr=%q", code, stderr)
		}
		if hookSecretRe.MatchString(out) {
			t.Fatalf("re-configuring must not mint a new token: %q", out)
		}
		compareGolden(t, "hooks-set-update", normalizeGolden(out))
	})

	t.Run("hooks rotate", func(t *testing.T) {
		code, out, stderr := runCLI(t, "hooks", "rotate", "--app", appID)
		if code != 0 || stderr != "" {
			t.Fatalf("hooks rotate: code=%d stderr=%q", code, stderr)
		}
		if hookSecretRe.FindString(out) == "" {
			t.Fatalf("rotate output missing secret: %q", out)
		}
		compareGolden(t, "hooks-rotate", normalizeGolden(out))

		code, out, stderr = runCLI(t, "hooks", "rotate", "--app", appID, "--json")
		if code != 0 || stderr != "" {
			t.Fatalf("hooks rotate --json: code=%d stderr=%q", code, stderr)
		}
		compareGolden(t, "hooks-rotate-json", normalizeGolden(out))
	})
}
