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
