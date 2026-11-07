package pubtemplate

// pubtemplate 门禁（B6a 补 B4b 缺课：digest 钉定 + 模板面 + kafka live 定案
// 钉板——dbtemplate_test.go 的 TestImageDigestsPinned/TestTemplateFaces 同构；
// buckettemplate_test.go 单版最小形态先例）。kafka 断言值全部锚 2026-11-07
// 本机 docker live 实测形态（kafkadialect.go 包注实录）。

import (
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
)

// TestImageDigestsPinned 钉全矩阵 digest 形态（sha256:<64 hex>；Image() 与
// ImageDigest() 恒一致；真值对账走 scripts/digests.sh，ADR-0094 决策 6）。
func TestImageDigestsPinned(t *testing.T) {
	for _, engine := range Engines() {
		specs, ok := VersionsFor(engine)
		if !ok {
			t.Fatalf("engine %q listed but carries no versions", engine)
		}
		for _, spec := range specs {
			tpl, err := For(engine, spec.ID)
			if err != nil {
				t.Fatalf("For(%s, %s): %v", engine, spec.ID, err)
			}
			img := tpl.Image()
			if !strings.Contains(img, "@sha256:") {
				t.Fatalf("%s %s image %q must be tag@sha256 pinned", engine, spec.ID, img)
			}
			d := tpl.ImageDigest()
			if !strings.HasPrefix(d, "sha256:") || len(d) != len("sha256:")+64 {
				t.Fatalf("%s %s digest %q must be sha256:<64 hex>", engine, spec.ID, d)
			}
			if !strings.HasSuffix(img, "@"+d) {
				t.Fatalf("%s %s Image() digest segment must equal ImageDigest(): %q vs %q", engine, spec.ID, img, d)
			}
		}
	}
}

// TestTemplateFaces 钉族级模板面不变量：每引擎恰一 Default、声明序旧→新、
// 探针非空、knob 白名单与 owned 键闭集交集为空（平台 owned 键不可设——
// design §3.1）。
func TestTemplateFaces(t *testing.T) {
	for _, engine := range Engines() {
		tpl, err := For(engine, "")
		if err != nil {
			t.Fatalf("For(%s): %v", engine, err)
		}
		specs := tpl.Versions()
		defaults := 0
		for _, s := range specs {
			if s.Default {
				defaults++
			}
		}
		if defaults != 1 {
			t.Fatalf("engine %q carries %d default versions, want exactly 1", engine, defaults)
		}
		if len(specs) >= 2 && specs[len(specs)-1].ID <= specs[len(specs)-2].ID {
			t.Fatalf("engine %q versions must be declared oldest -> newest, got %v", engine, VersionIDs(specs))
		}
		if len(tpl.Probe()) == 0 {
			t.Fatalf("engine %q carries no probe", engine)
		}
		knobs := map[string]bool{}
		for _, k := range tpl.Knobs() {
			knobs[k.Key] = true
		}
		for _, owned := range tpl.OwnedKeys() {
			if knobs[owned] {
				t.Fatalf("engine %q: platform-owned key %q must not be a knob (settings whitelist is the closure)", engine, owned)
			}
		}
		if tpl.Dialect() == "" {
			t.Fatalf("engine %q carries no dialect", engine)
		}
	}
}

// TestKafkaVersionMatrix 钉 kafka 在册矩阵（4.x 两条、4.4 Default——design
// §2.5 建议值随上游 4.x 线演进的 B6a 实值；3.x 不入册）。
func TestKafkaVersionMatrix(t *testing.T) {
	tpl, err := For("kafka", "")
	if err != nil {
		t.Fatalf("For(kafka): %v", err)
	}
	if got := VersionIDs(tpl.Versions()); strings.Join(got, ",") != "4.3,4.4" {
		t.Fatalf("kafka versions = %v, want [4.3 4.4]", got)
	}
	if tpl.Meta().Version != "4.4" || tpl.Meta().Port != 9092 {
		t.Fatalf("kafka default meta = %+v, want version 4.4 port 9092", tpl.Meta())
	}
}

