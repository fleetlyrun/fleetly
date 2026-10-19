package swarm

// 孤儿 Secret 载体清扫单测（收尾批 E29-1；fakeDaemon 的 Secret 路由面）。

import (
	"context"
	"testing"
	"time"

	"github.com/moby/moby/api/types/swarm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// orphanSeedSvc 是引用 fleetly-sec-a-<fp8> 的受管服务（现役锚）。
func orphanSeedSvc(name, secretRef string) swarm.ServiceSpec {
	spec := swarm.ServiceSpec{Annotations: swarm.Annotations{Name: name, Labels: map[string]string{
		labelManaged: "true",
	}}}
	spec.TaskTemplate.ContainerSpec = &swarm.ContainerSpec{Image: "busybox:1.37"}
	if secretRef != "" {
		spec.TaskTemplate.ContainerSpec.Secrets = []*swarm.SecretReference{{
			SecretID: "sec-" + secretRef, SecretName: secretRef,
		}}
	}
	return spec
}

func seedOrphan(f *fakeDaemon, name string, age time.Time, labels map[string]string) {
	sec := swarm.Secret{Spec: swarm.SecretSpec{Annotations: swarm.Annotations{Name: name, Labels: labels}}}
	sec.ID = "sec-" + name
	sec.CreatedAt = age
	f.addSecret(sec)
}

// TestSweepOrphanSecrets 钉 E29-1：孤儿判定=标签+前缀+非现役+宽限窗四重；
// 现役/宽限窗内/非受管载体不删；删除有界。
func TestSweepOrphanSecrets(t *testing.T) {
	ctx := context.Background()
	old := time.Now().Add(-2 * orphanSecretGrace)

	t.Run("reaps unreferenced old carriers and keeps active ones", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		f.addService(orphanSeedSvc("web", "fleetly-sec-a-feedbeef"))
		seedOrphan(f, "fleetly-sec-a-feedbeef", old, map[string]string{labelManaged: "true"}) // 现役
		seedOrphan(f, "fleetly-sec-a-deadcode", old, map[string]string{labelManaged: "true"}) // 孤儿（旧指纹残留）
		seedOrphan(f, "fleetly-sec-a-00ba1100d", old, map[string]string{})                    // 非受管：不判孤儿
		n, err := p.SweepOrphanSecrets(ctx, 100)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "only the unreferenced managed carrier is an orphan")
		assert.Equal(t, 2, f.secretCount(), "active and unlabeled carriers must survive")
	})

	t.Run("respects the creation grace window", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		seedOrphan(f, "fleetly-sec-zot-htpasswd-fresh", time.Now().Add(-time.Minute), map[string]string{labelManaged: "true"})
		n, err := p.SweepOrphanSecrets(ctx, 100)
		require.NoError(t, err)
		assert.Zero(t, n, "carriers inside the grace window must not be reaped (create-to-reference race)")
		assert.Equal(t, 1, f.secretCount())
	})

	t.Run("skips carriers without a birth timestamp", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		seedOrphan(f, "fleetly-sec-x-12345678", time.Time{}, map[string]string{labelManaged: "true"})
		n, err := p.SweepOrphanSecrets(ctx, 100)
		require.NoError(t, err)
		assert.Zero(t, n, "no birth fact means no orphan verdict (conservative)")
	})

	t.Run("caps deletions per call", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		for _, n := range []string{"fleetly-sec-a-11111111", "fleetly-sec-a-22222222", "fleetly-sec-a-33333333"} {
			seedOrphan(f, n, old, map[string]string{labelManaged: "true"})
		}
		n, err := p.SweepOrphanSecrets(ctx, 2)
		require.NoError(t, err)
		assert.Equal(t, 2, n, "delete budget caps one call (janitor tick rate limiting)")
		assert.Equal(t, 1, f.secretCount(), "remaining orphan waits for the next tick")
	})
}

// TestSweepOrphanPeerGrantsNoop：peer grant 面在 swarm 是诚实 no-op（互放行
// 是网络附件语义，无独立 grant 载体对象——ADR-0055 决策 3 的 swarm 对应面）。
func TestSweepOrphanPeerGrantsNoop(t *testing.T) {
	f := newFakeDaemon()
	p := &Provider{cli: f.newClient(t)}
	deleted, err := p.SweepOrphanPeerGrants(context.Background(), 10)
	require.NoError(t, err)
	assert.Zero(t, deleted)
}

