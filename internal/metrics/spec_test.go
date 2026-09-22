package metrics

// 期望 spec 的形态表单测（victorialogs spec_test 同型——部署器输入的权威
// 钉定面）：钉版镜像字面（三锚同源：Go 常量 / 台账 / e2e）、绑定面钉死
//（VM 查询面回环 + 采集器 0.0.0.0 采集面——§6 挂账票修订的双向不变量）、
// retention 对齐、manager 约束公式、host 网络任务、global 形态、抓取配置
// 引用（动态 targets 的内容寻址——节点集矩阵）、幂等比对的漂移捕获。

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/swarm"

	"github.com/fleetlyrun/fleetly/internal/state"
)

// TestDefaultImagesArePinnedDigests 镜像钉版纪律：tag@sha256 双锚字面
// （台账 docs/runbooks/image-prepull.md #15/#16/#17 同源——改任一侧须同步；
// cAdvisor 的 repo 勘误注记见 DefaultCAdvisorImage 文档）。
func TestDefaultImagesArePinnedDigests(t *testing.T) {
	if !strings.Contains(DefaultVictoriaMetricsImage, "victoriametrics/victoria-metrics:") ||
		!strings.Contains(DefaultVictoriaMetricsImage, "@sha256:86ca5fdb6d87d56ba047b044039019ba2bd9042b36e35f6ea34e437b6c825cef") {
		t.Fatalf("victoria-metrics image %q is not pinned to the ledger digest", DefaultVictoriaMetricsImage)
	}
	if !strings.Contains(DefaultNodeExporterImage, "prom/node-exporter:") ||
		!strings.Contains(DefaultNodeExporterImage, "@sha256:1b4e4438faca4dd7e001dd445d161a4a2091b0fededa84093b3a8dfeae1f1be0") {
		t.Fatalf("node-exporter image %q is not pinned to the ledger digest", DefaultNodeExporterImage)
	}
	// cAdvisor 官方 repo = gcr.io/cadvisor/cadvisor（Docker Hub google/cadvisor
	// 已 DEPRECATED——repo 勘误钉进测试防回退）。
	if !strings.Contains(DefaultCAdvisorImage, "gcr.io/cadvisor/cadvisor:") ||
		!strings.Contains(DefaultCAdvisorImage, "@sha256:3de2bd5203120b866d74a9b283b2ffb8ec382fbf9dc321814700c6ea6f44ec57") {
		t.Fatalf("cadvisor image %q is not pinned to the official gcr.io repo digest", DefaultCAdvisorImage)
	}
}

