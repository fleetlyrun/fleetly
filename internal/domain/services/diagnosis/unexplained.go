// unexplained.go 是完备性承诺的兜底（设计 §2.1）：unknown 永远不是全部
// 答案——存在失败态观测但没有任何解释性规则命中时，产出最小陈述条目
// （观测态 + 最近相关事件 + 建议动作）。守卫测试（unexplained_guard_test.go）
// 对「失败态输入全集」形态断言兜底永不空。
package diagnosis

import "strconv"

// unexplainedGaps 对未被任何解释性规则覆盖的失败态观测逐 workload 产出
// 兜底条目。covered 是已被解释的 workload id 集（RunExplainers 单源推导，
// 不维护第二份判定）。观测面 None 沉默（读不了 ≠ 有失败）。
func unexplainedGaps(facts Facts, covered map[string]bool) []Finding {
	obs, ok := facts.observations()
	if !ok {
		return nil
	}
	events, eventsReadable := facts.Events.Value()
	var latest EventFact
	var hasLatest bool
	if eventsReadable {
		latest, hasLatest = latestEvent(events)
	}
	var out []Finding
	for _, o := range obs {
		if !isFailureState(o.State) {
			continue // 只兜失败态（stopped 由稳态规则解释，running/completed 不是坏）
		}
		if covered[o.WorkloadID] {
			continue // 已有更具体的解释：兜底不叠床架屋
		}
		params := map[string]string{
			"state":         string(o.State),
			"restart_count": strconv.Itoa(o.RestartCount),
		}
		if code := exitCodeString(o.ExitCode); code != "" {
			params["exit_code"] = code
		}
		if meaning := ExitCodeMeaning(derefInt(o.ExitCode)); meaning != "" {
			params["exit_code_meaning"] = meaning
		}
		// 最近相关事件：App 域近窗内 Seq 最大者（None 面不猜——无事件证据
		// 也是诚实陈述的一部分）。
		var evidence []Evidence
		if hasLatest {
			params["recent_event"] = latest.Code
			evidence = append(evidence, Evidence{Kind: EvidenceEvent, Ref: "seq:" + strconv.FormatInt(latest.Seq, 10)})
		}
		out = append(out, instantiate("F_DIAGNOSIS_UNEXPLAINED", o, nil, params, evidence...))
	}
	return out
}
