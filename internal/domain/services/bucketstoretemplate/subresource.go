// Bucket 子资源管理方言面（B8，ADR-0096 决策 10 / design §4.7）：bucket
// store 实例内托管 Bucket 的 admin 工具容器命令渲染 + 实况解析。silo 镜像
// 自带 mcli（mc fork——e2e 既有工具面，零额外镜像）；工具容器 = 引擎自身
// 镜像，root AK/SK 经 MC_HOST_<alias> env 注入（mcli 原生 env 面——alias
// 解析不走落盘配置，工具容器无状态）。
//
// 全部 live 定案（2026-11-07 本机 docker 实测 pgsty/silo
// RELEASE.2026-10-26T00-00-00Z）：
//   - 存在性探测 = `mcli quota info local/<bucket>`（缺 bucket 报
//     "The specified bucket does not exist"——缺席判据；在场则回读
//     "has hard quota of 954 MiB"/"has  quota of 0 B"〔无配额形〕）。
//   - 创建 = `mcli mb local/<bucket>`（重复创建报 already exists——ensure
//     段先探后建不触发）。
//   - 配额 = `mcli quota set local/<bucket> --size <size>`（hard quota，
//     mcli 尺寸后缀 k/m/g/t/i/b——字节整数直传即合法）；**silo 对象扫描器
//     逐拍检查配额**：达限即拒后续写（quota set 回显文案如实披露）；
//     撤配额 = `mcli quota clear`（回 "Successfully cleared bucket quota"）。
//   - 删除 = `mcli rb --force local/<bucket>`（连带对象数据——wide 档确认
//     的执行面）。
//   - 数据量披露 = `mcli du local/<bucket> --json`（JSON 行
//     {"prefix":..,"size":N,"objects":N,"status":"success"}——删除预览
//     的将删数据量行）。
package bucketstoretemplate

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
)

// AdminSpec 是一条 admin 工具容器命令（pubsubtemplate.AdminSpec 同形——
// BackupSpec 再导出别名）。
type AdminSpec = dataservicetemplate.BackupSpec

// BucketLive 是 Bucket 实况的解析面。
type BucketLive struct {
	Exists bool
	// QuotaBytes 是实况 hard quota（nil = 无配额）。
	QuotaBytes *int64
	// Bytes 是桶内对象字节量（du 探测；存在性探测不填）。
	Bytes int64
	// Objects 是对象数（du 探测；存在性探测不填）。
	Objects int64
}

// mcliAlias 是工具容器内的 mcli alias 名（MC_HOST_<alias> env 键的尾段）。
const mcliAlias = "fleetlylocal"

// mcliEnv 铸 mcli 连接 env（MC_HOST_<alias>=http://<ak>:<sk>@<host>:<port>
// ——mcli 原生 alias env 面，无落盘配置；G101 纪律：键名字面量非凭证拼接，
// SK 值由调用方注入）。
func mcliEnv(host string, port int32, accessKey, secretKey string) map[string]string {
	return map[string]string{
		"MC_HOST_" + mcliAlias: "http://" + accessKey + ":" + secretKey + "@" + host + ":" + strconv.FormatInt(int64(port), 10),
	}
}

// mcliTarget 拼 alias/bucket 目标串。
func mcliTarget(bucket string) string { return mcliAlias + "/" + bucket }

// siloMcliPort 是 silo 的 S3 API 口（Meta().Port 同源——9000；独立常量注释
// 锚定，改动须与模板面同批）。
const siloMcliPort = 9000

// BucketAdminFace 是 Bucket 的 admin 方言面（siloTemplate 实现；凭证 =
// root AK/SK，经 env 注入）。
type BucketAdminFace interface {
	// InspectBucket 渲染存在性+配额探测（quota info——缺席判据的报文模式
	// 在 ParseBucketInspect 收口）。
	InspectBucket(host, accessKey, secretKey, bucket string) (AdminSpec, error)
	// ParseBucketInspect 解析探测产物（退出错 + "does not exist" → 缺席；
	// 退出 0 → 配额文案解析）。
	ParseBucketInspect(stdout, stderr string, runErr error) (BucketLive, error)
	// CreateBucket 渲染创建（`mcli mb`）。
	CreateBucket(host, accessKey, secretKey, bucket string) (AdminSpec, error)
	// SetBucketQuota 渲染配额 ensure（`mcli quota set --size <bytes>`；
	// quotaBytes nil = 撤配额——`quota clear`）。
	SetBucketQuota(host, accessKey, secretKey, bucket string, quotaBytes *int64) (AdminSpec, error)
	// InspectBucketUsage 渲染数据量探测（`mcli du --json`——删除披露行）。
	InspectBucketUsage(host, accessKey, secretKey, bucket string) (AdminSpec, error)
	// ParseBucketUsage 解析 du JSON 行（{"size":N,"objects"?..}）。
	ParseBucketUsage(stdout, stderr string, runErr error) (BucketLive, error)
	// DeleteBucket 渲染删除（`mcli rb --force`——连带对象）。
	DeleteBucket(host, accessKey, secretKey, bucket string) (AdminSpec, error)
}