// TestBuildVictoriaSpecInvariants 期望 spec 全部执行面（设计 §4.1 形态表
// 逐条；VM 回环 8428（查询面零公网面——本票修订后仍钉死）+ host 网络 + 卷 +
// 抓取配置引用（按节点集内容寻址）+ 128MB）。
func TestBuildVictoriaSpecInvariants(t *testing.T) {
	addrs := []string{"10.217.0.10"}
	spec := buildVictoriaSpec("n_TESTNODEID01", 14, addrs)
	if spec.Name != VictoriaServiceName {
		t.Fatalf("service name = %s, want %s", spec.Name, VictoriaServiceName)
	}
	cs := spec.TaskTemplate.ContainerSpec
	if cs.Image != DefaultVictoriaMetricsImage {
		t.Fatalf("image = %s, want pinned default", cs.Image)
	}
	// 参数四件：数据路径 / retention 对齐 / 回环监听（零公网面）/ 抓取配置。
	wantArgs := []string{
		"-storageDataPath=/vmdata",
		"-retentionPeriod=14d",
		"-httpListenAddr=127.0.0.1:8428",
		"-promscrape.config=/etc/fleetly/vm-scrape.yml",
	}
	if len(cs.Args) != len(wantArgs) {
		t.Fatalf("args = %v, want %v", cs.Args, wantArgs)
	}
	for i := range wantArgs {
		if cs.Args[i] != wantArgs[i] {
			t.Fatalf("args[%d] = %s, want %s", i, cs.Args[i], wantArgs[i])
		}
	}
	// 数据卷挂载。
	if len(cs.Mounts) != 1 || cs.Mounts[0].Source != VolumeName || cs.Mounts[0].Target != "/vmdata" {
		t.Fatalf("mounts = %+v, want single volume %s -> /vmdata", cs.Mounts, VolumeName)
	}
	// 抓取配置引用（内容寻址名〔节点集参数化〕+ 显式 UID/GID/Mode——swarm
	// agent 对空串 strconv 解析失败的同族真机教训，database spec 同注）。
	if len(cs.Configs) != 1 {
		t.Fatalf("configs = %+v, want single scrape config reference", cs.Configs)
	}
	cfgRef := cs.Configs[0]
	if cfgRef.ConfigName != scrapeConfigName(addrs) {
		t.Fatalf("config name = %s, want %s", cfgRef.ConfigName, scrapeConfigName(addrs))
	}
	if cfgRef.File == nil || cfgRef.File.Name != "/etc/fleetly/vm-scrape.yml" ||
		cfgRef.File.UID != "0" || cfgRef.File.GID != "0" || cfgRef.File.Mode != 0o444 {
		t.Fatalf("config file target = %+v, want /etc/fleetly/vm-scrape.yml 0:0:0444", cfgRef.File)
	}
	// host 网络任务（回环监听的承载形态——查询面只在 manager 本地）。
	if len(spec.TaskTemplate.Networks) != 1 || spec.TaskTemplate.Networks[0].Target != "host" {
		t.Fatalf("networks = %+v, want single host attachment", spec.TaskTemplate.Networks)
	}
	// 单副本 + manager 约束。
	if spec.Mode.Replicated == nil || spec.Mode.Replicated.Replicas == nil || *spec.Mode.Replicated.Replicas != 1 {
		t.Fatalf("mode = %+v, want replicated-1", spec.Mode)
	}
	wantConstraint := "node.labels." + state.LabelNodeID + " == n_TESTNODEID01"
	if spec.TaskTemplate.Placement == nil ||
		len(spec.TaskTemplate.Placement.Constraints) != 1 ||
		spec.TaskTemplate.Placement.Constraints[0] != wantConstraint {
		t.Fatalf("constraints = %+v, want [%s]", spec.TaskTemplate.Placement, wantConstraint)
	}
	// 内存限额 128MB + 无端口发布面。
	if spec.TaskTemplate.Resources == nil || spec.TaskTemplate.Resources.Limits == nil ||
		spec.TaskTemplate.Resources.Limits.MemoryBytes != int64(128)<<20 {
		t.Fatalf("resources = %+v, want 128MiB limit", spec.TaskTemplate.Resources)
	}
	if spec.EndpointSpec != nil {
		t.Fatalf("endpoint spec = %+v, want nil (host network task binds loopback itself)", spec.EndpointSpec)
	}
}

