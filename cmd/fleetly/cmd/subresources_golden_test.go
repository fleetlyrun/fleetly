package cmd

// 子资源动词组 golden（B8，ADR-0096 决策 10）：`pubsubs topics`/
// `pubsubs streams`/`buckets bucket` 三组双形态钉板——create/list/get/
// update（确认三形态 + 分区减拒）/delete（wide 披露 + 确认拒绝面）。golden
// 夹具无引擎实况（fake 工具容器）——收敛行恒 pending/披露行取缺席腿，输出
// 确定性由行面承载。

import (
	"testing"
)

// seedSubParent 建父实例并返回其 id（kafka/nats/silo 三形态的父面）。
func seedSubParent(t *testing.T, projectID, engine, name string) string {
	t.Helper()
	var args []string
	switch engine {
	case "silo":
		args = []string{"buckets", "create", "--project", projectID, "--engine", engine, name}
	default:
		args = []string{"pubsubs", "create", "--project", projectID, "--engine", engine, name}
	}
	code, out, stderr := runCLI(t, args...)
	if code != 0 || stderr != "" {
		t.Fatalf("seed parent (%v): code=%d stderr=%q", args, code, stderr)
	}
	return extractTailID(out)
}

// TestGoldenSubResourceGroups 钉子资源嵌套组的组帮助面（coverage 契约：
// 组本身也要双形态 golden——apps vars 同款）。
func TestGoldenSubResourceGroups(t *testing.T) {
	_ = newGoldenHarness(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"pubsubs-topics", []string{"pubsubs", "topics"}},
		{"pubsubs-streams", []string{"pubsubs", "streams"}},
		{"buckets-bucket", []string{"buckets", "bucket"}},
	} {
		code, out, stderr := runCLI(t, tc.args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%s: code=%d stderr=%q", tc.name, code, stderr)
		}
		compareGolden(t, tc.name, out)
		code, out, stderr = runCLI(t, append(append([]string(nil), tc.args...), "--json")...)
		if code != 0 || stderr != "" {
			t.Fatalf("%s --json: code=%d stderr=%q", tc.name, code, stderr)
		}
		compareGolden(t, tc.name+"-json", out)
	}
}