// BucketAdminFaceOf 把绑版 Template 收窄为 Bucket admin 面。
func BucketAdminFaceOf(tpl Template) BucketAdminFace {
	face, ok := tpl.(BucketAdminFace)
	if !ok {
		return nil
	}
	return face
}

// ValidateBucketName 钳制桶名（S3 值域保守子集：小写字母数字与连字符/点，
// 3–63 字符；admin argv 的注入面）。
func ValidateBucketName(bucket string) error {
	if len(bucket) < 3 || len(bucket) > 63 {
		return fmt.Errorf("bucketstoretemplate: bucket name %q must be 3-63 characters", bucket)
	}
	for i := 0; i < len(bucket); i++ {
		c := bucket[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' || c == '.':
		default:
			return fmt.Errorf("bucketstoretemplate: bucket name %q carries characters outside the [a-z0-9.-] set (S3 bucket naming)", bucket)
		}
	}
	return nil
}

// InspectBucket：`mcli quota info local/<b>`（存在性 + 配额一体探测——
// quota info 对缺桶报错、对在桶无配额回 "quota of 0 B"，单命令双信息）。
func (siloTemplate) InspectBucket(host, accessKey, secretKey, bucket string) (AdminSpec, error) {
	if err := ValidateBucketName(bucket); err != nil {
		return AdminSpec{}, err
	}
	return AdminSpec{
		Argv: []string{"mcli", "quota", "info", mcliTarget(bucket)},
		Env:  mcliEnv(host, siloMcliPort, accessKey, secretKey),
	}, nil
}

// ParseBucketInspect：缺席判据 = "does not exist"；配额文案 =
// "Bucket `<b>` has hard quota of 954 MiB" / "has  quota of 0 B"（live
// 实测两形态——hard 在场配额 / 无配额）。尺寸后缀 k/m/g/t/i/b 解析。
func (siloTemplate) ParseBucketInspect(stdout, stderr string, runErr error) (BucketLive, error) {
	combined := stdout + stderr
	if runErr != nil || strings.Contains(combined, "<ERROR>") {
		if strings.Contains(combined, "does not exist") {
			return BucketLive{Exists: false}, nil
		}
		if runErr != nil {
			return BucketLive{}, fmt.Errorf("bucket quota info failed: %w: %s", runErr, firstLine(combined))
		}
		return BucketLive{}, fmt.Errorf("bucket quota info failed: %s", firstLine(combined))
	}
	// 无配额形态（“has  quota of 0 B”/任意 0 B）→ Exists、nil quota。
	if !strings.Contains(combined, "hard quota") {
		return BucketLive{Exists: true}, nil
	}
	quota, ok := parseMcliQuotaLine(combined)
	if !ok {
		return BucketLive{}, fmt.Errorf("bucket quota info output is not parseable: %s", firstLine(combined))
	}
	return BucketLive{Exists: true, QuotaBytes: &quota}, nil
}

// parseMcliQuotaLine 解析 "has hard quota of <num><unit>"（unit ∈ B/KiB/
// MiB/GiB/TiB/k/m/g/t——IEC 1024 进制与米制并存，mcli 文案两形都出）。
func parseMcliQuotaLine(s string) (int64, bool) {
	idx := strings.Index(s, "hard quota of ")
	if idx < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(s[idx+len("hard quota of "):])
	var num strings.Builder
	unit := ""
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if (c >= '0' && c <= '9') || c == '.' {
			num.WriteByte(c)
			continue
		}
		unit = strings.TrimSpace(rest[i:])
		break
	}
	value, err := strconv.ParseFloat(num.String(), 64)
	if err != nil {
		return 0, false
	}
	var mult float64
	switch strings.ToUpper(strings.TrimSuffix(unit, "B")) {
	case "", "B":
		mult = 1
	case "K", "KI":
		mult = 1024
	case "M", "MI":
		mult = 1024 * 1024
	case "G", "GI":
		mult = 1024 * 1024 * 1024
	case "T", "TI":
		mult = 1024 * 1024 * 1024 * 1024
	default:
		return 0, false
	}
	return int64(value * mult), true
}