// TestBuildCollectorSpecsInvariants global 采集器两件：host 网络、
// **0.0.0.0 绑定**（§6 挂账票修订——VM 经节点 advertise 地址直连抓取；
// 公网拦截由节点防火墙负责——诚实口径三处同锚之一，这里钉死参数字面）、
// 宿主只读挂载（官方容器形态）、global 形态、无约束、限额。
func TestBuildCollectorSpecsInvariants(t *testing.T) {
	cad := buildCAdvisorSpec()
	if cad.Name != CAdvisorServiceName {
		t.Fatalf("cadvisor name = %s", cad.Name)
	}
	if cad.Mode.Global == nil {
		t.Fatal("cadvisor mode must be global (one task per node)")
	}
	if cad.TaskTemplate.Placement != nil && len(cad.TaskTemplate.Placement.Constraints) != 0 {
		t.Fatal("cadvisor must not carry placement constraints (global covers every node)")
	}
	ccs := cad.TaskTemplate.ContainerSpec
	if ccs.Image != DefaultCAdvisorImage {
		t.Fatalf("cadvisor image = %s", ccs.Image)
	}
	// 启动形态（2026-09-22 staging 补丁）：/bin/sh -c 单串自适选拿 containerd
	// socket——宿主两种形态（dockerd 自管 / 系统 containerd）启动期择一，
	// 关键不变量=两路径都出现在选择式里 + 0.0.0.0 绑定（跨节点抓取面）+
	// -docker_only + moby 命名空间（sh 形态下不做逐元素比对——壳层字符串
	// 整体钉死）。
	if len(ccs.Command) != 2 || ccs.Command[0] != "/bin/sh" || ccs.Command[1] != "-c" {
		t.Fatalf("cadvisor command = %v, want [/bin/sh -c]", ccs.Command)
	}
	if len(ccs.Args) != 1 {
		t.Fatalf("cadvisor args = %v, want single shell payload", ccs.Args)
	}
	sh := ccs.Args[0]
	for _, frag := range []string{
		"exec /usr/bin/cadvisor -logtostderr",
		"-listen_ip=0.0.0.0",
		"-port=8080",
		"-docker_only",
		`[ -S /var/run/docker/containerd/containerd.sock ]`,
		"/var/run/containerd/containerd.sock",
		"-containerd-namespace=moby",
	} {
		if !strings.Contains(sh, frag) {
			t.Fatalf("cadvisor shell payload missing %q: %s", frag, sh)
		}
	}
	// VM 与采集器的绑定面分野钉死：采集器 0.0.0.0，VM -httpListenAddr
	// 恒回环（两条断言互为反照——绑定面漂移双向可检）。
	if strings.Contains(sh, hostIP) {
		t.Fatalf("cadvisor payload must not bind the VM loopback address %s: %s", hostIP, sh)
	}
	vmArgs := buildVictoriaSpec("n_TESTNODEID01", 14, nil).TaskTemplate.ContainerSpec.Args
	for _, a := range vmArgs {
		if strings.HasPrefix(a, "-httpListenAddr=") && a != "-httpListenAddr=127.0.0.1:8428" {
			t.Fatalf("VM listen arg %s drifted from loopback pin", a)
		}
	}
	wantCadMounts := [][2]string{
		{"/", "/rootfs"}, {"/var/run", "/var/run"}, {"/sys", "/sys"},
		{"/var/lib/docker", "/var/lib/docker"},
	}
	assertMounts(t, ccs.Mounts, wantCadMounts)
	// 健康检查：显式 IPv4 回环探针（镜像缺省 localhost 探针在双栈解析下
	// 恒败 → swarm 杀任务循环——见 spec 内注记）。
	if ccs.Healthcheck == nil || len(ccs.Healthcheck.Test) != 2 ||
		!strings.Contains(ccs.Healthcheck.Test[1], "http://127.0.0.1:8080/healthz") {
		t.Fatalf("cadvisor healthcheck = %+v, want explicit 127.0.0.1:8080/healthz probe", ccs.Healthcheck)
	}
	if len(cad.TaskTemplate.Networks) != 1 || cad.TaskTemplate.Networks[0].Target != "host" {
		t.Fatalf("cadvisor networks = %+v, want host", cad.TaskTemplate.Networks)
	}
	if cad.TaskTemplate.Resources == nil || cad.TaskTemplate.Resources.Limits == nil ||
		cad.TaskTemplate.Resources.Limits.MemoryBytes != int64(192)<<20 {
		t.Fatalf("cadvisor resources = %+v, want 192MiB limit", cad.TaskTemplate.Resources)
	}

	ne := buildNodeExporterSpec()
	if ne.Mode.Global == nil {
		t.Fatal("node-exporter mode must be global")
	}
	ncs := ne.TaskTemplate.ContainerSpec
	wantNeArgs := []string{
		"--path.rootfs=/host", "--path.procfs=/host/proc", "--path.sysfs=/host/sys",
		"--web.listen-address=0.0.0.0:9100",
	}
	if len(ncs.Args) != len(wantNeArgs) {
		t.Fatalf("node-exporter args = %v, want %v", ncs.Args, wantNeArgs)
	}
	for i := range wantNeArgs {
		if ncs.Args[i] != wantNeArgs[i] {
			t.Fatalf("node-exporter args[%d] = %s, want %s", i, ncs.Args[i], wantNeArgs[i])
		}
	}
	wantNeMounts := [][2]string{
		{"/", "/host"}, {"/proc", "/host/proc"}, {"/sys", "/host/sys"},
	}
	assertMounts(t, ncs.Mounts, wantNeMounts)
	if len(ne.TaskTemplate.Networks) != 1 || ne.TaskTemplate.Networks[0].Target != "host" {
		t.Fatalf("node-exporter networks = %+v, want host", ne.TaskTemplate.Networks)
	}
	if ne.TaskTemplate.Resources == nil || ne.TaskTemplate.Resources.Limits == nil ||
		ne.TaskTemplate.Resources.Limits.MemoryBytes != int64(64)<<20 {
		t.Fatalf("node-exporter resources = %+v, want 64MiB limit", ne.TaskTemplate.Resources)
	}
}

// assertMounts 校验挂载集的 source→target 形态与只读位（顺序敏感——期望
// 序权威）。
func assertMounts(t *testing.T, mounts []mount.Mount, want [][2]string) {
	t.Helper()
	if len(mounts) != len(want) {
		t.Fatalf("mounts = %+v, want %v", mounts, want)
	}
	for i, m := range mounts {
		if m.Source != want[i][0] || m.Target != want[i][1] {
			t.Fatalf("mounts[%d] = %s->%s, want %s->%s", i, m.Source, m.Target, want[i][0], want[i][1])
		}
		if !m.ReadOnly {
			t.Fatalf("mounts[%d] = %s->%s must be read-only", i, m.Source, m.Target)
		}
		if m.Type != mount.TypeBind {
			t.Fatalf("mounts[%d] = %s->%s must be a bind mount", i, m.Source, m.Target)
		}
	}
}