// TestKafkaFaces 钉 kafka 模板面（live 定案形态）：数据目录/探针分面/
// Workload 最小 env 集/能力位/方言。
func TestKafkaFaces(t *testing.T) {
	tpl, err := For("kafka", "")
	if err != nil {
		t.Fatalf("For(kafka): %v", err)
	}
	if tpl.DataTarget() != "/var/lib/kafka/data" {
		t.Fatalf("kafka data target = %q", tpl.DataTarget())
	}
	// 探针 = 内部明文 listener 专供探针（三选一 live 定案；绝对路径——
	// /opt/kafka/bin 不在镜像 PATH）。
	probe := tpl.Probe()
	if strings.Join(probe, " ") != "/opt/kafka/bin/kafka-broker-api-versions.sh --bootstrap-server localhost:9094" {
		t.Fatalf("kafka probe = %v", probe)
	}
	if tpl.DataCapabilities().Backup {
		t.Fatalf("kafka v1 carries no data backup face (deferred)")
	}
	if tpl.Dialect() != "kafka" {
		t.Fatalf("kafka dialect = %q, want kafka", tpl.Dialect())
	}
	env, command, err := tpl.Workload("pub-01hx", 0, nil)
	if err != nil {
		t.Fatalf("workload: %v", err)
	}
	// live 定案最小 env 集（缺一即起服失败的关键键）。
	for key, want := range map[string]string{
		"KAFKA_PROCESS_ROLES":                        "broker,controller",
		"KAFKA_CONTROLLER_QUORUM_VOTERS":             "1@localhost:9093",
		"KAFKA_CONTROLLER_LISTENER_NAMES":            "CONTROLLER",
		"KAFKA_LISTENERS":                            "BROKER://:9092,CONTROLLER://:9093,PROBE://127.0.0.1:9094",
		"KAFKA_ADVERTISED_LISTENERS":                 "BROKER://pub-01hx:9092,PROBE://127.0.0.1:9094",
		"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP":       "CONTROLLER:PLAINTEXT,BROKER:SASL_PLAINTEXT,PROBE:PLAINTEXT",
		"KAFKA_INTER_BROKER_LISTENER_NAME":           "BROKER",
		"KAFKA_SASL_ENABLED_MECHANISMS":              "SCRAM-SHA-256",
		"KAFKA_SASL_MECHANISM_INTER_BROKER_PROTOCOL": "SCRAM-SHA-256",
		"KAFKA_OPTS":                                 "-Djava.security.auth.login.config=/run/secrets/" + kafkaMaterialJAAS,
		"KAFKA_LOG_DIRS":                             "/var/lib/kafka/data",
		"CLUSTER_ID":                                 kafkaClusterID,
	} {
		if env[key] != want {
			t.Fatalf("kafka workload env %s = %q, want %q", key, env[key], want)
		}
	}
	// 命令面 = SCRAM 引导链（镜像 CMD 覆盖）。
	if len(command) != 3 || command[0] != "/bin/bash" || command[1] != "-c" {
		t.Fatalf("kafka command = %v, want bash -c bootstrap chain", command)
	}
	for _, anchor := range []string{
		"kafka-storage.sh format --ignore-formatted",
		"--add-scram \"SCRAM-SHA-256=[name=fleetly,password=$(cat /run/secrets/" + kafkaMaterialScram + ")]\"",
		"exec /etc/kafka/docker/run",
	} {
		if !strings.Contains(command[2], anchor) {
			t.Fatalf("kafka bootstrap chain must contain %q, got:\n%s", anchor, command[2])
		}
	}
}

