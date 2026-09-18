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
}

// redactorTTL 是脱敏值集刷新周期（env set → 脱敏生效的最迟延迟）。
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

// build 解出该 app 全部平台 env 明文构建 redactor；box 缺失（无 env 面）
// 返回空集；行级解密失败跳过该行（保守：其余值仍脱）。第二返回值 = 是否
// 完整构建成功（false = 调用方沿用旧值集）。
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
		v := string(plain)
		if len(v) >= minRedactLen {
			vals = append(vals, v)
		}
	}
	sort.Slice(vals, func(i, j int) bool { return len(vals[i]) > len(vals[j]) })
	return &redactor{values: vals}, true
}