// TestRetentionArg 保留天数 → VM 参数（缺省回落 14d；自定义天数直译）。
func TestRetentionArg(t *testing.T) {
	for _, tc := range []struct {
		days int
		want string
	}{
		{days: 0, want: "-retentionPeriod=14d"},
		{days: -3, want: "-retentionPeriod=14d"},
		{days: 14, want: "-retentionPeriod=14d"},
		{days: 7, want: "-retentionPeriod=7d"},
	} {
		if got := retentionArg(tc.days); got != tc.want {
			t.Errorf("retentionArg(%d) = %s, want %s", tc.days, got, tc.want)
		}
	}
}

// TestScrapeConfigDeterministic 抓取配置渲染确定性 + 内容寻址名稳定 +
// 动态 targets 形态（§6 挂账票——节点集参数化；空/单/多矩阵 + 规范化
// 序 + 集合变化即配置内容 sha 变化〔更新链触发的前提〕）。
func TestScrapeConfigDeterministic(t *testing.T) {
	single := []string{"10.217.0.10"}
	a := scrapeConfigYAML(single)
	b := scrapeConfigYAML(single)
	if a != b {
		t.Fatal("scrape config render is not deterministic")
	}
	for _, want := range []string{
		`- job_name: fleetly-cadvisor`,
		`- targets: ["10.217.0.10:8080"]`,
		`- job_name: fleetly-node-exporter`,
		`- targets: ["10.217.0.10:9100"]`,
	} {
		if !strings.Contains(a, want) {
			t.Fatalf("scrape config missing %q:\n%s", want, a)
		}
	}
	if !strings.HasPrefix(scrapeConfigName(single), scrapeConfigPrefix) {
		t.Fatalf("config name %q lacks prefix %q", scrapeConfigName(single), scrapeConfigPrefix)
	}
	// 内容寻址：名 = 前缀 + 配置内容 sha256 前 8 hex（幂等 ensure 的根基
	// ——独立重算哈希比对，不做同表达式自比）。
	sum := sha256.Sum256([]byte(scrapeConfigYAML(single)))
	want := scrapeConfigPrefix + hex.EncodeToString(sum[:4])
	if scrapeConfigName(single) != want {
		t.Fatalf("config name = %q, want content-addressed %q", scrapeConfigName(single), want)
	}

	// 矩阵——空集：渲染空 targets 列表（合法 YAML；converge 对空集先行
	// 短路报错，本形态是渲染完备性面）。
	empty := scrapeConfigYAML(nil)
	if !strings.Contains(empty, `- targets: []`) {
		t.Fatalf("empty node set must render empty target lists:\n%s", empty)
	}

	// 矩阵——多节点：乱序 + 重复输入经 scrapeAddrs 规范化后与手排集合同一
	// 渲染（排序去重——同节点集恒同名，底座清单顺序不进哈希；converge
	// 消费的正是规范化后的清单）。
	multi := []string{"10.217.0.11", "10.217.0.10", "10.217.0.11"}
	sorted := []string{"10.217.0.10", "10.217.0.11"}
	if scrapeConfigYAML(scrapeAddrs(multi)) != scrapeConfigYAML(sorted) {
		t.Fatal("input order must not affect the rendered config")
	}
	if scrapeConfigName(scrapeAddrs(multi)) != scrapeConfigName(sorted) {
		t.Fatal("input order must not affect the content-addressed name")
	}
	multiYAML := scrapeConfigYAML(sorted)
	for _, want := range []string{
		`"10.217.0.10:8080", "10.217.0.11:8080"`,
		`"10.217.0.10:9100", "10.217.0.11:9100"`,
	} {
		if !strings.Contains(multiYAML, want) {
			t.Fatalf("multi-node config missing %q:\n%s", want, multiYAML)
		}
	}

	// 集合变化 → 内容 sha 变 → 对象名变化（节点增减两向——动态再生链的
	// 触发前提；不变化则服务引用比对无从捕获）。
	if scrapeConfigName(single) == scrapeConfigName(sorted) {
		t.Fatal("distinct node sets must yield distinct config names")
	}
	if scrapeConfigName(append(append([]string{}, sorted...), "10.217.0.12")) == scrapeConfigName(sorted) {
		t.Fatal("adding a node must change the config name")
	}
}

