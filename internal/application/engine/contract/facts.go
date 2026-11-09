// facts.go 是 engine/contract 的取证/诊断事实契约面（P5 wave-2 随 execs 与
// metrics 两卫星拆包下沉，2026-11-01）：engine 核与卫星共享的纯函数帮手
// 与端口签名在此单源——核侧经包内别名零漂移，卫星侧直接消费（wave-1 的
// SleepCtx/LogIngestBatchFrames 同款按需最小下沉）。
package contract

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// EmitLedger 是取证事实发射端口的签名（engine 核 emitLedger 方法 = 唯一
// 生产实现：LedgerOnly 审计行，无 outbox 孪生）。P5 wave-2 随 execs 拆包
// 下沉——中继活体顶替的审计事件（node.relay_replaced）经本端口注入卫星
// （构造面绑定核方法值；单写发射面留核，ADR-0088 决策 3）。
type EmitLedger func(ctx context.Context, action, resource, detail string) error

// WorkloadID 合成平台 Workload ID（app ULID + process 名：唯一、稳定、
// 人读可分解——归属与 Drift 判定的锚，Provider 把它搬运到载体标记）。
// 公式真源在此导出：API/CLI 面按 process 过滤日志、exec 受理/实例解析与
// engine 核投影面（projection/processrestart）复用同一公式，不得各自拼
// 接（散落副本一旦漂移即静默失配）。P5 wave-2 自 engine 包下沉（execs
// 卫星首消费；engine 侧经 var 别名零漂移）。
func WorkloadID(appID, process string) string {
	return appID + "-" + process
}

// AuditDetailJSON 编码审计 Detail（结构受控；编码失败退回空串——审计行
// 仍落，动作可见性不因 Detail 缺失而丢失）。engine 核（nodes/converge）
// 与 execs 卫星（中继顶替审计）同一编码契约——P5 wave-2 下沉单源。
func AuditDetailJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// ContainerExitFact 判定观测是否容器终态事实（B2a W1b facts 地基）：异常
// 终止（failed/rejected 终态观测）或非零退出（OOM 的 137 形态落此）；正常
// 完成（退出码 0）不是异常终态，不入账。engine 核观测面（observ.go 终态
// 去抖/事实入账）与 metrics 卫星 crash-loop 进入沿评估同一判据——P5
// wave-2 下沉单源（核侧经 var 别名零漂移）。
func ContainerExitFact(ev capability.WorkloadEvent) bool {
	if ev.State == capability.WorkloadFailed {
		return true
	}
	return ev.ExitCode != nil && *ev.ExitCode != 0
}

// DiagnoseWindow 是诊断近窗口径：终态计数与事件面共用同一窗（崩溃循环
// 阈值 diagnosis.CrashLoopThreshold 在窗内计数——规则阈值与装配窗口分离，
// 窗口是装配面参数而非域知识）。engine 核诊断 facts 装配（diagnose.go）
// 与 metrics 卫星 crash-loop 计数（crashnotify.go）同一窗——P5 wave-2
// 下沉单源（核侧经常量别名零漂移）。
const DiagnoseWindow = 10 * time.Minute

// DBCredentialSecretPrefix 是数据库凭证 Secret 的命名约定前缀
// （database:<name>；ADR-0029 决策 6——完整连接串的单真源封装面）。
// P5 wave-3 下沉（WorkloadID 同款：公式真源三方消费——engine 核铸名/
// 前缀判定、gRPC API 创建面、browses 卫星凭证回读）。
const DBCredentialSecretPrefix = "database:"

// DBCredentialSecretName 铸凭证 Secret 名（API 创建面与投影共用的单源
// 公式）。P5 wave-3 自 engine resolve.go 下沉（核侧经声明别名零漂移，
// 解析面锚不动）。
func DBCredentialSecretName(databaseName string) string {
	return DBCredentialSecretPrefix + databaseName
}

// PubCredentialSecretPrefix 是 PubSub 凭证 Secret 的命名约定前缀
// （pubsub:<name>；ADR-0096 决策 4——连接材料方言串的单真源封装面）。
const PubCredentialSecretPrefix = "pubsub:"

// PubCredentialSecretName 铸 PubSub 凭证 Secret 名（API 创建面与投影共用）。
func PubCredentialSecretName(pubsubName string) string {
	return PubCredentialSecretPrefix + pubsubName
}

// PubSubDNSName 铸 per-PubSub 稳定 DNS 名（pub-<id>，ADR-0096 决策 2——
// engine/contract 铸名族同源下沉；Provider 经 Addressing 声明映射）。
func PubSubDNSName(pubsubID string) string {
	return "pub-" + strings.ToLower(pubsubID)
}

// BKTCredentialSecretPrefix 是 Bucket Store 凭证 Secret 的命名约定前缀
// （bucketstore:<name>；B5，ADR-0096 决策 4——endpoint/AK/SK k=v 方言串的
// 单真源封装面）。
const BKTCredentialSecretPrefix = "bucketstore:"

// BKTCredentialSecretName 铸 Bucket Store 凭证 Secret 名（API 创建面与
// 投影共用；G101 纪律：凭证形态字面量拼接）。
func BKTCredentialSecretName(bucketStoreName string) string {
	return BKTCredentialSecretPrefix + bucketStoreName
}

