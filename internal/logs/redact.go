package logs

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// 脱敏（T2.20 验收：日志中 secret 值脱敏，负面断言）：脱敏源 = 该 app 的
// env 明文集（state 密文经 secrets.Box 解出）——**只脱已知值**；未知内容
// 原样通过（负面测试断言 env 值不出现，不做启发式）。
//
// 取舍：短值（< minRedactLen 字节）不参与脱敏（如 "1"、"abc" 这类值会把
// 日志整体打花，反而破坏排障价值）；脱敏粒度 = 子串全量替换，命中数不外泄。

// minRedactLen 是参与脱敏的最小 env 值长度（字节）。
const minRedactLen = 8

// redactor 持有一个 app 的脱敏值集。
type redactor struct {
	values []string // 降序排列（长值优先替换，防短值截断长值前缀后漏脱）
}

func (r *redactor) redact(line string) string {
	for _, v := range r.values {
		line = strings.ReplaceAll(line, v, "***")
	}
	return line
}

// redactorEntry 是缓存项（值集 + 构建时刻）。
type redactorEntry struct {
	r       *redactor
	builtAt time.Time
	ok      bool
}

// redactorRegistry 按应用缓存 redactor（TTL 刷新——env 变更最迟一个 TTL
// 生效；解密故障保守沿用旧值集而非放弃脱敏）。
type redactorRegistry struct {
	mu  sync.Mutex
	st  *state.Store
	box secretBox
	m   map[string]redactorEntry
	log *slog.Logger
	now func() time.Time
	// extra 是 B3 值集扩面的补充供给（state 之外的明文 secret 落点，如
	// gitserver 的 post-receive 钩子 token——明文只在钩子文件，无解密面）；
	// nil = 无扩面（测试/未装配形态）。
	extra SecretValuesSource
}

// redactorTTL 是脱敏值集刷新周期——未挂钩写路径的兜底生效延迟（H9：env
// 写点与部署 env 提升点已即时 invalidate 联动，TTL 只兜未挂钩的间接变更
// 路径，如 app 删除连带 env 清理等；最迟一个 TTL 收敛）。
const redactorTTL = 30 * time.Second

// secretBox 是脱敏所需的解密端口（实现 = secrets.Box；测试可注入）。
type secretBox interface {
	Decrypt(ciphertext []byte) ([]byte, error)
}

func newRedactorRegistry(st *state.Store, box secretBox, log *slog.Logger) *redactorRegistry {
	return &redactorRegistry{st: st, box: box, m: make(map[string]redactorEntry), log: log, now: time.Now}
}

// forApp 返回该 app 的 redactor（缓存命中且未过期直接用；过期/首次构建）。
func (r *redactorRegistry) forApp(ctx context.Context, appID string) *redactor {
	r.mu.Lock()
	cur, ok := r.m[appID]
	r.mu.Unlock()
	if ok && r.now().Sub(cur.builtAt) < redactorTTL {
		return cur.r
	}
	fresh, okBuild := r.build(ctx, appID)
	r.mu.Lock()
	ent := redactorEntry{builtAt: r.now()}
	if okBuild {
		ent.r = fresh
		ent.ok = true
	} else if ok {
		// 构建失败：保守沿用旧值集（脱敏不回退），只续期一半。
		ent = cur
		ent.builtAt = r.now().Add(-redactorTTL / 2)
	} else {
		ent.r = &redactor{} // 从未成功：空集兜底（不脱敏但不阻塞采集）
	}
	r.m[appID] = ent
	r.mu.Unlock()
	return ent.r
}

// invalidate 删除该 app 的缓存项（H9）：env 写路径与部署 env 提升点即时
// 联动——30s TTL 窗内旧值集仍生效会让新 secret 值被明文采集并按天落盘
// 保留 7 天，写点失效把暴露窗收敛到下一次 forApp 重建。未挂钩的写路径
// 仍由 TTL 兜底。
func (r *redactorRegistry) invalidate(appID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, appID)
}

// build 解出该 app 全部已知 secret 明文构建 redactor；box 缺失（无 env 面）
// 返回空集；行级解密失败跳过该行（保守：其余值仍脱）。第二返回值 = 是否
// 完整构建成功（false = 调用方沿用旧值集）。B3 值集扩面：除平台 env 外，
// 并入该 app 的 webhook secret、拉源 https_token（state 密文列 + box 解密）
// 与 extra 供给的钩子 token——同一 ≥minRedactLen 规则。
func (r *redactorRegistry) build(ctx context.Context, appID string) (*redactor, bool) {
	if r.box == nil {
		return &redactor{}, true
	}
	rows, err := r.st.ListAppEnv(ctx, appID)
	if err != nil {
		r.log.Warn("logs: list env for redaction failed", "app_id", appID, "error", err.Error())
		return nil, false
	}
	var vals []string
	for _, row := range rows {
		plain, err := r.box.Decrypt([]byte(row.Value))
		if err != nil {
			r.log.Warn("logs: decrypt env for redaction failed (skipped)", "app_id", appID, "key", row.Key)
			continue
		}
		if v := string(plain); len(v) >= minRedactLen {
			vals = append(vals, v)
		}
	}
	// B3：git 触发面 secret（webhook 验签密钥 + 拉源 https_token）——
	// GetAppGitConfig 密文列经同一 box 解密；读取/解密失败跳过（观测面
	// 降级不阻断采集）。
	if cfg, err := r.st.GetAppGitConfig(ctx, appID); err == nil {
		if cfg.SecretSet && cfg.WebhookSecret != "" {
			if plain, derr := r.box.Decrypt([]byte(cfg.WebhookSecret)); derr == nil {
				if v := string(plain); len(v) >= minRedactLen {
					vals = append(vals, v)
				}
			} else {
				r.log.Warn("logs: decrypt webhook secret for redaction failed (skipped)", "app_id", appID)
			}
		}
		if cfg.AuthKind == state.SourceAuthToken && cfg.AuthSecret != "" {
			if plain, derr := r.box.Decrypt([]byte(cfg.AuthSecret)); derr == nil {
				if v := string(plain); len(v) >= minRedactLen {
					vals = append(vals, v)
				}
			} else {
				r.log.Warn("logs: decrypt source token for redaction failed (skipped)", "app_id", appID)
			}
		}
	} else {
		r.log.Warn("logs: read git config for redaction failed (skipped)", "app_id", appID, "error", err.Error())
	}
	// B3：extra 供给（钩子 token 等 state 之外的明文落点）。
	if r.extra != nil {
		for _, v := range r.extra.SecretValues(ctx, appID) {
			if len(v) >= minRedactLen {
				vals = append(vals, v)
			}
		}
	}
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	return &redactor{values: vals}, true
}
