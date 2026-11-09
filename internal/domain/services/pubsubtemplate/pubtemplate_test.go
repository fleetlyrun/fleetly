package pubsubtemplate

// pubsubtemplate 门禁（B6a 补 B4b 缺课：digest 钉定 + 模板面 + kafka live 定案
// 钉板——databasetemplate_test.go 的 TestImageDigestsPinned/TestTemplateFaces 同构；
// bucketstoretemplate_test.go 单版最小形态先例）。kafka 断言值全部锚 2026-11-07
// 本机 docker live 实测形态（kafkadialect.go 包注实录）。

import (
	"strings"
	"testing"

	"github.com/fleetlyrun/fleetly/internal/domain/services/dataservicetemplate"
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

// TestToolUserDeclarations 钉 ToolUser 声明面（B8-fix）：kafka/redpanda
// 声明 root（非 root 镜像用户写 root 属主卷根/读 0600 材料均被拒——工具
// 面 root 化的主案）；nats 缺省空（镜像自带 root，零行为变化）。声明值域
// = {""，"root"}——宽于 docker User 词法的声明先例由未来引擎按需立法。
func TestToolUserDeclarations(t *testing.T) {
	for engine, want := range map[string]string{
		"nats":     "",
		"kafka":    "root",
		"redpanda": "root",
	} {
		tpl, err := For(engine, "")
		if err != nil {
			t.Fatalf("For(%s): %v", engine, err)
		}
		if tpl.ToolUser() != want {
			t.Fatalf("engine %q ToolUser() = %q, want %q", engine, tpl.ToolUser(), want)
		}
		// 绑版实例声明不随版本分叉（载体面知识，非版本面）。
		for _, spec := range tpl.Versions() {
			bound, err := For(engine, spec.ID)
			if err != nil {
				t.Fatalf("For(%s,%s): %v", engine, spec.ID, err)
			}
			if bound.ToolUser() != want {
				t.Fatalf("engine %s version %s ToolUser() = %q, want %q", engine, spec.ID, bound.ToolUser(), want)
			}
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
	if _, err := nats.ApplyDynamicSettings("pub-01hy", "secretpw123", map[string]string{"any": "1"}); err != dataservicetemplate.ErrNoDynamicFace {
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
	byKey := map[string]dataservicetemplate.Knob{}
	for _, k := range knobs {
		byKey[k.Key] = k
	}
	if byKey["log.retention.ms"].Class != dataservicetemplate.KnobDynamic || byKey["log.retention.ms"].Default == "" {
		t.Fatalf("log.retention.ms must be dynamic with a registered default (reset face)")
	}
	if byKey["message.max.bytes"].Class != dataservicetemplate.KnobDynamic || byKey["message.max.bytes"].Default == "" {
		t.Fatalf("message.max.bytes must be dynamic with a registered default (reset face)")
	}
	if byKey["num.partitions"].Class != dataservicetemplate.KnobStatic {
		t.Fatalf("num.partitions must be static (server.properties lane)")
	}
	if byKey["heap_mb"].Class != dataservicetemplate.KnobStatic {
		t.Fatalf("heap_mb must be static (KAFKA_HEAP_OPTS env lane)")
	}
	if err := dataservicetemplate.ValidateSettings(knobs, map[string]string{
		"log.retention.ms": "7d", "message.max.bytes": "1gb", "num.partitions": "8", "heap_mb": "512mb",
	}); err != nil {
		t.Fatalf("full legal knob set must validate: %v", err)
	}
	for key, bad := range map[string]string{
		"log.retention.ms": "7sec", "message.max.bytes": "512q", "num.partitions": "0", "heap_mb": "64mb",
	} {
		if err := dataservicetemplate.ValidateSettings(knobs, map[string]string{key: bad}); err == nil {
			t.Fatalf("knob %s=%s must be refused", key, bad)
		}
	}
}

// TestEngineBacksUp 钉 per-engine 能力位判据（B6a 拆分：kafka/redpanda
// false 挂账/nats true；离册引擎保守 false）。
func TestEngineBacksUp(t *testing.T) {
	if EngineBacksUp("kafka") {
		t.Fatalf("kafka carries no backup face (deferred)")
	}
	if EngineBacksUp("redpanda") {
		t.Fatalf("redpanda carries no backup face (deferred — same kafka-family ruling)")
	}
	if !EngineBacksUp("nats") {
		t.Fatalf("nats carries the per-stream backup face")
	}
	if EngineBacksUp("nosuchengine") {
		t.Fatalf("engines outside the registry must be conservatively false")
	}
}

// TestCatalogEngineLevelCapabilities 钉目录按引擎级真值（kafka/redpanda
// backup:false/migrate:false 诚实推论 + dialect 轴；nats backup:true；
// redpanda BSL 1.1 license_note 披露）。
func TestCatalogEngineLevelCapabilities(t *testing.T) {
	catalog := Catalog()
	found := map[string]CatalogEntry{}
	for _, e := range catalog {
		found[e.Engine] = e
	}
	if len(found) != 3 {
		t.Fatalf("catalog carries nats+kafka+redpanda, got %v", Engines())
	}
	if found["kafka"].Capabilities.Backup || found["kafka"].Capabilities.Migrate {
		t.Fatalf("kafka catalog must list backup:false and migrate:false (honest inference), got %+v", found["kafka"].Capabilities)
	}
	if found["redpanda"].Capabilities.Backup || found["redpanda"].Capabilities.Migrate {
		t.Fatalf("redpanda catalog must list backup:false and migrate:false (honest inference), got %+v", found["redpanda"].Capabilities)
	}
	if !found["nats"].Capabilities.Backup {
		t.Fatalf("nats catalog must list backup:true")
	}
	if found["kafka"].Dialect != "kafka" || found["nats"].Dialect != "nats" {
		t.Fatalf("dialect axis drifted: %+v", found)
	}
	// 方言族互示：redpanda 与 kafka 同 dialect=kafka（客户端零改动切换，
	// design §4.6）。
	if found["redpanda"].Dialect != "kafka" {
		t.Fatalf("redpanda shares the kafka dialect family, got %q", found["redpanda"].Dialect)
	}
	// BSL 披露（红队 C-AT-06 完整化：Additional Use Grant + 4 年 Change
	// Date；nats/kafka 无披露义务）。
	if !strings.Contains(found["redpanda"].LicenseNote, "BSL 1.1") ||
		!strings.Contains(found["redpanda"].LicenseNote, "Streaming Service") ||
		!strings.Contains(found["redpanda"].LicenseNote, "4 years") {
		t.Fatalf("redpanda license note must carry the full BSL disclosure, got %q", found["redpanda"].LicenseNote)
	}
	if found["kafka"].LicenseNote != "" || found["nats"].LicenseNote != "" {
		t.Fatalf("nats/kafka carry no license disclosure obligation")
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
	files, err := tpl.Materials("scram0123456789abcdef", 0, nil)
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
	if _, err := tpl.Materials("bad;password", 0, nil); err == nil {
		t.Fatalf("render gate must refuse hostile passwords")
	}
}

// —— redpanda（B6b，ADR-0096 决策 7 / design §4.6 redpanda 条目）：断言值
// 全部锚 2026-11-07 本机 docker live 实测形态（redpanda.go 包注实录）——
// 嵌 KafkaDialect 基座 + 载体面全自持（继承律逐面裁决的钉板半边）。

// TestRedpandaVersionMatrix 钉 redpanda 在册矩阵（26.1/26.2 双活两条、
// 26.2 Default——2026-11-07 Docker Hub 实值；25.3 EOL 临期不入册）。
func TestRedpandaVersionMatrix(t *testing.T) {
	tpl, err := For("redpanda", "")
	if err != nil {
		t.Fatalf("For(redpanda): %v", err)
	}
	if got := VersionIDs(tpl.Versions()); strings.Join(got, ",") != "26.1,26.2" {
		t.Fatalf("redpanda versions = %v, want [26.1 26.2]", got)
	}
	if tpl.Meta().Version != "26.2" || tpl.Meta().Port != 9092 {
		t.Fatalf("redpanda default meta = %+v, want version 26.2 port 9092", tpl.Meta())
	}
}

// TestRedpandaFaces 钉 redpanda 模板面（live 定案形态）：数据目录/探针
// （admin 9644 就绪 + 引导完成门）/基座继承面（dialect=kafka、ConnMaterial
// 同形）/无联动面（native 无 JVM 堆）。
func TestRedpandaFaces(t *testing.T) {
	tpl, err := For("redpanda", "")
	if err != nil {
		t.Fatalf("For(redpanda): %v", err)
	}
	if tpl.DataTarget() != "/var/lib/redpanda/data" {
		t.Fatalf("redpanda data target = %q", tpl.DataTarget())
	}
	if tpl.DataCapabilities().Backup {
		t.Fatalf("redpanda v1 carries no data backup face (deferred)")
	}
	// 基座继承半边：dialect 与 ConnMaterial（kafka 方言族互示）。
	if tpl.Dialect() != "kafka" {
		t.Fatalf("redpanda dialect = %q, want kafka (dialect family base)", tpl.Dialect())
	}
	mat := tpl.ConnMaterial("pub-01hx", "scram0123456789abcdef")
	if mat.Bootstrap != "pub-01hx:9092" || mat.Mechanism != "SCRAM-SHA-256" ||
		mat.User != KafkaPlatformUser || mat.Password != "scram0123456789abcdef" {
		t.Fatalf("redpanda conn material must ride the KafkaDialect base, got %+v", mat)
	}
	// 探针 = admin 9644 /v1/status/ready + 引导完成门（首启 admin 开放窗
	// 不随 running 暴露——铸用户+硬化落地前不绿）。
	probe := strings.Join(tpl.Probe(), " ")
	if probe != "/bin/bash -c test -f /tmp/fleetly-boot-done && curl -sf -o /dev/null http://127.0.0.1:9644/v1/status/ready" {
		t.Fatalf("redpanda probe = %v", probe)
	}
	// 无联动面：native 无 JVM 堆（memory 由 rpk 从 cgroup 自留）。
	if _, ok := tpl.(LinkageValidator); ok {
		t.Fatalf("redpanda must not implement the linkage face (no JVM heap — memory is cgroup-derived)")
	}
	// Workload：env 唯一键 = 通告面（rpk --advertise-kafka-addr 消费）；
	// memoryMB 忽略（无 heap 分支）。
	env, command, err := tpl.Workload("pub-01hx", 2048, nil)
	if err != nil {
		t.Fatalf("workload: %v", err)
	}
	if env["FLEETLY_ADVERTISE_KAFKA"] != "pub-01hx:9092" {
		t.Fatalf("advertise env = %q, want pub-01hx:9092", env["FLEETLY_ADVERTISE_KAFKA"])
	}
	if len(env) != 1 {
		t.Fatalf("redpanda workload env carries only the advertise face, got %v", env)
	}
	// 命令面 = bash -c 监督链（起服/就绪等待/幂等铸用户/硬化/门/wait/孤儿
	// 收尾——live 定案实录见 redpanda.go 包注）。
	if len(command) != 3 || command[0] != "/bin/bash" || command[1] != "-c" {
		t.Fatalf("redpanda command = %v, want bash -c supervision chain", command)
	}
	for _, anchor := range []string{
		"rpk redpanda start --config /tmp/fleetly-redpanda.yaml --check=false --advertise-kafka-addr \"$FLEETLY_ADVERTISE_KAFKA\"",
		"until curl -sf -o /dev/null http://127.0.0.1:9644/v1/status/ready",
		"rpk acl user create fleetly --password \"$PW\" --mechanism scram-sha-256",
		"rpk acl user update fleetly --new-password \"$PW\" --mechanism SCRAM-SHA-256",
		"rpk cluster config set admin_api_require_auth true",
		"touch /tmp/fleetly-boot-done",
		"while pidof redpanda >/dev/null 2>&1",
	} {
		if !strings.Contains(command[2], anchor) {
			t.Fatalf("redpanda bootstrap chain must contain %q, got:\n%s", anchor, command[2])
		}
	}
}

// TestRedpandaConfigRender 钉配置单源渲染（非 dev 模式完整 redpanda.yaml：
// auto_create_topics 收编 + SASL 面 + 生产形态 + listener 分面）。
func TestRedpandaConfigRender(t *testing.T) {
	conf := string(RedpandaConfigFile(0))
	for _, anchor := range []string{
		`data_directory: "/var/lib/redpanda/data"`,
		"seed_servers: []",
		`    port: 33145`, // rpc_server
		`    port: 9092`,  // kafka_api（客户端面）
		`    port: 9644`,  // admin（探针/工具面）
		"developer_mode: false",
		"auto_create_topics_enabled: false",
		"enable_sasl: true",
		`  - "` + KafkaPlatformUser + `"`,
		"overprovisioned: true",
	} {
		if !strings.Contains(conf, anchor) {
			t.Fatalf("redpanda config missing %q:\n%s", anchor, conf)
		}
	}
	// 通告面不在 yaml（host 依赖经 Workload env → rpk 旗标）。
	if strings.Contains(conf, "advertised_kafka_api") {
		t.Fatalf("advertised_kafka_api rides the Workload env face, not the host-free config file")
	}
	// 无限制（memoryMB=0）不渲染 rpk.memory/smp——B6b 无限制行为不变。
	if strings.Contains(conf, "memory:") || strings.Contains(conf, "smp:") {
		t.Fatalf("unlimited renders must not pin rpk.memory/smp (autodetect stays):\n%s", conf)
	}
}

// TestRedpandaConfigMemoryFace 钉 rpk.memory 显式化（B8-fix live 实录：rpk
// 的 cgroup 自动探测在受限容器把 seastar 预算算成 ~58MiB/shard 起服即 OOM
// 崩环——`docker run -m 2g` 直测复现；显式 memory+smp 同环境起服绿）。
func TestRedpandaConfigMemoryFace(t *testing.T) {
	for _, tc := range []struct {
		mem    int64
		anchor string
	}{
		{2048, "  memory: 1G\n  smp: 1\n"},   // limit/2（kafka heap F-13 分支二同判据）
		{1024, "  memory: 512M\n  smp: 1\n"}, // 非整 GiB 走 M 词法
		{256, "  memory: 256M\n  smp: 1\n"},  // 下限钳（128M 不可起服）
		{128, "  memory: 256M\n  smp: 1\n"},  // 下限以下的行值也钳到 256M
	} {
		conf := string(RedpandaConfigFile(tc.mem))
		if !strings.Contains(conf, tc.anchor) {
			t.Fatalf("memoryMB=%d must render %q:\n%s", tc.mem, tc.anchor, conf)
		}
	}
}

// TestRedpandaMaterials 钉材料面：完整 redpanda.yaml + scram 裸密码；空
// settings 产物恒定；坏密码拒。
func TestRedpandaMaterials(t *testing.T) {
	tpl, _ := For("redpanda", "")
	files, err := tpl.Materials("scram0123456789abcdef", 2048, nil)
	if err != nil {
		t.Fatalf("materials: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("redpanda materials = config + scram password file, got %d files", len(files))
	}
	if string(files[redpandaConfigFile]) != string(RedpandaConfigFile(2048)) {
		t.Fatalf("config material must equal the single-source render")
	}
	if string(files[redpandaScramFile]) != "scram0123456789abcdef" {
		t.Fatalf("scram material = raw password, got %q", files[redpandaScramFile])
	}
	if _, err := tpl.Materials("bad;password", 0, nil); err == nil {
		t.Fatalf("render gate must refuse hostile passwords")
	}
}

// TestRedpandaDynamicFace 钉 dynamic 热应用面（继承律 Apply 半边的显式
// 覆盖：rpk cluster config set——redpanda 自有动态面，非 kafka 基座的
// kafka-configs.sh）：bash -c 逐键链、受理口径值折算（duration→ms /
// size→bytes）、键名 snake_case 原生词、static 键拒、凭证住 profile 材料。
func TestRedpandaDynamicFace(t *testing.T) {
	tpl, err := For("redpanda", "")
	if err != nil {
		t.Fatalf("For(redpanda): %v", err)
	}
	spec, err := tpl.ApplyDynamicSettings("pub-01hx", "scram0123456789abcdef", map[string]string{
		"log_retention_ms": "90d",
		"log_segment_size": "64mb",
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(spec.Argv) != 3 || spec.Argv[0] != "/bin/bash" || spec.Argv[1] != "-c" {
		t.Fatalf("apply argv must be a bash -c chain, got %v", spec.Argv)
	}
	argv := spec.Argv[2]
	want := "rpk cluster config set log_retention_ms 7776000000 --config /run/secrets/" + redpandaClientFile +
		" && rpk cluster config set log_segment_size 67108864 --config /run/secrets/" + redpandaClientFile
	if argv != want {
		t.Fatalf("apply chain =\n%s\nwant\n%s", argv, want)
	}
	profile := string(spec.SecretFiles[redpandaClientFile])
	for _, anchor := range []string{"brokers:", "- \"pub-01hx:9092\"", "user: \"fleetly\"",
		"password: \"scram0123456789abcdef\"", "- \"pub-01hx:9644\""} {
		if !strings.Contains(profile, anchor) {
			t.Fatalf("apply profile missing %q:\n%s", anchor, profile)
		}
	}
	// 白名单外/static 键到达热应用面 = fail-closed。
	if _, err := tpl.ApplyDynamicSettings("pub-01hx", "scram0123456789abcdef", map[string]string{"heap_mb": "512mb"}); err == nil {
		t.Fatalf("a knob outside the dynamic whitelist must fail closed")
	}
	if _, err := tpl.ApplyDynamicSettings("pub-01hx", "scram0123456789abcdef", nil); err != dataservicetemplate.ErrNoDynamicFace {
		t.Fatalf("an empty dynamic set carries no face, got %v", err)
	}
}

// TestRedpandaRotateFace 钉轮换面（继承律 Rotate 半边的覆盖：rpk acl user
// update——admin 面数据面改密，live 定案新立即可用/旧即拒；argv 载 next、
// profile 材料载 current——kafka 轮换面同形）。
func TestRedpandaRotateFace(t *testing.T) {
	tpl, err := For("redpanda", "")
	if err != nil {
		t.Fatalf("For(redpanda): %v", err)
	}
	spec, err := tpl.RotatePassword("pub-01hx", "current0123456789", "next0123456789ab")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	argv := strings.Join(spec.Argv, " ")
	want := "rpk acl user update fleetly --new-password next0123456789ab --mechanism SCRAM-SHA-256 --config /run/secrets/" + redpandaClientFile
	if argv != want {
		t.Fatalf("rotate argv =\n%s\nwant\n%s", argv, want)
	}
	profile := string(spec.SecretFiles[redpandaClientFile])
	if !strings.Contains(profile, "current0123456789") || strings.Contains(profile, "next0123456789ab") {
		t.Fatalf("rotate must authenticate with the current credential (next stays out of the profile)")
	}
}

// TestRedpandaBackupDeferred 钉备份/恢复挂账哨兵（引擎专属哨兵——受理位
// 按能力位门控先拒，文案锚「no data backup face」共用）。
func TestRedpandaBackupDeferred(t *testing.T) {
	tpl, err := For("redpanda", "")
	if err != nil {
		t.Fatalf("For(redpanda): %v", err)
	}
	if _, err := tpl.Backup("pub-01hx", "scram0123456789abcdef"); err == nil || !strings.Contains(err.Error(), "no data backup face") {
		t.Fatalf("redpanda backup must refuse with the deferred sentinel, got %v", err)
	}
	if _, err := tpl.Restore("pub-01hx", "scram0123456789abcdef"); err == nil || !strings.Contains(err.Error(), "no data backup face") {
		t.Fatalf("redpanda restore must refuse with the deferred sentinel, got %v", err)
	}
	if _, err := tpl.Backup("pub-01hx", "bad;password"); err == nil {
		t.Fatalf("render gate must fire before the sentinel check on hostile passwords")
	}
}

// TestRedpandaKnobSchema 钉旋钮集（live 定案最小集：cluster config 动态面
// 两键、全 dynamic、带 Default 复位面；message.max.bytes 族诚实缺席——
// redpanda 消息上限是 topic 级非 cluster 属性）。
func TestRedpandaKnobSchema(t *testing.T) {
	knobs := redpandaTemplate{}.Knobs()
	if len(knobs) != 2 {
		t.Fatalf("redpanda carries exactly two knobs (live-verified minimal set), got %d", len(knobs))
	}
	byKey := map[string]dataservicetemplate.Knob{}
	for _, k := range knobs {
		byKey[k.Key] = k
	}
	if byKey["log_retention_ms"].Class != dataservicetemplate.KnobDynamic || byKey["log_retention_ms"].Default != "168h" {
		t.Fatalf("log_retention_ms must be dynamic with the 7d registered default (reset face)")
	}
	if byKey["log_segment_size"].Class != dataservicetemplate.KnobDynamic || byKey["log_segment_size"].Default != "128mb" {
		t.Fatalf("log_segment_size must be dynamic with the 128mb registered default (reset face)")
	}
	if err := dataservicetemplate.ValidateSettings(knobs, map[string]string{
		"log_retention_ms": "7d", "log_segment_size": "256mb",
	}); err != nil {
		t.Fatalf("full legal knob set must validate: %v", err)
	}
	for key, bad := range map[string]string{
		"log_retention_ms": "7sec", "log_segment_size": "512q",
	} {
		if err := dataservicetemplate.ValidateSettings(knobs, map[string]string{key: bad}); err == nil {
			t.Fatalf("knob %s=%s must be refused", key, bad)
		}
	}
}
