package schema

// builtin Spec 条目（架构 §3 IR 三形态）：Spec 类型真源在 genproto
//（proto 是唯一契约源），本包以 protojson 形态反射出 JSON Schema。
// explain 的寻址名 = 领域名词（app/task/database）。

import (
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
)

func init() {
	Register(KindSpec, "app",
		"AppSpec: desired state of a long-running deployable unit (an app is one or more processes).",
		ReflectProto(&specv1.AppSpec{}))
	Register(KindSpec, "task",
		"TaskSpec: desired state of a programmatic workload (one-shot or resident, ADR-0012).",
		ReflectProto(&specv1.TaskSpec{}))
	Register(KindSpec, "database",
		"DatabaseSpec: desired state of a managed data service (template-rendered, rides the Runtime channel).",
		ReflectProto(&specv1.DatabaseSpec{}))
}
