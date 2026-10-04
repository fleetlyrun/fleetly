package cmd

// Alerting/Metrics 动词 golden 双形态（F2.5，ADR-0041）：通道全生命周期
//（create→list→test（不可达端点的诚实失败）→delete）+ 规则（create→list
//→states→delete）+ 组裸形态 + 查询面（夹具未配置 metrics.addr → 精确
// 失败信封）。占位替换/ID 捕获是 verbs_golden_test.go 同款；删除步的
// --json 轮删除 create --json 轮铸的第二实体（删除不幂等——404 会漂移）。

import (
	"regexp"
	"strings"
	"testing"
)

func TestGoldenAlertingVerbs(t *testing.T) {
	h := newGoldenHarness(t)
	appID := goldenSeedDeploy(t, h)

	steps := []struct {
		verb string
		args []string
		code int
	}{
		{"metrics", []string{"metrics"}, 0},
		{"alerts", []string{"alerts"}, 0},
		{"alerts rules", []string{"alerts", "rules"}, 0},
		{"channels", []string{"channels"}, 0},
		{"channels list", []string{"channels", "list"}, 0},
		// 通道生命周期：webhook 指向不可达端点——test 的失败形态（exit 1）
		// 是通道验收的诚实面锚。
		{"channels create", []string{"channels", "create", "--name", "ops-hook", "--kind", "webhook", "--url", "ftp://127.0.0.1/nope"}, 0},
		{"channels list after create", []string{"channels", "list"}, 0},
		{"channels test", []string{"channels", "test", "--channel", "GOLDEN_CHANNEL"}, 1},
		{"channels delete", []string{"channels", "delete", "--channel", "GOLDEN_CHANNEL"}, 0},
		{"channels list after delete", []string{"channels", "list"}, 0},
		// 规则生命周期（states 含系统内置行 platform-offsite-backup——
		// 夹具 s3 未配但未满 24h 窗 → ok 形态）。
		{"alerts rules create", []string{"alerts", "rules", "create", "--app", "GOLDEN_APP", "--metric", "memory_working_set_bytes", "--threshold", "1048576", "--for-seconds", "300"}, 0},
		{"alerts rules list", []string{"alerts", "rules", "list"}, 0},
		{"alerts list", []string{"alerts", "list"}, 0},
		{"alerts rules delete", []string{"alerts", "rules", "delete", "--rule", "GOLDEN_RULE"}, 0},
		{"alerts rules list after delete", []string{"alerts", "rules", "list"}, 0},
		// 查询面：夹具未配置 metrics.addr → 精确失败；空查询 → usage 拒绝。
		{"metrics query", []string{"metrics", "query", "up"}, 1},
		{"metrics query empty", []string{"metrics", "query"}, 64},
	}

	// jsonRoundCode 是 --json 轮的退出码覆盖表（缺省同 code）。
	var jsonRoundCode = map[string]int{
		"channels test": 0,
	}

	var channelID, ruleID string         // 人类轮实体（步骤间引用）
	var jsonChannelID, jsonRuleID string // --json 轮 create 铸的第二实体（删除步消费）
	for _, st := range steps {
		t.Run(st.verb, func(t *testing.T) {
			args := st.args
			for i, a := range args {
				switch a {
				case "GOLDEN_APP":
					args[i] = appID
				case "GOLDEN_CHANNEL":
					args[i] = channelID
				case "GOLDEN_RULE":
					args[i] = ruleID
				}
			}
			code, out, stderr := runCLI(t, args...)
			if code != st.code {
				t.Fatalf("%s: code=%d stderr=%q args=%q", st.verb, code, stderr, args)
			}
			if st.code == 0 && stderr != "" {
				t.Fatalf("%s: unexpected stderr=%q", st.verb, stderr)
			}
			if st.verb == "channels create" {
				channelID = extractTailID(out)
			}
			if st.verb == "alerts rules create" {
				ruleID = extractAlertRuleID(t, out)
			}
			compareGolden(t, goldenFile(st.verb), normalizeGolden(out))

			// --json 轮：create 类换名铸第二实体；删除步删第二实体（同 ID
			// 二删会 404 漂移）。
			jsonArgs := append([]string{}, args...)
			switch st.verb {
			case "channels create":
				for i, a := range jsonArgs {
					if a == "ops-hook" {
						jsonArgs[i] = "ops-hook-json"
					}
				}
			case "alerts rules create":
				for i, a := range jsonArgs {
					if a == "1048576" {
						jsonArgs[i] = "5242880"
					}
				}
			case "channels delete":
				replaceArgValue(jsonArgs, channelID, jsonChannelID)
			case "alerts rules delete":
				replaceArgValue(jsonArgs, ruleID, jsonRuleID)
			}
			wantJSONCode := st.code
			if over, ok := jsonRoundCode[st.verb]; ok {
				wantJSONCode = over
			}
			code, out, stderr = runCLI(t, append(jsonArgs, "--json")...)
			if code != wantJSONCode {
				t.Fatalf("%s --json: code=%d want=%d stderr=%q", st.verb, code, wantJSONCode, stderr)
			}
			if wantJSONCode == 0 && stderr != "" {
				t.Fatalf("%s --json: unexpected stderr=%q", st.verb, stderr)
			}
			switch st.verb {
			case "channels create":
				jsonChannelID = extractProtoJSONID(t, out)
			case "alerts rules create":
				jsonRuleID = extractProtoJSONID(t, out)
			}
			compareGolden(t, goldenFile(st.verb)+"-json", normalizeGolden(out))
		})
	}
}

// replaceArgValue 把切片中首个 from 替换为 to（删除步的 --json 轮指向
// 第二实体）。
func replaceArgValue(args []string, from, to string) {
	for i, a := range args {
		if a == from {
			args[i] = to
			return
		}
	}
}

// extractAlertRuleID 从 alerts rules create 的人类形态提取规则 ID
// （"created alert rule X (...)"——第 4 词）。
func extractAlertRuleID(t *testing.T, out string) string {
	t.Helper()
	fields := strings.Fields(out)
	if len(fields) < 4 {
		t.Fatalf("cannot extract rule id from output: %q", out)
	}
	return fields[3]
}

// extractProtoJSONID 从 protojson 输出提取 ULID 形 id（"id": "X"）。
var protoJSONIDRe = regexp.MustCompile(`"id":\s*"([0-9A-HJKMNP-TV-Z]{26})"`)

func extractProtoJSONID(t *testing.T, out string) string {
	t.Helper()
	m := protoJSONIDRe.FindStringSubmatch(out)
	if len(m) < 2 {
		t.Fatalf("cannot extract id from protojson output: %q", out)
	}
	return m[1]
}