// TestKafkaHeapLane 钉 heap 联动三分支（红队 F-13 立法）：heap_mb 设 →
// KAFKA_HEAP_OPTS 显式；仅 memory 设 → 渲染期默认 heap=memory×0.5；皆无 →
// 不渲染（镜像缺省 1G）。
func TestKafkaHeapLane(t *testing.T) {
	tpl, err := For("kafka", "")
	if err != nil {
		t.Fatalf("For(kafka): %v", err)
	}
	env, _, err := tpl.Workload("pub-01hx", 0, map[string]string{"heap_mb": "512mb"})
	if err != nil {
		t.Fatalf("workload: %v", err)
	}
	if env["KAFKA_HEAP_OPTS"] != "-Xms512m -Xmx512m" {
		t.Fatalf("explicit heap lane = %q", env["KAFKA_HEAP_OPTS"])
	}
	env, _, err = tpl.Workload("pub-01hx", 2048, nil)
	if err != nil {
		t.Fatalf("workload: %v", err)
	}
	if env["KAFKA_HEAP_OPTS"] != "-Xms1024m -Xmx1024m" {
		t.Fatalf("memory-derived heap lane = %q, want memory/2", env["KAFKA_HEAP_OPTS"])
	}
	env, _, err = tpl.Workload("pub-01hx", 0, nil)
	if err != nil {
		t.Fatalf("workload: %v", err)
	}
	if _, ok := env["KAFKA_HEAP_OPTS"]; ok {
		t.Fatalf("no heap and no memory must leave KAFKA_HEAP_OPTS unset (image default), got %q", env["KAFKA_HEAP_OPTS"])
	}
	// heap 显式时 memory 分支不叠加（显式优先）。
	env, _, _ = tpl.Workload("pub-01hx", 2048, map[string]string{"heap_mb": "512mb"})
	if env["KAFKA_HEAP_OPTS"] != "-Xms512m -Xmx512m" {
		t.Fatalf("explicit heap wins over memory-derived default, got %q", env["KAFKA_HEAP_OPTS"])
	}
	// static env 旋钮车道：num.partitions → KAFKA_NUM_PARTITIONS。
	env, _, _ = tpl.Workload("pub-01hx", 0, map[string]string{"num.partitions": "6"})
	if env["KAFKA_NUM_PARTITIONS"] != "6" {
		t.Fatalf("num.partitions lane = %q", env["KAFKA_NUM_PARTITIONS"])
	}
	// 喂入律：dynamic 键永不进 Workload env（TestDynamicKeysAbsentFromProjection
	// 的包内半边——dynamic 直达 Workload 即静默丢，纵深闸拒收坏白名单）。
	env, _, err = tpl.Workload("pub-01hx", 0, map[string]string{"log.retention.ms": "7d"})
	if err != nil {
		t.Fatalf("workload with dynamic key: %v", err)
	}
	for k := range env {
		if k == "KAFKA_LOG_RETENTION_MS" || strings.Contains(k, "RETENTION") {
			t.Fatalf("dynamic key leaked into workload env: %s=%s", k, env[k])
		}
	}
}

// TestKafkaLinkage 钉跨面联动受理闸（F-13 三分支受理半边：两者皆设 →
// heap ≤ memory×0.7；仅 memory/仅 heap → 放行）。
func TestKafkaLinkage(t *testing.T) {
	bound, err := For("kafka", "")
	if err != nil {
		t.Fatalf("For(kafka): %v", err)
	}
	tpl, ok := bound.(LinkageValidator)
	if !ok {
		t.Fatalf("kafka must implement the linkage face (heap_mb x memory)")
	}
	if err := tpl.ValidateLinkage(1024, map[string]string{"heap_mb": "1024mb"}); err == nil {
		t.Fatalf("heap 1024mb against a 1024MiB memory limit must be refused (0.7 ceiling = 716mb)")
	}
	if err := tpl.ValidateLinkage(1024, map[string]string{"heap_mb": "716mb"}); err != nil {
		t.Fatalf("heap 716mb against a 1024MiB memory limit is at the 0.7 ceiling and must pass: %v", err)
	}
	if err := tpl.ValidateLinkage(0, map[string]string{"heap_mb": "32gb"}); err != nil {
		t.Fatalf("heap without a memory limit is the operator's call: %v", err)
	}
	if err := tpl.ValidateLinkage(512, nil); err != nil {
		t.Fatalf("memory without an explicit heap renders memory/2 at render time, admission passes: %v", err)
	}
	// nats 无联动面（可选面不实现——受理位 type-assert 自然放行）。
	nats, err := For("nats", "")
	if err != nil {
		t.Fatalf("For(nats): %v", err)
	}
	if _, ok := nats.(LinkageValidator); ok {
		t.Fatalf("nats must not implement the linkage face (no cross-face knobs)")
	}
}

