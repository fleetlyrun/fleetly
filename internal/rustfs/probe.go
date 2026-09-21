package rustfs

// 平台侧 S3 消费面（E3-5，设计 §2.5「探测容器」形态）：fleetlyd 是宿主
// 进程——overlay 网络只对容器可达，宿主直连任务 IP 不可靠（dind 与真机
// 均可能不可路由）。因此「EnsureBucket」与「TestConnection 探针」都以
// **一次性 restic 容器**执行：attach fleetly-rustfs-net（容器内 DNS 直接
// 解析服务 alias，与上传轨 restic 容器同形态），经对象存储端点做真实的
// 认证/写/读/删除往返。执行器端口 = statebackup.ResticRunner（同一份
// ResticSpec 契约；生产实现 internal/substrate.Client，W3-S2 已接线）。
//
// 探针诚实契约（§2.1 的容器化等价形态）：通过 = 能认证（init 对端点鉴权）、
// 能写（backup 落对象）、能读回（snapshots 列表含该快照 id）、能删（forget
// --prune 收口）——任何一步失败即失败步可见。探针仓库固定
// fleetly/fleetly-probe（复用平台单桶；仓库口令由托管 secret 派生——
// sha256(secret key)，不新增存储面；容器内数据本身非敏感：restic 镜像内
// /etc 的临时快照）。
//
// 明文纪律：凭据只进容器 env（与上传轨同纪律）；错误摘要先经凭据擦除
// 再进探针结果（RunRestic 的 stderr 摘要可能含端点 URL，不含 env 值）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/statebackup"
)

// probeRepoPathSuffix 是探针仓库在平台单桶内的固定路径（复用桶；仓库
// 只承载探针的临时快照，forget --prune 收口后仅剩仓库 config 头）。
const probeRepoPathSuffix = "fleetly-probe"

// probeStepTimeout 是探针单步容器的自身预算（镜像已钉版预拉常态下秒级；
// 首次含拉取——调用方 ctx 若更短以调用方为准）。
const probeStepTimeout = 3 * time.Minute

// probeRepoPassword 由托管 secret key 派生（sha256 hex——确定性、不落任何
// 存储；探针仓库数据非敏感，口令的意义是 restic 的形态约束与隔离）。
func probeRepoPassword(c credentials) string {
	sum := sha256.Sum256([]byte(c.SecretKey))
	return hex.EncodeToString(sum[:])
}

// probeEnv 组装 restic 容器 env（托管凭据 + 派生口令 + repo 地址）。
func probeEnv(c credentials, repo string) map[string]string {
	return map[string]string{
		"RESTIC_REPOSITORY":     repo,
		"RESTIC_PASSWORD":       probeRepoPassword(c),
		"AWS_ACCESS_KEY_ID":     c.AccessKey,
		"AWS_SECRET_ACCESS_KEY": c.SecretKey,
	}
}

// probeRepoURL 返回容器视角的 repo 地址（容器内 DNS 解析服务 alias——
// state.RustfsEndpointURL 的规范形态，不经宿主拨号）。
func probeRepoURL(bucketPathSuffix string) string {
	return "s3:" + state.RustfsEndpointURL + "/" + state.RustfsBucketName + "/" + bucketPathSuffix
}

// probeSpec 构造单步 restic 容器载荷（attach 内部网络；只读挂载面缺省
// ——探针步骤不消费宿主目录；HostMountDir 留空即无 bind）。
func (m *Manager) probeSpec(ctx context.Context, c credentials, args ...string) statebackup.ResticSpec {
	return statebackup.ResticSpec{
		Image: statebackup.DefaultResticImage,
		Args:  append([]string{"-o", "s3.bucket-lookup=path"}, args...),
		Env:   probeEnv(c, probeRepoURL(probeRepoPathSuffix)),
		Networks: []string{
			state.RustfsNetworkName,
		},
	}
}

// scrubCredentials 把托管凭据明文从错误文本中擦除（state-model §2.9 兜底）。
func scrubCredentials(s string, c credentials) string {
	for _, secret := range []string{c.AccessKey, c.SecretKey} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "***")
		}
	}
	return s
}

// probeStep 是探针单步结果（诚实契约：失败步可定位）。
type probeStep struct {
	Step     string
	OK       bool
	Duration time.Duration
	Err      string
}

// ProbeResult 是探针结构化结果（api TestS3Connection 的 rustfs 面）。
type ProbeResult struct {
	OK         bool
	FailedStep string
	Steps      []probeStep
}