// orphanSeedSvcConfigs 是引用 config 载体的受管服务（现役锚：config 引用面
// 是 ContainerSpec.Configs，与 Secret 引用面分立）。
func orphanSeedSvcConfigs(name string, configRefs ...string) swarm.ServiceSpec {
	spec := orphanSeedSvc(name, "")
	for _, ref := range configRefs {
		spec.TaskTemplate.ContainerSpec.Configs = append(spec.TaskTemplate.ContainerSpec.Configs,
			&swarm.ConfigReference{ConfigID: "cfg-" + ref, ConfigName: ref})
	}
	return spec
}

func seedOrphanConfig(f *fakeDaemon, name string, age time.Time, labels map[string]string) {
	cfg := swarm.Config{Spec: swarm.ConfigSpec{Annotations: swarm.Annotations{Name: name, Labels: labels}}}
	cfg.ID = "cfg-" + name
	cfg.CreatedAt = age
	f.addConfig(cfg)
}

// TestSweepOrphanConfigCarriers 钉 R06-M7：config 载体（内容寻址版本化名与
// Secret 同公式）必须进清扫面——每次 Config 值变更净增一枚、旧版本永不回收
// 是旧缺陷。判据与 Secret 同款三重防误删：managed 标签 + 载体前缀 + 非现役
// 引用集（ContainerSpec.Configs）+ 出生宽限窗；预算单源限流。
func TestSweepOrphanConfigCarriers(t *testing.T) {
	ctx := context.Background()
	oldSecret := time.Now().Add(-2 * orphanSecretGrace)
	oldConfig := time.Now().Add(-2 * configCarrierGrace)

	t.Run("reaps unreferenced old config carriers and keeps active ones", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		f.addService(orphanSeedSvcConfigs("web", "fleetly-sec-app-conf-feedbeef"))
		seedOrphanConfig(f, "fleetly-sec-app-conf-feedbeef", oldConfig, map[string]string{labelManaged: "true"}) // 现役
		seedOrphanConfig(f, "fleetly-sec-app-conf-deadcode", oldConfig, map[string]string{labelManaged: "true"}) // 孤儿（旧内容指纹）
		seedOrphanConfig(f, "fleetly-sec-app-conf-00ba1100", oldConfig, map[string]string{})                     // 非受管：不判孤儿
		seedOrphanConfig(f, "manual-config", oldConfig, map[string]string{labelManaged: "true"})                 // 无载体前缀：不判孤儿
		n, err := p.SweepOrphanSecrets(ctx, 100)
		require.NoError(t, err)
		assert.Equal(t, 1, n, "only the unreferenced managed config carrier is an orphan")
		assert.Equal(t, 3, f.configCount(), "active, unlabeled and non-prefixed configs must survive")
	})

	t.Run("respects the creation grace window", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		// Secret 宽限（1h）已过但 Config 宽限（7d）未过：一律不动。
		seedOrphanConfig(f, "fleetly-sec-app-conf-fresh", time.Now().Add(-2*orphanSecretGrace), map[string]string{labelManaged: "true"})
		n, err := p.SweepOrphanSecrets(ctx, 100)
		require.NoError(t, err)
		assert.Zero(t, n, "config carriers inside the 7d grace window must not be reaped")
		assert.Equal(t, 1, f.configCount())
	})

	t.Run("shares one delete budget across secret and config carriers", func(t *testing.T) {
		f := newFakeDaemon()
		p := &Provider{cli: f.newClient(t)}
		seedOrphan(f, "fleetly-sec-a-11111111", oldSecret, map[string]string{labelManaged: "true"})
		seedOrphanConfig(f, "fleetly-sec-b-22222222", oldConfig, map[string]string{labelManaged: "true"})
		seedOrphanConfig(f, "fleetly-sec-c-33333333", oldConfig, map[string]string{labelManaged: "true"})
		n, err := p.SweepOrphanSecrets(ctx, 2)
		require.NoError(t, err)
		assert.Equal(t, 2, n, "both carrier kinds draw from the single sweep budget")
		assert.Equal(t, 0, f.secretCount())
		assert.Equal(t, 1, f.configCount(), "the deterministic (name, kind) order leaves the last config for the next tick")
	})
}