// TestKafkaConnMaterialRoundTrip 钉写读同源律（双方言）：kafka k=v 串铸/回
// 读等值、nats URL 形经同一 ParseConnMaterial 回读、坏形态精确失败。
func TestKafkaConnMaterialRoundTrip(t *testing.T) {
	tpl, err := For("kafka", "")
	if err != nil {
		t.Fatalf("For(kafka): %v", err)
	}
	mat := tpl.ConnMaterial("pub-01hx", "scram0123456789abcdef")
	if mat.Bootstrap != "pub-01hx:9092" || mat.Mechanism != "SCRAM-SHA-256" || mat.User != "fleetly" || mat.Password != "scram0123456789abcdef" {
		t.Fatalf("kafka conn material drifted: %+v", mat)
	}
	rendered := RenderConnMaterial(mat)
	if rendered != "bootstrap=pub-01hx:9092;mechanism=SCRAM-SHA-256;user=fleetly;password=scram0123456789abcdef" {
		t.Fatalf("kafka dialect string = %q", rendered)
	}
	back, err := ParseConnMaterial(rendered)
	if err != nil {
		t.Fatalf("parse %q: %v", rendered, err)
	}
	if back != mat {
		t.Fatalf("round trip drifted: %+v vs %+v", back, mat)
	}
	if pw, err := PasswordFromURL(rendered); err != nil || pw != "scram0123456789abcdef" {
		t.Fatalf("PasswordFromURL on the kafka dialect = %q, %v", pw, err)
	}
	// nats URL 形同一回读面。
	nats, _ := For("nats", "")
	nmat := nats.ConnMaterial("pub-01hy", "secretpw123")
	nparsed, err := ParseConnMaterial(RenderConnMaterial(nmat))
	if err != nil {
		t.Fatalf("parse nats url form: %v", err)
	}
	if nparsed.Password != "secretpw123" || nparsed.User != "fleetly" || nparsed.URL == "" || nparsed.Bootstrap != "" {
		t.Fatalf("nats round trip drifted: %+v", nparsed)
	}
	for _, bad := range []string{
		"",
		"bootstrap=pub:9092",
		"bootstrap=pub:9092;mechanism=SCRAM-SHA-256;user=fleetly",
		"bootstrap=pub:9092;mechanism=SCRAM-SHA-256;user=fleetly;password=",
		"bootstrap=pub:9092;mechanism=SCRAM-SHA-256;user=fleetly;password=p;extra=1",
		"bootstrap=pub:9092;bootstrap=pub:9093;mechanism=SCRAM-SHA-256;user=fleetly;password=p",
	} {
		if _, err := ParseConnMaterial(bad); err == nil {
			t.Fatalf("parse(%q) must fail", bad)
		}
	}
}

