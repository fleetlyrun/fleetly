package finding

// builtins 是首发条目集（docs/design/2026-10-30-zeabur-oss-borrowing.md
// §2.5 首批规则集；每规则至少一条 + unexplained 兜底专用条目）。纪律：
// 只增——新条目必须带 Source 锚点并在生产代码出现（usage 反扫），预留条目
// 进 usage_test.go 豁免清单并在此注明。
//
// Source 锚 = 规则 file path + 函数 + 提案/ADR 锚（docs §2.2）；file path
// 是 diagnosis 域环包内的相对路径（model 注册表保持编排器词汇零命中，
// irguard 执法面）。
var builtins = []Finding{
	{
		ID:         "F_DIAGNOSIS_UNEXPLAINED",
		Summary:    "The workload is failing but no built-in rule attributes the cause yet; evidence is attached for manual triage.",
		Severity:   SeverityWarning,
		FaultClass: FaultClassApp,
		Source:     "diagnosis/unexplained.go unexplainedGaps (docs/design/2026-10-30-zeabur-oss-borrowing.md §2.1 unexplained 兜底, ADR-0085)",
		Fix: Fix{
			Verb:        "",
			Params:      nil,
			ConsolePath: "/p/{project_id}/apps/{app_id}",
		},
	},
	{
		ID:         "F_WORKLOAD_CRASH_LOOP",
		Summary:    "The workload keeps terminating and restarting in the near window.",
		Severity:   SeverityWarning,
		FaultClass: FaultClassApp,
		Source:     "diagnosis/rules_workload.go ruleCrashLoop (docs/design/2026-10-30-zeabur-oss-borrowing.md §2.5, ADR-0085)",
		Fix: Fix{
			// restart/scale 动词的深链直达进程面（修复批 F4：/processes 子页
			// 是动词落点——app 根页多一跳）。
			Verb:        "restart",
			Params:      nil,
			ConsolePath: "/p/{project_id}/apps/{app_id}/processes",
		},
	},
	{
		ID:         "F_WORKLOAD_CRASH_LOOP_BAD_COMMAND",
		Summary:    "The workload crash-loops because its command cannot run (exit 126: not executable; exit 127: command not found).",
		Severity:   SeverityWarning,
		FaultClass: FaultClassDeclaration,
		Source:     "diagnosis/rules_workload.go ruleCrashLoop (exit-code variant; docs/design/2026-10-30-zeabur-oss-borrowing.md §2.5, ADR-0085)",
		Fix: Fix{
			// 命令/入口点修正是声明改动：不带 verb，深链进部署面走 B1 预览（§2.2 动词面裁决）。
			Verb:        "",
			Params:      map[string]string{"field": "command"},
			ConsolePath: "/p/{project_id}/apps/{app_id}",
		},
	},
	{
		ID:         "F_WORKLOAD_HEALTH_CHECK_FAILING",
		Summary:    "The workload keeps failing its health check.",
		Severity:   SeverityWarning,
		FaultClass: FaultClassApp,
		Source:     "diagnosis/rules_workload.go ruleHealthCheck (docs/design/2026-10-30-zeabur-oss-borrowing.md §2.5, ADR-0085)",
		Fix: Fix{
			Verb:        "",
			Params:      map[string]string{"field": "healthcheck"},
			ConsolePath: "/p/{project_id}/apps/{app_id}",
		},
	},
	{
		ID:         "F_WORKLOAD_IMAGE_PULL_FAILING",
		Summary:    "The runtime cannot pull the workload image (bad image reference or registry unreachable).",
		Severity:   SeverityWarning,
		FaultClass: FaultClassExternal,
		Source:     "diagnosis/rules_workload.go ruleImagePull (docs/design/2026-10-30-zeabur-oss-borrowing.md §2.5, ADR-0085)",
		Fix: Fix{
			Verb:        "",
			Params:      map[string]string{"field": "image"},
			ConsolePath: "/p/{project_id}/apps/{app_id}",
		},
	},
	{
		ID:         "F_WORKLOAD_OOM_WITH_LIMIT",
		Summary:    "The workload was killed for exceeding its declared memory limit.",
		Severity:   SeverityWarning,
		FaultClass: FaultClassApp,
		Source:     "diagnosis/rules_workload.go ruleOOM (limit variant; docs/design/2026-10-30-zeabur-oss-borrowing.md §2.5, ADR-0085)",
		Fix: Fix{
			Verb:        "",
			Params:      map[string]string{"field": "memory"},
			ConsolePath: "/p/{project_id}/apps/{app_id}",
		},
	},
	{
		ID:         "F_WORKLOAD_OOM_WITHOUT_LIMIT",
		Summary:    "The workload was killed by node memory pressure with no memory limit declared.",
		Severity:   SeverityWarning,
		FaultClass: FaultClassApp,
		Source:     "diagnosis/rules_workload.go ruleOOM (no-limit variant; docs/design/2026-10-30-zeabur-oss-borrowing.md §2.5, ADR-0085)",
		Fix: Fix{
			Verb:        "",
			Params:      map[string]string{"field": "memory"},
			ConsolePath: "/p/{project_id}/apps/{app_id}",
		},
	},
	{
		ID:         "F_WORKLOAD_STOPPED_EXPLAINED",
		Summary:    "The workload is stopped as declared; it stays stopped until the declaration changes.",
		Severity:   SeverityAdvice,
		FaultClass: FaultClassDeclaration,
		Source:     "diagnosis/rules_steadystate.go ruleStopped (看门狗语义解释化; docs/design/2026-10-30-zeabur-oss-borrowing.md §2.5, ADR-0085)",
		Fix: Fix{
			// scale 是 §8.10 已裁一等动词（现场漂移 Fix 的 verb 落点同源）；声明 replicas=1 即启动。
			Verb:        "scale",
			Params:      map[string]string{"replicas": "1"},
			ConsolePath: "/p/{project_id}/apps/{app_id}/processes",
		},
	},
}