// TestScrapeAddrsNormalization 地址集规范化的单元矩阵：排序、去重、空串
// 剔除（无地址节点如实跳过后的兜底面）。
func TestScrapeAddrsNormalization(t *testing.T) {
	got := scrapeAddrs([]string{"10.0.0.3", "", "10.0.0.1", "10.0.0.3", "10.0.0.2"})
	want := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	if !slices.Equal(got, want) {
		t.Fatalf("scrapeAddrs = %v, want %v", got, want)
	}
	if len(scrapeAddrs(nil)) != 0 {
		t.Fatal("scrapeAddrs(nil) must be empty")
	}
}

// TestSpecEqualDriftMatrix 幂等比对：全等通过；任一执行面漂移捕获
// （镜像/参数〔含 VM 回环与采集器 0.0.0.0 绑定面〕/挂载/抓取配置引用/
// 网络/约束/副本/global 形态/限额）。
func TestSpecEqualDriftMatrix(t *testing.T) {
	addrs := []string{"10.217.0.10"}
	spec := buildVictoriaSpec("n_TESTNODEID01", 14, addrs)
	cur := stateOf(spec)
	if !specEqual(cur, spec) {
		t.Fatal("specEqual(cur, desired) = false on identical spec, want true")
	}
	// mutate 从干净 spec 重建（ServiceSpec 内含指针，浅拷贝会共享可变面
	// ——每次重渲染避免污染基准）。
	mutate := func(f func(d *swarm.ServiceSpec)) {
		d := buildVictoriaSpec("n_TESTNODEID01", 14, addrs)
		f(&d)
		if specEqual(stateOf(spec), d) {
			t.Fatalf("specEqual did not detect drift: %+v", d)
		}
	}
	mutate(func(d *swarm.ServiceSpec) {
		d.TaskTemplate.ContainerSpec.Image = "victoriametrics/victoria-metrics:latest"
	})
	mutate(func(d *swarm.ServiceSpec) {
		// VM 回环监听被改掉 = 查询面零公网面漂移，必须捕获。
		d.TaskTemplate.ContainerSpec.Args[2] = "-httpListenAddr=0.0.0.0:8428"
	})
	mutate(func(d *swarm.ServiceSpec) { d.TaskTemplate.ContainerSpec.Args[1] = "-retentionPeriod=30d" })
	mutate(func(d *swarm.ServiceSpec) { d.TaskTemplate.ContainerSpec.Args[3] = "-promscrape.config=/other.yml" })
	mutate(func(d *swarm.ServiceSpec) { d.TaskTemplate.ContainerSpec.Mounts[0].Source = "other-volume" })
	mutate(func(d *swarm.ServiceSpec) {
		// 抓取配置引用指向别的节点集版本（内容寻址换名）= 动态 targets
		// 漂移，必须捕获——节点集变化驱动服务更新的比对锚。
		other := append(append([]string{}, addrs...), "10.217.0.11")
		d.TaskTemplate.ContainerSpec.Configs[0].ConfigName = scrapeConfigName(other)
	})
	mutate(func(d *swarm.ServiceSpec) { d.TaskTemplate.Networks = nil })
	mutate(func(d *swarm.ServiceSpec) { d.TaskTemplate.Placement.Constraints = []string{"node.role==worker"} })
	one := uint64(2)
	mutate(func(d *swarm.ServiceSpec) { d.Mode.Replicated.Replicas = &one })
	mem := int64(256) << 20
	mutate(func(d *swarm.ServiceSpec) { d.TaskTemplate.Resources.Limits.MemoryBytes = mem })
	// 健康检查（仅 cAdvisor 有）的漂移捕获：探针文本变更与整字段缺失。
	cad := buildCAdvisorSpec()
	cadState := stateOf(cad)
	if !specEqual(cadState, cad) {
		t.Fatal("cadvisor specEqual = false on identical spec")
	}
	dCad := buildCAdvisorSpec()
	dCad.TaskTemplate.ContainerSpec.Healthcheck.Test[1] = "wget --spider http://127.0.0.1:9999/healthz || exit 1"
	if specEqual(cadState, dCad) {
		t.Fatal("specEqual did not detect healthcheck probe drift")
	}
	dNoHealth := buildCAdvisorSpec()
	dNoHealth.TaskTemplate.ContainerSpec.Healthcheck = nil
	if specEqual(cadState, dNoHealth) {
		t.Fatal("specEqual did not detect missing healthcheck drift")
	}

	// global 形态比对：cAdvisor 的 global → replicated 漂移必被捕获。
	dGlobal := buildCAdvisorSpec()
	dGlobal.Mode = swarm.ServiceMode{Replicated: &swarm.ReplicatedService{}}
	oneRep := uint64(1)
	dGlobal.Mode.Replicated.Replicas = &oneRep
	if specEqual(cadState, dGlobal) {
		t.Fatal("specEqual did not detect global→replicated drift")
	}
}