// BucketStoreDNSName 铸 per-BucketStore 稳定 DNS 名（bkt-<id>，B5，
// ADR-0096 决策 2——engine/contract 铸名族同源）。
func BucketStoreDNSName(bucketStoreID string) string {
	return "bkt-" + strings.ToLower(bucketStoreID)
}

// DatabaseDNSName 铸 per-Database 稳定 DNS 名（engine 铸名公式真源，
// TaskDNSName 先例；Provider 经 Addressing 声明映射为自己的原语）。
// P5 wave-3 自 engine resolve.go 下沉（核 rotate/database/projection、
// gRPC API 投影面与 backups 卫星恢复渲染三方消费，核侧经声明别名零
// 漂移、解析面锚不动）。
func DatabaseDNSName(databaseID string) string {
	return "db-" + strings.ToLower(databaseID)
}

// ManagedFingerprint 返回 Workload 集的稳定指纹（json.Marshal 对 map 键
// 排序——确定性序列化；序列化失败退化为逐次唯一值——保守推进，宁重下发
// 不漏变更）。受管/Database/Browse 三域的 Ensure 签名构造共用——P5
// wave-3 下沉（engine 核 converge 与 browses 卫星同一口径，核侧经 var
// 别名零漂移）。
func ManagedFingerprint(ws []capability.Workload) string {
	b, err := json.Marshal(ws)
	if err != nil {
		return fmt.Sprintf("unserializable-%p", &ws)
	}
	return string(b)
}

// MaterialsFingerprint 返回材料的稳定指纹（json.Marshal 对 map 键排序；
// []byte 走 base64——确定性序列化。序列化失败退化为逐次唯一值——保守
// 推进，宁重下发不漏变更，ManagedFingerprint 同款取舍）。P5 wave-3 下沉
// （核侧经 var 别名零漂移）。
func MaterialsFingerprint(m capability.Materials) string {
	b, err := json.Marshal(struct {
		RegistryAuth map[string]capability.RegistryCredential `json:"registry_auth,omitempty"`
		SecretFiles  map[string][]byte                        `json:"secret_files,omitempty"`
	}{m.RegistryAuth, m.SecretFiles})
	if err != nil {
		return fmt.Sprintf("unserializable-%p", &m)
	}
	return string(b)
}

// EgressSubset 返回 names 中声明 egress:none 的子集（投影期网络事实过滤
// ——egress 集来自 networks 表 ListByProject）。engine 核投影面（Project/
// ProjectTask）与 databases 卫星的 Database 投影同一公式——P5 wave-4 下沉
// 单源（核侧经 var 别名零漂移，ManagedFingerprint 先例）。
func EgressSubset(names []string, egress map[string]bool) []string {
	if len(egress) == 0 || len(names) == 0 {
		return nil
	}
	var out []string
	for _, n := range names {
		if egress[n] {
			out = append(out, n)
		}
	}
	return out
}

// StderrTail 截取 stderr 尾部（错误报文面；有界防刷屏）。纯串帮手，
// engine 核（观测面/rotate 工具取证）与 backups 卫星（备份/恢复/restic
// 报文面）共用——P5 wave-3 下沉（核侧经 var 别名零漂移）。
func StderrTail(s string) string {
	const max = 512
	if len(s) <= max {
		return TrimTrailing(s)
	}
	return TrimTrailing(s[len(s)-max:])
}

// TrimTrailing 去尾部换行/空格（StderrTail/各尾部捕获 String 的共用
// 底座）。P5 wave-3 下沉（核侧经 var 别名零漂移）。
func TrimTrailing(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

// BuildEventPayload 是 build.* 事件载荷的 schema（字段只增）。P5 wave-5
// 随 intake 拆包自 engine events.go 下沉（核 transitBuild 与卫星
// DriveBuilding 同一载荷契约；核侧经类型别名零漂移——schemareg 的载荷
// schema 注册与 backups 先例同款）。
type BuildEventPayload struct {
	BuildID    string `json:"build_id"`
	AppID      string `json:"app_id"`
	RevisionID string `json:"revision_id,omitempty"`
	State      string `json:"state"`
	Digest     string `json:"digest,omitempty"`
	Error      string `json:"error,omitempty"`
}

// BuildEventPayloadJSON 编码 Build 状态事件的载荷。engine 核（transition.go
// transitBuild 四件一的事实面）与 intake 卫星（DriveBuilding 的建行事实）
// 同一编码契约——P5 wave-5 下沉单源（核侧经 var 别名零漂移）。
func BuildEventPayloadJSON(b *build.Build) []byte {
	payload := BuildEventPayload{
		BuildID: b.ID, AppID: b.AppID, RevisionID: b.RevisionID,
		State: string(b.State), Digest: b.Digest, Error: b.Error,
	}
	out, _ := json.Marshal(payload)
	return out
}

// BuildEventName 把 Build 状态映射为事件名（字面量锚定）。engine 核
// （transitBuild）与 intake 卫星（DriveBuilding 建行事实）同一铸名——
// P5 wave-5 下沉单源（核侧经 var 别名零漂移，eventcode 注册表
// build.* 族同源）。
func BuildEventName(s build.State) string {
	switch s {
	case build.StateQueued:
		return "build.queued"
	case build.StateBuilding:
		return "build.building"
	case build.StateSucceeded:
		return "build.succeeded"
	case build.StateFailed:
		return "build.failed"
	case build.StateCancelled:
		return "build.cancelled"
	case build.StateExpired:
		return "build.expired"
	default:
		return "build." + strings.ReplaceAll(string(s), "-", "_")
	}
}
