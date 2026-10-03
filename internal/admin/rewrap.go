// Package admin 是 fleetlyd 的离线维护面（cmd/fleetlyd admin 子命令的
// 执行体）：停机窗口内对平台私有存储做一次性维护操作。与 API 面分立：
// 不经 wire 装配/gRPC，只依赖 state 与 material；守护进程运行时禁止执行
// （SQLite 单写者 + KEK 轮换窗口）。
package admin

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/hook"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
)

// RewrapFailure 是一条不可解密文行的报告面（rewrap 拒执行时逐条列出）。
type RewrapFailure struct {
	// Kind 是行类别（"secret" | "hook"）。
	Kind string `json:"kind"`
	// Key 是行标识（secret: <project_id>/<name>；hook: <app_id>）。
	Key string `json:"key"`
	// Err 是解封错误文本（不含明文）。
	Err string `json:"error"`
}

// RewrapReport 是一次 KEK 重封的报告面（人类与 JSON 双形态消费）。
// total = current + rewrapped + failed。
type RewrapReport struct {
	SecretsTotal     int             `json:"secrets_total"`
	SecretsRewrapped int             `json:"secrets_rewrapped"`
	SecretsCurrent   int             `json:"secrets_current"`
	HooksTotal       int             `json:"hooks_total"`
	HooksRewrapped   int             `json:"hooks_rewrapped"`
	HooksCurrent     int             `json:"hooks_current"`
	Failures         []RewrapFailure `json:"failures,omitempty"`
}

// sealClass 是密文行的钥匙分类（rewrap 的幂等判定轴）。
type sealClass int

const (
	classUnopenable sealClass = iota // 装载的任何 key 都解不开
	classCurrent                     // 现役 key 可解（已现行，跳过）
	classRetired                     // 退役 key 才可解（需重封）
)

// RewrapSecrets 用现役 KEK 重封全部密文行：secrets 表全部行（含
// tombstone——undelete 路径依赖密文可解）与 app_hooks 的 webhook secret
// 信封。dryRun=true 只做全量解封验证并报告将重封条数，不落任何库。
//
// 原子性形态 = 单事务全量重封：SQLite 单写者（连接池已收口 1），行规模
// 是团队级小集合；任一行不可解先全量报错拒执行，事务内不存在半途失败
// 面，无需分批续跑。幂等 = 现役 key 可解的行跳过不重写（age 加密非确
// 定，重写无害但徒增 churn，且让重复执行不可比对）。两阶段之间无并发
// 写者（停机窗口），阶段一读到的分类在事务内依然成立。
func RewrapSecrets(ctx context.Context, db *state.DB, cipher *material.Cipher, dryRun bool) (*RewrapReport, error) {
	rep := &RewrapReport{}
	secrets := secret.New(db.Clock())
	hooks := hook.New(db.Clock())
	run := db.Runner()

	// 阶段一：全量解封分类，铸重封写单（不落库）。
	type write struct {
		kind string
		key  string
		run  func(ctx context.Context, tx *sql.Tx) error
	}
	var writes []write

	rows, err := secrets.ListAll(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("admin: list secrets: %w", err)
	}
	for i := range rows {
		row := &rows[i]
		rep.SecretsTotal++
		key := row.ProjectID + "/" + row.Name
		switch classify(cipher, row.Ciphertext) {
		case classCurrent:
			rep.SecretsCurrent++
		case classRetired:
			ct, werr := reseal(cipher, row.Ciphertext)
			if werr != nil {
				rep.Failures = append(rep.Failures, RewrapFailure{Kind: "secret", Key: key, Err: werr.Error()})
				continue
			}
			id := row.ID
			writes = append(writes, write{kind: "secret", key: key, run: func(ctx context.Context, tx *sql.Tx) error {
				return secrets.UpdateCiphertext(ctx, tx, id, ct)
			}})
			rep.SecretsRewrapped++
		default:
			_, oerr := cipher.Open(row.Ciphertext)
			rep.Failures = append(rep.Failures, RewrapFailure{Kind: "secret", Key: key, Err: oerr.Error()})
		}
	}

	hookRows, err := hooks.ListAll(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("admin: list hooks: %w", err)
	}
	for i := range hookRows {
		row := &hookRows[i]
		rep.HooksTotal++
		switch classify(cipher, row.SecretCiphertext) {
		case classCurrent:
			rep.HooksCurrent++
		case classRetired:
			ct, werr := reseal(cipher, row.SecretCiphertext)
			if werr != nil {
				rep.Failures = append(rep.Failures, RewrapFailure{Kind: "hook", Key: row.AppID, Err: werr.Error()})
				continue
			}
			appID := row.AppID
			writes = append(writes, write{kind: "hook", key: row.AppID, run: func(ctx context.Context, tx *sql.Tx) error {
				return hooks.UpdateSecretCiphertext(ctx, tx, appID, ct)
			}})
			rep.HooksRewrapped++
		default:
			_, oerr := cipher.Open(row.SecretCiphertext)
			rep.Failures = append(rep.Failures, RewrapFailure{Kind: "hook", Key: row.AppID, Err: oerr.Error()})
		}
	}

	if len(rep.Failures) > 0 {
		return rep, fmt.Errorf("admin: %d row(s) cannot be opened with any loaded key; nothing written", len(rep.Failures))
	}
	if dryRun {
		return rep, nil
	}

	// 阶段二：单事务全量落库（全有或全无）。
	if err := db.Tx(ctx, func(tx *sql.Tx) error {
		for i := range writes {
			if err := writes[i].run(ctx, tx); err != nil {
				return fmt.Errorf("admin: rewrite %s %s: %w", writes[i].kind, writes[i].key, err)
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return rep, nil
}

// reseal 解封（退役 key 路径）并以现役 key 重封。
func reseal(cipher *material.Cipher, ciphertext []byte) ([]byte, error) {
	plaintext, err := cipher.Open(ciphertext)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}
	resealed, err := cipher.Seal(plaintext)
	if err != nil {
		return nil, fmt.Errorf("seal: %w", err)
	}
	return resealed, nil
}

// classify 判定密文行的钥匙分类（现役可解优先判定——rewrap 幂等跳过的
// 依据；Open 自带现役优先序，退役路径复用同一解封）。
func classify(cipher *material.Cipher, ciphertext []byte) sealClass {
	if cipher.OpensWithActive(ciphertext) {
		return classCurrent
	}
	if _, err := cipher.Open(ciphertext); err == nil {
		return classRetired
	}
	return classUnopenable
}
