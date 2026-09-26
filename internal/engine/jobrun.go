package engine

// 一次性 job 运行器的共享原语（DT-4，从 E5 Cron 的实现中抽出）：cron 调度
// 器（internal/cron）与发布管线的 init 相位（initjobs.go）共用同一套形态
// 约束与任务判定——抽取时 cron 行为逐字保持（trigger/pollInFlight 的调用
// 面只换成 engine.JobSpecFrom / engine.JobTaskVerdict，语义与文案不变）。
//
// 形态约束（swarm one-shot job 的既有坑位记忆，cron 先例）：
//   - 单副本、restart-condition=none（失败即 failed 终态、不重试——盲目
//     重启会放大半程产物，重试语义归编排层）；
//   - Global=false（副本模式）；substrate 适配器把 Job=true 翻译为
//     ReplicatedJob{MaxConcurrent:1, TotalCompletions:1} 且不写 UpdateConfig
//     （daemon 拒绝，substrate/services.go）；
//   - 服务 label 收敛为「受管件 + 归属 + 一次性运行锚」——模板携带的
//     deployment/desired-hash 归属 label 对 job 服务是谎，不继承。

import (
	"context"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// DefaultJobTimeout 是一次性 job 看门狗的平台缺省预算（10m；cron 家族与
// init 相位同值——internal/cron.DefaultJobTimeout 是本常量的别名）。
const DefaultJobTimeout = 10 * time.Minute

// JobVerdict 是一次性 job 的任务侧结论词表。
type JobVerdict string

const (
	// JobVerdictRunning 仍在途（任务未出现/运行中/底座读失败——最后一类
	// 由调用方的看门狗兜底重判）。
	JobVerdictRunning JobVerdict = ""
	// JobVerdictSucceeded 任务 complete。
	JobVerdictSucceeded JobVerdict = "succeeded"
	// JobVerdictFailed 任务 failed/rejected/shutdown（Reason 携带归因）。
	JobVerdictFailed JobVerdict = "failed"
)

// JobSpecFrom 由 Job 模板克隆一次性 job 服务的执行形态：改名、单副本、
// restart-condition=none、服务 label 收敛为受管件 + 归属 + extraLabels
// （调用方传入一次性运行锚：cron = fleetly.cron.run，init =
// fleetly.init.run）。模板的 env/命令/卷/网络/secret/资源/约束/健康检查
// 原样继承——投影与同服务部署同源。
func JobSpecFrom(template ServiceSpec, jobName string, extraLabels map[string]string) ServiceSpec {
	j := template
	j.Name = jobName
	j.Job = true
	j.Global = false
	j.Replicas = 1
	j.RestartPolicy = &RestartPolicySpec{Condition: "none"}
	labels := map[string]string{
		state.LabelManaged: state.ManagedLabelValue,
		state.LabelApp:     template.ServiceLabels[state.LabelApp],
		state.LabelProcess: template.ServiceLabels[state.LabelProcess],
	}
	if v := template.ServiceLabels[state.LabelTeam]; v != "" {
		labels[state.LabelTeam] = v
	}
	if v := template.ServiceLabels[state.LabelProject]; v != "" {
		labels[state.LabelProject] = v
	}
	for k, v := range extraLabels {
		if v != "" {
			labels[k] = v
		}
	}
	j.ServiceLabels = labels
	return j
}

// JobTaskVerdict 判定 job 服务的任务侧结论（TaskList 轮询的共享内核）：
// succeeded / failed（含原因）/ 空串（仍在途——无任务、任务运行中或底座
// 读失败〔下一拍重试，看门狗兜底〕）。归因文案与 cron 原实现逐字一致。
func JobTaskVerdict(ctx context.Context, sub Substrate, jobService string) (JobVerdict, string) {
	tasks, err := sub.TaskList(ctx, jobService)
	if err != nil {
		return JobVerdictRunning, "" // 底座暂态：下一拍重判（看门狗兜底）
	}
	for _, t := range tasks {
		switch t.State {
		case "complete":
			return JobVerdictSucceeded, ""
		case "failed", "rejected":
			return JobVerdictFailed, jobSingleLine(jobErrOrText(t.Err, "task "+t.State))
		case "shutdown":
			return JobVerdictFailed, "task was shut down before completion"
		}
	}
	return JobVerdictRunning, ""
}

// jobErrOrText 回退文案（任务 Err 为空/纯空白时的诚实缺省）。
func jobErrOrText(err, fallback string) string {
	if strings.TrimSpace(err) == "" {
		return fallback
	}
	return err
}

// jobSingleLine 是任务失败原因的单行化（禁换行 + 300 字符上限——事件
// payload 与台账列的脱敏契约；cron 原 singleLine 逐字同口径）。
func jobSingleLine(v string) string {
	msg := strings.ReplaceAll(v, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}