// firstLine 取报文首行（错误面的取证截断）。
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// CreateBucket：`mcli mb local/<b>`。
func (siloTemplate) CreateBucket(host, accessKey, secretKey, bucket string) (AdminSpec, error) {
	if err := ValidateBucketName(bucket); err != nil {
		return AdminSpec{}, err
	}
	return AdminSpec{
		Argv: []string{"mcli", "mb", mcliTarget(bucket)},
		Env:  mcliEnv(host, siloMcliPort, accessKey, secretKey),
	}, nil
}

// SetBucketQuota：`mcli quota set local/<b> --size <bytes>`（字节整数直传
// ——mcli 尺寸面无后缀即字节；nil = `quota clear` 撤配额）。
func (siloTemplate) SetBucketQuota(host, accessKey, secretKey, bucket string, quotaBytes *int64) (AdminSpec, error) {
	if err := ValidateBucketName(bucket); err != nil {
		return AdminSpec{}, err
	}
	if quotaBytes == nil {
		return AdminSpec{
			Argv: []string{"mcli", "quota", "clear", mcliTarget(bucket)},
			Env:  mcliEnv(host, siloMcliPort, accessKey, secretKey),
		}, nil
	}
	return AdminSpec{
		Argv: []string{"mcli", "quota", "set", mcliTarget(bucket), "--size", strconv.FormatInt(*quotaBytes, 10)},
		Env:  mcliEnv(host, siloMcliPort, accessKey, secretKey),
	}, nil
}

// InspectBucketUsage：`mcli du local/<b> --json`（删除披露的数据量行）。
func (siloTemplate) InspectBucketUsage(host, accessKey, secretKey, bucket string) (AdminSpec, error) {
	if err := ValidateBucketName(bucket); err != nil {
		return AdminSpec{}, err
	}
	return AdminSpec{
		Argv: []string{"mcli", "du", mcliTarget(bucket), "--json"},
		Env:  mcliEnv(host, siloMcliPort, accessKey, secretKey),
	}, nil
}

// ParseBucketUsage：du 的 JSON 行 {"size":N,...,"objects"?..}；缺桶归
// Exists=false（披露面容错——调用方对缺席桶披露「桶已不在」）。
func (siloTemplate) ParseBucketUsage(stdout, stderr string, runErr error) (BucketLive, error) {
	combined := stdout + stderr
	if runErr != nil || strings.Contains(combined, "<ERROR>") {
		if strings.Contains(combined, "does not exist") || strings.Contains(combined, "Object does not exist") {
			return BucketLive{Exists: false}, nil
		}
		if runErr != nil {
			return BucketLive{}, fmt.Errorf("bucket usage probe failed: %w: %s", runErr, firstLine(combined))
		}
		return BucketLive{}, fmt.Errorf("bucket usage probe failed: %s", firstLine(combined))
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var usage struct {
			Status  string `json:"status"`
			Size    int64  `json:"size"`
			Objects int64  `json:"objects"`
		}
		if err := json.Unmarshal([]byte(line), &usage); err != nil {
			continue // 非 JSON 行（进度/表头）跳过
		}
		if usage.Status != "" && usage.Status != "success" {
			continue
		}
		return BucketLive{Exists: true, Bytes: usage.Size, Objects: usage.Objects}, nil
	}
	return BucketLive{}, fmt.Errorf("bucket usage probe produced no JSON line")
}

// DeleteBucket：`mcli rb --force local/<b>`（--force 连带对象——数据删除
// 的诚实执行面）。
func (siloTemplate) DeleteBucket(host, accessKey, secretKey, bucket string) (AdminSpec, error) {
	if err := ValidateBucketName(bucket); err != nil {
		return AdminSpec{}, err
	}
	return AdminSpec{
		Argv: []string{"mcli", "rb", "--force", mcliTarget(bucket)},
		Env:  mcliEnv(host, siloMcliPort, accessKey, secretKey),
	}, nil
}