// RunProbe 执行一轮探针（api TestS3Connection 的 rustfs 消费面；设计 §2.5
// 「探测容器一次性 attach 该网」形态）：
//
//	init     —— 对端点鉴权 + 写仓库头（已存在 = 幂等通过）；
//	backup   —— 写一枚临时快照（--json 取快照 id）；
//	snapshots—— 读回列表并断言该 id 在列（诚实读回）；
//	forget   —— 删除该快照并 prune 收口（删除面）。
//
// OK = 四步全绿。失败步与底层摘要进结果（凭据已擦除）。
func (m *Manager) RunProbe(ctx context.Context) (ProbeResult, error) {
	creds, err := m.loadCredentials(ctx)
	if err != nil {
		return ProbeResult{}, err
	}
	res := ProbeResult{}
	run := func(step string, check func(raw string) error, out *string, args ...string) bool {
		start := time.Now()
		pctx, cancel := context.WithTimeout(ctx, probeStepTimeout)
		raw, err := m.probeRunner.RunRestic(pctx, m.probeSpec(pctx, creds, args...))
		cancel()
		ps := probeStep{Step: step, OK: err == nil, Duration: time.Since(start)}
		if err == nil && check != nil {
			err = check(raw)
		}
		if err != nil {
			ps.OK = false
			ps.Err = scrubCredentials(err.Error(), creds)
			res.FailedStep = step
		}
		if out != nil {
			*out = raw
		}
		res.Steps = append(res.Steps, ps)
		return ps.OK
	}

	// ① init：鉴权 + 建仓库头。幂等豁免：仓库已初始化（W3-F1 真机发现——
	// 第二次探针撞 repository already initialized；auth 正确性由后续 backup
	// 步证明，init 步只保证「仓库存在且可鉴权接触」）。
	if !run("init", nil, nil, "init", "--repository-version", "2") {
		last := res.Steps[len(res.Steps)-1]
		if !initAlreadyInitialized(last.Err) {
			res.OK = false
			return res, nil
		}
		// 已初始化 = 幂等通过（改判本步 OK，保留原摘要供运维归因）。
		res.Steps[len(res.Steps)-1].OK = true
		res.FailedStep = ""
	}
	// ② backup：写一枚临时快照（/etc——restic 镜像内临时系统目录，非敏感）。
	var backupOut string
	if !run("backup", nil, &backupOut, "backup", "/etc", "--json") {
		res.OK = false
		return res, nil
	}
	snapID := parseProbeSnapshotID(backupOut)
	if snapID == "" {
		res.Steps = append(res.Steps, probeStep{Step: "backup", OK: false,
			Err: "backup produced no snapshot id (summary message missing)"})
		res.FailedStep = "backup"
		return res, nil
	}
	// ③ snapshots：读回断言（快照 id 必须在列表——「写了但读不到」不通过）。
	var snapsOut string
	if !run("snapshots", func(raw string) error {
		if !probeSnapshotsContain(raw, snapID) {
			return errors.New("probe snapshot id not listed in readback (write without read-back)")
		}
		return nil
	}, &snapsOut, "snapshots", "--json", snapID) {
		res.OK = false
		return res, nil
	}
	// ④ forget：删除面（删快照 + prune 收口数据）。
	if !run("forget", nil, nil, "forget", "--prune", snapID) {
		res.OK = false
		return res, nil
	}
	res.OK = true
	return res, nil
}

// EnsureBucketViaProbe 以探针容器的 init 步确保平台单桶存在（服务就绪后
// 的一次性收敛步骤；幂等——桶与探针仓库已存在时以已初始化形态通过）。
// 宿主进程不可达 overlay——建桶必须经容器。
func (m *Manager) EnsureBucketViaProbe(ctx context.Context) error {
	creds, err := m.loadCredentials(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, probeStepTimeout)
	defer cancel()
	_, err = m.probeRunner.RunRestic(ctx, m.probeSpec(ctx, creds,
		"init", "--repository-version", "2"))
	if err == nil {
		return nil
	}
	if initAlreadyInitialized(err.Error()) {
		return nil // 仓库已初始化 = 桶已存在（幂等）
	}
	return fmt.Errorf("rustfs: probe bucket ensure failed: %s", scrubCredentials(err.Error(), creds))
}

// initAlreadyInitialized 判定 restic init 的「仓库已初始化」错误形态
//（W3-F1：restic 0.19.1 有两种文案——"config file already exists" 与
// "repository master key and config already initialized"；真机第二次探针
// 撞后者）。认证错误不含这些子串，不会被误豁免。
func initAlreadyInitialized(errText string) bool {
	return strings.Contains(errText, "config file already exists") ||
		strings.Contains(errText, "already initialized")
}

// parseProbeSnapshotID 解析 restic backup --json 的 summary 消息（与
// statebackup 同口径的探针内实现——不 import 其内部符号）。
func parseProbeSnapshotID(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var m struct {
			MessageType string `json:"message_type"`
			SnapshotID  string `json:"snapshot_id"`
		}
		if json.Unmarshal([]byte(line), &m) != nil {
			continue
		}
		if m.MessageType == "summary" && m.SnapshotID != "" {
			return m.SnapshotID
		}
	}
	return ""
}

// probeSnapshotsContain 断言快照 id 在读回列表（前缀形差容忍——与
// statebackup 同口径）。
func probeSnapshotsContain(output, snapID string) bool {
	var snaps []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &snaps) != nil {
		return false
	}
	for _, s := range snaps {
		if s.ID == snapID || strings.HasPrefix(snapID, s.ID) || strings.HasPrefix(s.ID, snapID) {
			return true
		}
	}
	return false
}