func TestGoldenTopicsLifecycle(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "sub-topics")
	pubsubID := seedSubParent(t, projectID, "kafka", "kbus")

	code, out, stderr := runCLI(t, "pubsubs", "topics", "create", "--pubsub", pubsubID,
		"--partitions", "3", "--retention-ms", "3600000", "orders")
	if code != 0 || stderr != "" {
		t.Fatalf("topics create: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-create", normalizeGolden(out))
	topicID := extractTailID(out)
	code, out, stderr = runCLI(t, "pubsubs", "topics", "create", "--pubsub", pubsubID,
		"--partitions", "1", "bills", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("topics create --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-create-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "pubsubs", "topics", "list", "--pubsub", pubsubID)
	if code != 0 || stderr != "" {
		t.Fatalf("topics list: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-list", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "topics", "list", "--pubsub", pubsubID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("topics list --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-list-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "pubsubs", "topics", "get", "--pubsub", pubsubID, topicID)
	if code != 0 || stderr != "" {
		t.Fatalf("topics get: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-get", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "topics", "get", "--pubsub", pubsubID, topicID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("topics get --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-get-json", normalizeGolden(out))

	// update：非 TTY 裸调用 = 本地确认指引；--preview = diff + ticket 两调用
	// 流第 1 步；分区减 = 方言层拒（预演受理即拒）。
	forceNoTTY(t)
	code, _, stderr = runCLI(t, "pubsubs", "topics", "update", "--pubsub", pubsubID,
		"--partitions", "5", topicID)
	if code == 0 {
		t.Fatalf("topics update without confirmation must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-topics-update-confirm-required", normalizeConfirmGolden(stderr))
	code, out, stderr = runCLI(t, "pubsubs", "topics", "update", "--pubsub", pubsubID,
		"--partitions", "5", "--preview", topicID)
	if code != 0 || stderr != "" {
		t.Fatalf("topics update --preview: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-update-preview", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "topics", "update", "--pubsub", pubsubID,
		"--partitions", "5", "--preview", topicID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("topics update --preview --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-update-preview-json", normalizeGolden(out))
	code, _, stderr = runCLI(t, "pubsubs", "topics", "update", "--pubsub", pubsubID,
		"--partitions", "1", "--yes", topicID)
	if code == 0 {
		t.Fatalf("shrinking partitions must be refused by the dialect gate, got exit 0")
	}
	compareGolden(t, "pubsubs-topics-update-shrink-refused", normalizeConfirmGolden(stderr))
	code, out, stderr = runCLI(t, "pubsubs", "topics", "update", "--pubsub", pubsubID,
		"--partitions", "5", "--retention-ms", "7200000", "--yes", topicID)
	if code != 0 || stderr != "" {
		t.Fatalf("topics update --yes: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-update", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "topics", "update", "--pubsub", pubsubID,
		"--retention-ms", "0", "--yes", topicID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("topics update --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-update-json", normalizeGolden(out))

	// delete：wide 披露（--preview 面）与非 TTY 裸调用拒绝面。
	forceNoTTY(t)
	code, _, stderr = runCLI(t, "pubsubs", "topics", "delete", "--pubsub", pubsubID, topicID)
	if code == 0 {
		t.Fatalf("topics delete without confirmation must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-topics-delete-confirm-required", normalizeConfirmGolden(stderr))
	code, out, stderr = runCLI(t, "pubsubs", "topics", "delete", "--pubsub", pubsubID,
		"--preview", topicID)
	if code != 0 || stderr != "" {
		t.Fatalf("topics delete --preview: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-delete-preview", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "topics", "delete", "--pubsub", pubsubID,
		"--preview", topicID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("topics delete --preview --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-delete-preview-json", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "topics", "delete", "--pubsub", pubsubID,
		"--yes", topicID)
	if code != 0 || stderr != "" {
		t.Fatalf("topics delete --yes: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-delete", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "topics", "delete", "--pubsub", pubsubID,
		"--yes", "bills", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("topics delete --yes --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-topics-delete-json", normalizeGolden(out))

	// 跨引擎可用性拒（kafka 实例上的 streams 面对照）。
	natsID := seedSubParent(t, projectID, "nats", "nbus")
	code, _, stderr = runCLI(t, "pubsubs", "topics", "create", "--pubsub", natsID, "nope")
	if code == 0 {
		t.Fatalf("the topic face on a nats instance must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-topics-wrong-engine", normalizeConfirmGolden(stderr))
}

func TestGoldenStreamsLifecycle(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "sub-streams")
	pubsubID := seedSubParent(t, projectID, "nats", "nbus")

	code, out, stderr := runCLI(t, "pubsubs", "streams", "create", "--pubsub", pubsubID,
		"--subjects", "orders.>,bills.>", "--retention", "limits", "--storage", "file",
		"--max-stream-size", "1048576", "ORDERS")
	if code != 0 || stderr != "" {
		t.Fatalf("streams create: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-create", normalizeGolden(out))
	streamID := extractTailID(out)
	code, out, stderr = runCLI(t, "pubsubs", "streams", "create", "--pubsub", pubsubID, "AUDIT", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("streams create --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-create-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "pubsubs", "streams", "list", "--pubsub", pubsubID)
	if code != 0 || stderr != "" {
		t.Fatalf("streams list: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-list", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "streams", "list", "--pubsub", pubsubID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("streams list --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-list-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "pubsubs", "streams", "get", "--pubsub", pubsubID, streamID)
	if code != 0 || stderr != "" {
		t.Fatalf("streams get: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-get", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "streams", "get", "--pubsub", pubsubID, streamID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("streams get --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-get-json", normalizeGolden(out))

	forceNoTTY(t)
	code, _, stderr = runCLI(t, "pubsubs", "streams", "update", "--pubsub", pubsubID,
		"--subjects", "orders.>", "--subjects-set", streamID)
	if code == 0 {
		t.Fatalf("streams update without confirmation must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-streams-update-confirm-required", normalizeConfirmGolden(stderr))
	code, out, stderr = runCLI(t, "pubsubs", "streams", "update", "--pubsub", pubsubID,
		"--subjects", "orders.>", "--subjects-set", "--preview", streamID)
	if code != 0 || stderr != "" {
		t.Fatalf("streams update --preview: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-update-preview", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "streams", "update", "--pubsub", pubsubID,
		"--subjects", "orders.>", "--subjects-set", "--max-stream-size", "2097152", "--yes", streamID)
	if code != 0 || stderr != "" {
		t.Fatalf("streams update --yes: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-update", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "streams", "update", "--pubsub", pubsubID,
		"--max-stream-size", "0", "--yes", streamID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("streams update --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-update-json", normalizeGolden(out))

	forceNoTTY(t)
	code, _, stderr = runCLI(t, "pubsubs", "streams", "delete", "--pubsub", pubsubID, streamID)
	if code == 0 {
		t.Fatalf("streams delete without confirmation must be refused, got exit 0")
	}
	compareGolden(t, "pubsubs-streams-delete-confirm-required", normalizeConfirmGolden(stderr))
	code, out, stderr = runCLI(t, "pubsubs", "streams", "delete", "--pubsub", pubsubID,
		"--preview", streamID)
	if code != 0 || stderr != "" {
		t.Fatalf("streams delete --preview: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-delete-preview", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "streams", "delete", "--pubsub", pubsubID,
		"--yes", streamID)
	if code != 0 || stderr != "" {
		t.Fatalf("streams delete --yes: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-delete", normalizeGolden(out))
	code, out, stderr = runCLI(t, "pubsubs", "streams", "delete", "--pubsub", pubsubID,
		"--yes", "AUDIT", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("streams delete --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "pubsubs-streams-delete-json", normalizeGolden(out))
}

func TestGoldenBucketsSubLifecycle(t *testing.T) {
	_ = newGoldenHarness(t)
	projectID := seedDatabaseProject(t, "sub-buckets")
	storeID := seedSubParent(t, projectID, "silo", "objects")

	code, out, stderr := runCLI(t, "buckets", "bucket", "create", "--store", storeID,
		"--quota-bytes", "1073741824", "media")
	if code != 0 || stderr != "" {
		t.Fatalf("bucket create: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-create", normalizeGolden(out))
	bucketID := extractTailID(out)
	code, out, stderr = runCLI(t, "buckets", "bucket", "create", "--store", storeID, "logs", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("bucket create --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-create-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "buckets", "bucket", "list", "--store", storeID)
	if code != 0 || stderr != "" {
		t.Fatalf("bucket list: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-list", normalizeGolden(out))
	code, out, stderr = runCLI(t, "buckets", "bucket", "list", "--store", storeID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("bucket list --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-list-json", normalizeGolden(out))

	code, out, stderr = runCLI(t, "buckets", "bucket", "get", "--store", storeID, bucketID)
	if code != 0 || stderr != "" {
		t.Fatalf("bucket get: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-get", normalizeGolden(out))
	code, out, stderr = runCLI(t, "buckets", "bucket", "get", "--store", storeID, bucketID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("bucket get --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-get-json", normalizeGolden(out))

	forceNoTTY(t)
	code, _, stderr = runCLI(t, "buckets", "bucket", "update", "--store", storeID,
		"--quota-bytes", "2147483648", bucketID)
	if code == 0 {
		t.Fatalf("bucket update without confirmation must be refused, got exit 0")
	}
	compareGolden(t, "buckets-bucket-update-confirm-required", normalizeConfirmGolden(stderr))
	code, out, stderr = runCLI(t, "buckets", "bucket", "update", "--store", storeID,
		"--quota-bytes", "2147483648", "--preview", bucketID)
	if code != 0 || stderr != "" {
		t.Fatalf("bucket update --preview: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-update-preview", normalizeGolden(out))
	code, out, stderr = runCLI(t, "buckets", "bucket", "update", "--store", storeID,
		"--quota-bytes", "2147483648", "--yes", bucketID)
	if code != 0 || stderr != "" {
		t.Fatalf("bucket update --yes: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-update", normalizeGolden(out))
	code, out, stderr = runCLI(t, "buckets", "bucket", "update", "--store", storeID,
		"--quota-bytes", "0", "--yes", bucketID, "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("bucket update --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-update-json", normalizeGolden(out))

	forceNoTTY(t)
	code, _, stderr = runCLI(t, "buckets", "bucket", "delete", "--store", storeID, bucketID)
	if code == 0 {
		t.Fatalf("bucket delete without confirmation must be refused, got exit 0")
	}
	compareGolden(t, "buckets-bucket-delete-confirm-required", normalizeConfirmGolden(stderr))
	code, out, stderr = runCLI(t, "buckets", "bucket", "delete", "--store", storeID,
		"--preview", bucketID)
	if code != 0 || stderr != "" {
		t.Fatalf("bucket delete --preview: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-delete-preview", normalizeGolden(out))
	code, out, stderr = runCLI(t, "buckets", "bucket", "delete", "--store", storeID,
		"--yes", bucketID)
	if code != 0 || stderr != "" {
		t.Fatalf("bucket delete --yes: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-delete", normalizeGolden(out))
	code, out, stderr = runCLI(t, "buckets", "bucket", "delete", "--store", storeID,
		"--yes", "logs", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("bucket delete --json: code=%d stderr=%q", code, stderr)
	}
	compareGolden(t, "buckets-bucket-delete-json", normalizeGolden(out))
}