// TestKafkaDynamicFace 钉 dynamic 热应用面（live 定案载体：kafka-configs.sh
// --alter dynamic broker config）：argv 形状、受理口径值 → kafka 原生字面
// 折算（duration→ms / size→bytes）、dynamic 白名单外键拒。
func TestKafkaDynamicFace(t *testing.T) {
	tpl, err := For("kafka", "")
	if err != nil {
		t.Fatalf("For(kafka): %v", err)
	}
	spec, err := tpl.ApplyDynamicSettings("pub-01hx", "scram0123456789abcdef", map[string]string{
		"log.retention.ms":  "90d",
		"message.max.bytes": "2mb",
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	argv := strings.Join(spec.Argv, " ")
	want := "/opt/kafka/bin/kafka-configs.sh --bootstrap-server pub-01hx:9092 --command-config /run/secrets/" +
		kafkaClientPropsFile + " --alter --entity-type brokers --entity-name 1 --add-config log.retention.ms=7776000000,message.max.bytes=2097152"
	if argv != want {
		t.Fatalf("apply argv =\n%s\nwant\n%s", argv, want)
	}
	if _, ok := spec.SecretFiles[kafkaClientPropsFile]; !ok {
		t.Fatalf("apply must carry the SASL client properties file")
	}
	// static 键到达热应用面 = fail-closed（ValidateDynamicSettings 哨兵）。
	if _, err := tpl.ApplyDynamicSettings("pub-01hx", "scram0123456789abcdef", map[string]string{"heap_mb": "512mb"}); err == nil {
		t.Fatalf("static knob reaching the hot-apply face must fail closed")
	}
	// nats 无 dynamic 面（哨兵）。
	nats, _ := For("nats", "")
	if _, err := nats.ApplyDynamicSettings("pub-01hy", "secretpw123", map[string]string{"any": "1"}); err != enginetemplate.ErrNoDynamicFace {
		t.Fatalf("nats carries no dynamic face, got %v", err)
	}
}

// TestKafkaRotateFace 钉轮换面（live 定案：数据面 SCRAM user alter——以
// current 认证、next 入 add-config；Secret 重写在执行链后段）。
func TestKafkaRotateFace(t *testing.T) {
	tpl, err := For("kafka", "")
	if err != nil {
		t.Fatalf("For(kafka): %v", err)
	}
	spec, err := tpl.RotatePassword("pub-01hx", "current0123456789", "next0123456789ab")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	argv := strings.Join(spec.Argv, " ")
	want := "/opt/kafka/bin/kafka-configs.sh --bootstrap-server pub-01hx:9092 --command-config /run/secrets/" +
		kafkaClientPropsFile + " --alter --entity-type users --entity-name fleetly --add-config SCRAM-SHA-256=[password=next0123456789ab]"
	if argv != want {
		t.Fatalf("rotate argv =\n%s\nwant\n%s", argv, want)
	}
	if !strings.Contains(string(spec.SecretFiles[kafkaClientPropsFile]), "current0123456789") {
		t.Fatalf("rotate must authenticate with the current credential")
	}
	// 声明式对照：nats RotateSpec Argv 空（conf 材料轮换）。
	nats, _ := For("nats", "")
	nspec, err := nats.RotatePassword("pub-01hy", "current0123456789", "next0123456789ab")
	if err != nil || len(nspec.Argv) != 0 {
		t.Fatalf("nats rotation is declarative, got %v (err %v)", nspec.Argv, err)
	}
}

// TestKafkaBackupDeferred 钉备份/恢复挂账哨兵（TriggerBackup 受理位映射
// E_INVALID_ARGUMENT；create 无 restore 源受理）。
func TestKafkaBackupDeferred(t *testing.T) {
	tpl, err := For("kafka", "")
	if err != nil {
		t.Fatalf("For(kafka): %v", err)
	}
	if _, err := tpl.Backup("pub-01hx", "scram0123456789abcdef"); err == nil || !strings.Contains(err.Error(), "no data backup face") {
		t.Fatalf("kafka backup must refuse with the deferred sentinel, got %v", err)
	}
	if _, err := tpl.Restore("pub-01hx", "scram0123456789abcdef"); err == nil || !strings.Contains(err.Error(), "no data backup face") {
		t.Fatalf("kafka restore must refuse with the deferred sentinel, got %v", err)
	}
	if _, err := tpl.Backup("pub-01hx", "bad;password"); err == nil {
		t.Fatalf("render gate must fire before the sentinel check on hostile passwords")
	}
}

// TestKafkaKnobSchema 钉旋钮四只（design §3.4）：class 车道、dynamic 两键
// 带 Default（dynamicResets 移除复位的数据源）、值域校验面。
func TestKafkaKnobSchema(t *testing.T) {
	knobs := kafkaTemplate{}.Knobs()
	if len(knobs) != 4 {
		t.Fatalf("kafka carries exactly four knobs (design §3.4), got %d", len(knobs))
	}
	byKey := map[string]enginetemplate.Knob{}
	for _, k := range knobs {
		byKey[k.Key] = k
	}
	if byKey["log.retention.ms"].Class != enginetemplate.KnobDynamic || byKey["log.retention.ms"].Default == "" {
		t.Fatalf("log.retention.ms must be dynamic with a registered default (reset face)")
	}
	if byKey["message.max.bytes"].Class != enginetemplate.KnobDynamic || byKey["message.max.bytes"].Default == "" {
		t.Fatalf("message.max.bytes must be dynamic with a registered default (reset face)")
	}
	if byKey["num.partitions"].Class != enginetemplate.KnobStatic {
		t.Fatalf("num.partitions must be static (server.properties lane)")
	}
	if byKey["heap_mb"].Class != enginetemplate.KnobStatic {
		t.Fatalf("heap_mb must be static (KAFKA_HEAP_OPTS env lane)")
	}
	if err := enginetemplate.ValidateSettings(knobs, map[string]string{
		"log.retention.ms": "7d", "message.max.bytes": "1gb", "num.partitions": "8", "heap_mb": "512mb",
	}); err != nil {
		t.Fatalf("full legal knob set must validate: %v", err)
	}
	for key, bad := range map[string]string{
		"log.retention.ms": "7sec", "message.max.bytes": "512q", "num.partitions": "0", "heap_mb": "64mb",
	} {
		if err := enginetemplate.ValidateSettings(knobs, map[string]string{key: bad}); err == nil {
			t.Fatalf("knob %s=%s must be refused", key, bad)
		}
	}
}

// TestEngineBacksUp 钉 per-engine 能力位判据（B6a 拆分：kafka false 挂账/
// nats true；离册引擎保守 false）。
func TestEngineBacksUp(t *testing.T) {
	if EngineBacksUp("kafka") {
		t.Fatalf("kafka carries no backup face (deferred)")
	}
	if !EngineBacksUp("nats") {
		t.Fatalf("nats carries the per-stream backup face")
	}
	if EngineBacksUp("nosuchengine") {
		t.Fatalf("engines outside the registry must be conservatively false")
	}
}

// TestCatalogEngineLevelCapabilities 钉目录按引擎级真值（kafka
// backup:false/migrate:false 诚实推论 + dialect 轴；nats backup:true）。
func TestCatalogEngineLevelCapabilities(t *testing.T) {
	catalog := Catalog()
	found := map[string]CatalogEntry{}
	for _, e := range catalog {
		found[e.Engine] = e
	}
	if len(found) != 2 {
		t.Fatalf("catalog carries nats+kafka, got %v", Engines())
	}
	if found["kafka"].Capabilities.Backup || found["kafka"].Capabilities.Migrate {
		t.Fatalf("kafka catalog must list backup:false and migrate:false (honest inference), got %+v", found["kafka"].Capabilities)
	}
	if !found["nats"].Capabilities.Backup {
		t.Fatalf("nats catalog must list backup:true")
	}
	if found["kafka"].Dialect != "kafka" || found["nats"].Dialect != "nats" {
		t.Fatalf("dialect axis drifted: %+v", found)
	}
}

// TestKafkaDurationToMs 钉 duration→ms 折算核（单位闭集 ms|s|m|h|d）。
func TestKafkaDurationToMs(t *testing.T) {
	for v, want := range map[string]int64{
		"15000ms": 15000, "30s": 30000, "5m": 300000, "168h": 604800000, "90d": 7776000000,
	} {
		got, err := KafkaDurationToMs(v)
		if err != nil || got != want {
			t.Fatalf("KafkaDurationToMs(%q) = %d, %v; want %d", v, got, err, want)
		}
	}
	for _, bad := range []string{"7sec", "MS", "", "30", "1y"} {
		if _, err := KafkaDurationToMs(bad); err == nil {
			t.Fatalf("KafkaDurationToMs(%q) must fail", bad)
		}
	}
}

// TestKafkaMaterials 钉材料面：JAAS（KafkaServer 上下文 + 双引号凭据）+
// scram 裸密码（format 引导面）；空 settings 产物恒定；坏密码拒。
func TestKafkaMaterials(t *testing.T) {
	tpl, _ := For("kafka", "")
	files, err := tpl.Materials("scram0123456789abcdef", nil)
	if err != nil {
		t.Fatalf("materials: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("kafka materials = jaas + scram password file, got %v", files)
	}
	jaas := string(files[kafkaMaterialJAAS])
	for _, anchor := range []string{"KafkaServer {", "ScramLoginModule required", `username="fleetly"`, `password="scram0123456789abcdef"`} {
		if !strings.Contains(jaas, anchor) {
			t.Fatalf("jaas material missing %q:\n%s", anchor, jaas)
		}
	}
	if string(files[kafkaMaterialScram]) != "scram0123456789abcdef" {
		t.Fatalf("scram bootstrap material = raw password, got %q", files[kafkaMaterialScram])
	}
	if _, err := tpl.Materials("bad;password", nil); err == nil {
		t.Fatalf("render gate must refuse hostile passwords")
	}
}
