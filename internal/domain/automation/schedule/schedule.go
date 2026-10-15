// Package schedule 是 Schedule 聚合的领域面（CONTEXT.md Schedule 词条：
// 周期触发规则，按时生成 Run；Avoid: cron 作实体名/timer；ADR-0075 Repo
// 接口层）。本包同时拥有带时区 cron 的解释面（ParseCron，ADR-0018）：
// IANA 时区名随行持久化，跨夏令时由 cron 库按墙钟解释；next_fire_at 是
// 绝对时刻（控制面重启后按墙钟续算，不依赖进程内计时器）。到期拍由
// engine 驱动环判定（对比在 Go 侧解析后进行——墙钟口径与 runs.deadline
// 一致，不做 SQL 字符串比较）。行类型、解释面与 Repo 接口在此单源，
// SQLite 实现住 infrastructure/persistence/schedule。
package schedule

import (
	"fmt"
	"strings"
	"time"
	// 嵌入 IANA 时区库（~450KB）：时区解释是控制面正确性面——容器形态
	//（无系统 zoneinfo）与裸 Windows 测试环境都不得随部署环境漂移。
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
)

// State 是 Schedule 状态机值（无中间态：活跃行到期即触发；删除是
// tombstone——在途的已铸 Task 不受影响，跑完自然收口）。
type State string

const (
	StateActive  State = "active"
	StateDeleted State = "deleted" // 终态 tombstone
)

// Terminal 报告是否终态。
func (s State) Terminal() bool { return s == StateDeleted }

// Schedule 是聚合行。
type Schedule struct {
	ID         string
	Seq        int64
	ProjectID  string
	Name       string
	State      State
	CronExpr   string // 5 字段表达式（minute hour dom month dow）
	Timezone   string // IANA 时区名（空按 UTC 解释；创建面归一为显式值）
	Spec       []byte // 冻结 TaskSpec 模板（protojson；task ref 由 fire 时铸造）
	NextFireAt string // RFC3339 绝对时刻（空 = 不触发）
	LastTaskID string // 最近一拍铸出的 Task（重叠 skip 判定锚）
	CreatedAt  string
	UpdatedAt  string
}

// ---- 带时区 cron 解释面（ADR-0018） ----

// cronParser 是 5 字段解析器（不支持 @descriptor——区间语义非墙钟，与其
// 让两种时间语义并存不如显式拒绝；CRON_TZ 前缀由 Parser.Parse 原生处理）。
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ParseCron 解析带时区的 5 字段 cron 表达式（timezone 是 IANA 时区名，空
// 按 UTC）。跨夏令时由 robfig/cron 按墙钟解释（SpecSchedule.Next 在目标
// 时区做墙钟算术——春跳不存在的时刻按 time.Date 归一，秋跳重叠时刻取
// 前一义）。表达式与库选型结论见 ADR-0018 附录 A。
func ParseCron(expr, timezone string) (cron.Schedule, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, fmt.Errorf("cron expression must not be empty")
	}
	if timezone == "" {
		timezone = "UTC"
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return nil, fmt.Errorf("timezone %q is not a valid IANA name: %v", timezone, err)
	}
	sched, err := cronParser.Parse("CRON_TZ=" + timezone + " " + strings.TrimSpace(expr))
	if err != nil {
		return nil, fmt.Errorf("invalid 5-field cron expression (minute hour day-of-month month day-of-week): %v", err)
	}
	return sched, nil
}
