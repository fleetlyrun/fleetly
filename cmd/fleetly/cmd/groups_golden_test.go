package cmd

// 动词组 golden（组名无参 = 子命令列表，双形态——组本身也是 --json 契约面）。

import (
	"testing"
)

func TestGoldenVerbGroups(t *testing.T) {
	groups := []string{"tokens", "users", "roles", "teams", "freeze", "projects", "apps", "secrets", "configs", "volumes", "networks", "databases", "deployments", "revisions", "builds", "uploads", "hooks", "tasks", "runs", "schedules", "routes", "nodes", "events", "platform"}
	for _, g := range groups {
		t.Run(g, func(t *testing.T) {
			code, out, stderr := runCLI(t, g)
			if code != 0 || stderr != "" {
				t.Fatalf("%s: code=%d stderr=%q", g, code, stderr)
			}
			compareGolden(t, g, out)

			code, out, stderr = runCLI(t, g, "--json")
			if code != 0 || stderr != "" {
				t.Fatalf("%s --json: code=%d stderr=%q", g, code, stderr)
			}
			compareGolden(t, g+"-json", out)
		})
	}
}
