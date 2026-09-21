package substrate

// Swarm secret 原语（E4 managed-databases §2.7，D-DB-7，W4-S4）：引擎
// SecretEnsurer 端口的底座实现。ensure 幂等语义与 internal/database 的
// ensureCredentialSecret 同型（inspect → 缺失才 create——secret 名内嵌值
// 指纹，「存在性」判据即幂等）；服务 spec 翻译侧补齐 SecretReference 的
// SecretID 与完整 File UID/GID/Mode（W3 真机教训：留空会让 swarm agent
// 在任务启动期 strconv 解析空串直接失败——database 包同注）。
//
// 明文纪律：data 只进创建载荷，绝不进日志/错误文本（rustfs/database 同族
// 纪律的延续）。

import (
	"context"
	"fmt"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/swarm"
	mobyclient "github.com/moby/moby/client"
)

// EnsureSecret 实现 engine.SecretEnsurer：确认 Swarm secret 在位并返回其
// 对象 ID；缺失时以 data 创建（labels 为归属标注——app secret 的清场/
// 识别选择器锚）。data 只进创建载荷。
func (c *Client) EnsureSecret(ctx context.Context, name string, data []byte, labels map[string]string) (string, error) {
	ictx, icancel := withCallTimeout(ctx) // D2：非流式 per-call 超时（逐调用）
	res, err := c.cli.SecretInspect(ictx, name, mobyclient.SecretInspectOptions{})
	icancel()
	if err == nil {
		return res.Secret.ID, nil
	}
	if !errdefs.IsNotFound(err) {
		return "", fmt.Errorf("substrate: secret inspect %s: %w", name, err)
	}
	spec := swarm.SecretSpec{
		Annotations: swarm.Annotations{Name: name, Labels: labels},
		Data:        data,
	}
	cctx, ccancel := withCallTimeout(ctx)
	created, err := c.cli.SecretCreate(cctx, mobyclient.SecretCreateOptions{Spec: spec})
	ccancel()
	if err != nil {
		// 并发创建竞态：已存在即成功（幂等判据 = 名字在位）。
		if errdefs.IsConflict(err) || errdefs.IsAlreadyExists(err) {
			rctx, rcancel := withCallTimeout(ctx)
			re, rerr := c.cli.SecretInspect(rctx, name, mobyclient.SecretInspectOptions{})
			rcancel()
			if rerr == nil {
				return re.Secret.ID, nil
			}
		}
		return "", fmt.Errorf("substrate: secret create %s: %w", name, err)
	}
	return created.ID, nil
}
