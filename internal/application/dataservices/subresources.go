package dataservices

// 子资源收敛段（B8，ADR-0096 决策 10 / design §4.7「声明行 + admin 工具
// 容器收敛」）：挂 dataservices 卫星分族步的尾段——pubsub 族步尾对账
// Topic（kafka 协议族引擎）与 Stream（nats 引擎），bucketstore 族步尾对账
// Bucket（silo）。对账协议（严格声明式，ADR-0081/0082）：
//
//   - **status 门**：父实例非 running 整段跳过（design §4.7 收敛段前置
//     status 门——工具容器需要活的 admin 面）；
//   - **幂等**：行集期望态摘要进 subMemo（ensureFresh 端口同协议：60s
//     强制重放窗——窗内同签名零动作；行集变化〔创建/更新/删除〕即失配
//     立即对账）；
//   - **ensure**：活跃行对照实况（方言 admin 面探测——存在+配置匹配即
//     ready 零动作；缺席 → 创建；失配 → 变更〔Topic partitions 可增不可减
//     等引擎约束：实况超出期望 = 平台不可收敛的漂移，行落 error 如实〕）；
//   - **GC（声明式）**：tombstone 行 → 删实况（Topic/Stream/Bucket 删除即
//     数据删除——受理位已过 wide 档确认）→ 物理删成功后硬删行；实况已
//     缺席视为已收口；
//   - **失败退避**：对账失败按 replayFails 同款窗退避（防工具容器 churn）。
//
// 负空间（平台只管声明行）：用户/App 经数据面自建的 topic/stream/bucket
// 不是行——对账段不触碰（design §4.7「对照行期望态」的字面义）。

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/fleetlyrun/fleetly/internal/application/engine/contract"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketresource"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/data/stream"
	"github.com/fleetlyrun/fleetly/internal/domain/data/topic"
	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// 子资源对账 memo 键（家族前缀分持一键空间）。
const (
	subMemoPubSub       = "pubsub:"
	subMemoBucketStore  = "bucketstore:"
	subBackoffKeyPrefix = "subresource-"
)

// reconcilePubSubSubResources 是 pubsub 族分族步的子资源尾段（stepPubSub
// 逐行调用，在 reconcilePubSub 之后——maintenance 读锁已释放，工具容器
// 链自持锁）。engine 决定子资源可用性：kafka 协议族 → Topic；nats →
// Stream；另一引擎受理位拒后本段零行可对账（防禘认真跳过）。
func (d *DataServices) reconcilePubSubSubResources(ctx context.Context, row *pubsub.PubSub) {
	if row.Status != pubsub.StatusRunning || row.Deleted() {
		return // status 门（design §4.7）：非 running 父实例无 admin 面。
	}
	topics, err := d.repos.Topics.ListByPubSub(ctx, row.ID)
	if err != nil {
		d.log.Error("subresource step: list topics", "pubsub", row.ID, "err", err)
		return
	}
	streams, err := d.repos.Streams.ListByPubSub(ctx, row.ID)
	if err != nil {
		d.log.Error("subresource step: list streams", "pubsub", row.ID, "err", err)
		return
	}
	if len(topics) == 0 && len(streams) == 0 {
		return // 零子资源行：零动作零 memo（负空间 + 零成本）。
	}
	sig := pubsubSubSig(topics, streams)
	if _, fresh := d.ensureFresh(d.subMemo, subMemoPubSub+row.ID, sig, d.clock.Now()); fresh {
		return
	}
	if d.replayInBackoff(subBackoffKeyPrefix + row.ID) {
		return // 失败退避窗：上一拍失败的实例本拍跳过。
	}
	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
	if err != nil {
		d.log.Error("subresource step: engine left the template registry", "pubsub", row.ID, "engine", row.Engine, "err", err)
		return
	}
	password, err := d.pubsubPassword(ctx, row)
	if err != nil {
		d.log.Error("subresource step: resolve credentials", "pubsub", row.ID, "err", err)
		return
	}
	ns := capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name}
	host := contract.PubSubDNSName(row.ID)

	failed := false
	if topicFace := pubsubtemplate.TopicAdminFaceOf(tpl); topicFace != nil {
		if !d.ensureTopics(ctx, ns, host, password, adminTool{image: tpl.Image(), user: tpl.ToolUser()}, topicFace, topics) {
			failed = true
		}
	}
	if streamFace := pubsubtemplate.StreamAdminFaceOf(tpl); streamFace != nil {
		if !d.ensureStreams(ctx, ns, host, password, streamFace, streams) {
			failed = true
		}
	}
	if failed {
		d.markReplayFailure(subBackoffKeyPrefix + row.ID)
		return // memo 不记：下一拍（退避窗外）重试。
	}
	d.clearReplayFailure(subBackoffKeyPrefix + row.ID)
	d.ensureRemember(d.subMemo, subMemoPubSub+row.ID, contract.Memo{Sig: sig, Gen: 0, At: d.clock.Now()})
}

// reconcileBucketSubResources 是 bucketstore 族分族步的子资源尾段（同上）。
func (d *DataServices) reconcileBucketSubResources(ctx context.Context, row *bucketstore.BucketStore) {
	if row.Status != bucketstore.StatusRunning || row.Deleted() {
		return
	}
	buckets, err := d.repos.Buckets.ListByBucketStore(ctx, row.ID)
	if err != nil {
		d.log.Error("subresource step: list buckets", "bucket_store", row.ID, "err", err)
		return
	}
	if len(buckets) == 0 {
		return
	}
	sig := bucketSubSig(buckets)
	if _, fresh := d.ensureFresh(d.subMemo, subMemoBucketStore+row.ID, sig, d.clock.Now()); fresh {
		return
	}
	if d.replayInBackoff(subBackoffKeyPrefix + row.ID) {
		return
	}
	tpl, err := bucketstoretemplate.For(row.Engine, row.Version)
	if err != nil {
		d.log.Error("subresource step: engine left the template registry", "bucket_store", row.ID, "engine", row.Engine, "err", err)
		return
	}
	face := bucketstoretemplate.BucketAdminFaceOf(tpl)
	if face == nil {
		return
	}
	accessKey, secretKey, err := d.bucketStoreCredentials(ctx, row)
	if err != nil {
		d.log.Error("subresource step: resolve credentials", "bucket_store", row.ID, "err", err)
		return
	}
	ns := capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, BucketStore: row.ID, BucketStoreName: row.Name}
	host := contract.BucketStoreDNSName(row.ID)

	failed := false
	for i := range buckets {
		b := &buckets[i]
		if b.Deleted() {
			if !d.gcBucket(ctx, ns, host, accessKey, secretKey, adminTool{image: tpl.Image(), user: tpl.ToolUser()}, face, b) {
				failed = true
			}
			continue
		}
		if !d.ensureBucket(ctx, ns, host, accessKey, secretKey, adminTool{image: tpl.Image(), user: tpl.ToolUser()}, face, b) {
			failed = true
		}
	}
	if failed {
		d.markReplayFailure(subBackoffKeyPrefix + row.ID)
		return
	}
	d.clearReplayFailure(subBackoffKeyPrefix + row.ID)
	d.ensureRemember(d.subMemo, subMemoBucketStore+row.ID, contract.Memo{Sig: sig, Gen: 0, At: d.clock.Now()})
}

// —— Topic ensure / GC ——

// ensureTopics 对账单实例的 Topic 行集；返回是否全部成功（失败面行内留痕，
// 不阻断兄弟行）。
func (d *DataServices) ensureTopics(ctx context.Context, ns capability.NamespaceRef, host, password string, tool adminTool, face pubsubtemplate.TopicAdminFace, rows []topic.Topic) bool {
	ok := true
	for i := range rows {
		t := &rows[i]
		if t.Deleted() {
			if !d.gcTopic(ctx, ns, host, password, tool, face, t) {
				ok = false
			}
			continue
		}
		if !d.ensureTopic(ctx, ns, host, password, tool, face, t) {
			ok = false
		}
	}
	return ok
}

// ensureTopic 对账单 Topic 行：探测 → 差异 → 创建/变更 → 复核 → 状态落行。
func (d *DataServices) ensureTopic(ctx context.Context, ns capability.NamespaceRef, host, password string, tool adminTool, face pubsubtemplate.TopicAdminFace, t *topic.Topic) bool {
	live, err := d.probeTopic(ctx, ns, host, password, tool, face, t.Name)
	if err != nil {
		d.subTopicFail(ctx, t, "inspect: %v", err)
		return false
	}
	if !live.Exists {
		spec, err := face.CreateTopic(host, password, t.Name, t.Partitions, t.RetentionMS)
		if err != nil {
			d.subTopicFail(ctx, t, "render create: %v", err)
			return false
		}
		if err := d.runAdminUtility(ctx, "topic-create-"+t.ID, ns, tool, spec); err != nil {
			d.subTopicFail(ctx, t, "create: %v", err)
			return false
		}
	} else {
		// 分区对照：可增不可减（引擎约束）——实况超出期望 = 不可收敛漂移。
		if live.Partitions < t.Partitions {
			spec, err := face.AlterTopicPartitions(host, password, t.Name, live.Partitions, t.Partitions)
			if err != nil {
				d.subTopicFail(ctx, t, "render partition change: %v", err)
				return false
			}
			if err := d.runAdminUtility(ctx, "topic-partitions-"+t.ID, ns, tool, spec); err != nil {
				d.subTopicFail(ctx, t, "partition change: %v", err)
				return false
			}
		} else if live.Partitions > t.Partitions {
			d.subTopicFail(ctx, t,
				"live topic carries %d partitions but the row declares %d; kafka partitions only increase — raise the row to %d or recreate the topic (engine refuses decreases to preserve key ordering)",
				live.Partitions, t.Partitions, live.Partitions)
			return false
		}
		// retention 对照：行 nil = 不管理（负空间——live 显式值不触碰）。
		if t.RetentionMS != nil && (live.RetentionMS == nil || *live.RetentionMS != *t.RetentionMS) {
			spec, err := face.AlterTopicRetention(host, password, t.Name, *t.RetentionMS)
			if err != nil {
				d.subTopicFail(ctx, t, "render retention change: %v", err)
				return false
			}
			if err := d.runAdminUtility(ctx, "topic-retention-"+t.ID, ns, tool, spec); err != nil {
				d.subTopicFail(ctx, t, "retention change: %v", err)
				return false
			}
		}
	}
	// 复核（变更后实况确认——ready 只在实况匹配时落）。
	after, err := d.probeTopic(ctx, ns, host, password, tool, face, t.Name)
	if err != nil {
		d.subTopicFail(ctx, t, "re-inspect: %v", err)
		return false
	}
	if after.Exists && after.Partitions == t.Partitions && retentionMatch(t.RetentionMS, after.RetentionMS) {
		d.subTopicReady(ctx, t)
		return true
	}
	d.subTopicFail(ctx, t, "post-change inspect does not match the desired state (live partitions %d, retention set %v)", after.Partitions, after.RetentionMS != nil)
	return false
}

// gcTopic 删实况 Topic（声明式 GC：wide 档确认已在受理位完成）→ 硬删行。
func (d *DataServices) gcTopic(ctx context.Context, ns capability.NamespaceRef, host, password string, tool adminTool, face pubsubtemplate.TopicAdminFace, t *topic.Topic) bool {
	live, err := d.probeTopic(ctx, ns, host, password, tool, face, t.Name)
	if err == nil && !live.Exists {
		return d.hardDeleteTopicRow(ctx, t)
	}
	spec, err := face.DeleteTopic(host, password, t.Name)
	if err != nil {
		d.log.Error("subresource gc: render topic delete", "topic", t.ID, "err", err)
		return false
	}
	if err := d.runAdminUtility(ctx, "topic-gc-"+t.ID, ns, tool, spec); err != nil {
		d.log.Error("subresource gc: delete topic", "topic", t.ID, "err", err)
		return false
	}
	return d.hardDeleteTopicRow(ctx, t)
}

func (d *DataServices) hardDeleteTopicRow(ctx context.Context, t *topic.Topic) bool {
	if err := d.repos.Topics.Delete(ctx, t.ID); err != nil {
		d.log.Error("subresource gc: delete topic row", "topic", t.ID, "err", err)
		return false
	}
	d.log.Info("subresource gc: topic removed", "topic", t.ID, "name", t.Name, "pubsub", t.PubSubID)
	return true
}

func (d *DataServices) subTopicFail(ctx context.Context, t *topic.Topic, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	d.log.Error("subresource ensure: topic", "topic", t.ID, "pubsub", t.PubSubID, "err", msg)
	if err := d.repos.Topics.SetStatus(ctx, t.ID, topic.StatusError, truncateSubError(msg)); err != nil {
		d.log.Error("subresource ensure: set topic status", "topic", t.ID, "err", err)
	}
}

func (d *DataServices) subTopicReady(ctx context.Context, t *topic.Topic) {
	if t.Status == topic.StatusReady && t.StatusError == "" {
		return // 幂等：已 ready 零写。
	}
	if err := d.repos.Topics.SetStatus(ctx, t.ID, topic.StatusReady, ""); err != nil {
		d.log.Error("subresource ensure: set topic status", "topic", t.ID, "err", err)
	}
}

// probeTopic 执行一次实况探测（方言面渲染 + 工具容器捕获 + 方言解析）。
func (d *DataServices) probeTopic(ctx context.Context, ns capability.NamespaceRef, host, password string, tool adminTool, face pubsubtemplate.TopicAdminFace, name string) (pubsubtemplate.TopicLive, error) {
	spec, err := face.InspectTopic(host, password, name)
	if err != nil {
		return pubsubtemplate.TopicLive{}, err
	}
	stdout, stderr, runErr := d.runAdminCapture(ctx, "topic-inspect-"+sanitizeSubID(name), ns, tool, spec)
	return face.ParseTopicInspect(stdout, stderr, runErr)
}

// —— Stream ensure / GC ——

func (d *DataServices) ensureStreams(ctx context.Context, ns capability.NamespaceRef, host, password string, face pubsubtemplate.StreamAdminFace, rows []stream.Stream) bool {
	tool := adminTool{image: pubsubtemplate.NatsBoxImage()}
	ok := true
	for i := range rows {
		s := &rows[i]
		if s.Deleted() {
			if !d.gcStream(ctx, ns, host, password, tool, face, s) {
				ok = false
			}
			continue
		}
		if !d.ensureStream(ctx, ns, host, password, tool, face, s) {
			ok = false
		}
	}
	return ok
}

// ensureStream 对账单 Stream 行（可变面 subjects/max_bytes；不可变轴
// retention/storage 建行即冻结——实况失配 = 不可收敛漂移，如实报错）。
func (d *DataServices) ensureStream(ctx context.Context, ns capability.NamespaceRef, host, password string, tool adminTool, face pubsubtemplate.StreamAdminFace, s *stream.Stream) bool {
	live, err := d.probeStream(ctx, ns, host, password, tool, face, s.Name)
	if err != nil {
		d.subStreamFail(ctx, s, "inspect: %v", err)
		return false
	}
	if !live.Exists {
		spec, err := face.CreateStream(host, password, s.Name, s.Subjects, s.Retention, s.Storage, s.MaxBytes)
		if err != nil {
			d.subStreamFail(ctx, s, "render create: %v", err)
			return false
		}
		if err := d.runAdminUtility(ctx, "stream-create-"+s.ID, ns, tool, spec); err != nil {
			d.subStreamFail(ctx, s, "create: %v", err)
			return false
		}
	} else {
		// 不可变轴对照（实况失配 = 建行后被外因改动——CLI 无变更通道，
		// 平台不可收敛，如实报错）。
		if !strings.EqualFold(live.Retention, s.Retention) || !strings.EqualFold(live.Storage, s.Storage) {
			d.subStreamFail(ctx, s,
				"live stream retention/storage (%s/%s) differ from the row (%s/%s); these axes are immutable after create — recreate the stream to change them",
				live.Retention, live.Storage, s.Retention, s.Storage)
			return false
		}
		if !subjectsEqual(live.Subjects, s.Subjects) || !maxBytesMatch(s.MaxBytes, live.MaxBytes, live.MaxBytesSet) {
			spec, err := face.UpdateStream(host, password, s.Name, s.Subjects, s.MaxBytes)
			if err != nil {
				d.subStreamFail(ctx, s, "render update: %v", err)
				return false
			}
			if err := d.runAdminUtility(ctx, "stream-update-"+s.ID, ns, tool, spec); err != nil {
				d.subStreamFail(ctx, s, "update: %v", err)
				return false
			}
		}
	}
	after, err := d.probeStream(ctx, ns, host, password, tool, face, s.Name)
	if err != nil {
		d.subStreamFail(ctx, s, "re-inspect: %v", err)
		return false
	}
	if after.Exists && strings.EqualFold(after.Retention, s.Retention) &&
		strings.EqualFold(after.Storage, s.Storage) &&
		subjectsEqual(after.Subjects, s.Subjects) && maxBytesMatch(s.MaxBytes, after.MaxBytes, after.MaxBytesSet) {
		d.subStreamReady(ctx, s)
		return true
	}
	d.subStreamFail(ctx, s, "post-change inspect does not match the desired state")
	return false
}

func (d *DataServices) gcStream(ctx context.Context, ns capability.NamespaceRef, host, password string, tool adminTool, face pubsubtemplate.StreamAdminFace, s *stream.Stream) bool {
	live, err := d.probeStream(ctx, ns, host, password, tool, face, s.Name)
	if err == nil && !live.Exists {
		return d.hardDeleteStreamRow(ctx, s)
	}
	spec, err := face.DeleteStream(host, password, s.Name)
	if err != nil {
		d.log.Error("subresource gc: render stream delete", "stream", s.ID, "err", err)
		return false
	}
	if err := d.runAdminUtility(ctx, "stream-gc-"+s.ID, ns, tool, spec); err != nil {
		d.log.Error("subresource gc: delete stream", "stream", s.ID, "err", err)
		return false
	}
	return d.hardDeleteStreamRow(ctx, s)
}

func (d *DataServices) hardDeleteStreamRow(ctx context.Context, s *stream.Stream) bool {
	if err := d.repos.Streams.Delete(ctx, s.ID); err != nil {
		d.log.Error("subresource gc: delete stream row", "stream", s.ID, "err", err)
		return false
	}
	d.log.Info("subresource gc: stream removed", "stream", s.ID, "name", s.Name, "pubsub", s.PubSubID)
	return true
}

func (d *DataServices) subStreamFail(ctx context.Context, s *stream.Stream, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	d.log.Error("subresource ensure: stream", "stream", s.ID, "pubsub", s.PubSubID, "err", msg)
	if err := d.repos.Streams.SetStatus(ctx, s.ID, stream.StatusError, truncateSubError(msg)); err != nil {
		d.log.Error("subresource ensure: set stream status", "stream", s.ID, "err", err)
	}
}

func (d *DataServices) subStreamReady(ctx context.Context, s *stream.Stream) {
	if s.Status == stream.StatusReady && s.StatusErr == "" {
		return
	}
	if err := d.repos.Streams.SetStatus(ctx, s.ID, stream.StatusReady, ""); err != nil {
		d.log.Error("subresource ensure: set stream status", "stream", s.ID, "err", err)
	}
}

func (d *DataServices) probeStream(ctx context.Context, ns capability.NamespaceRef, host, password string, tool adminTool, face pubsubtemplate.StreamAdminFace, name string) (pubsubtemplate.StreamLive, error) {
	spec, err := face.InspectStream(host, password, name)
	if err != nil {
		return pubsubtemplate.StreamLive{}, err
	}
	stdout, stderr, runErr := d.runAdminCapture(ctx, "stream-inspect-"+sanitizeSubID(name), ns, tool, spec)
	return face.ParseStreamInspect(stdout, stderr, runErr)
}

// —— Bucket ensure / GC ——

func (d *DataServices) ensureBucket(ctx context.Context, ns capability.NamespaceRef, host, accessKey, secretKey string, tool adminTool, face bucketstoretemplate.BucketAdminFace, b *bucketresource.Bucket) bool {
	live, err := d.probeBucket(ctx, ns, host, accessKey, secretKey, tool, face, b.Name)
	if err != nil {
		d.subBucketFail(ctx, b, "inspect: %v", err)
		return false
	}
	if !live.Exists {
		spec, err := face.CreateBucket(host, accessKey, secretKey, b.Name)
		if err != nil {
			d.subBucketFail(ctx, b, "render create: %v", err)
			return false
		}
		if err := d.runAdminUtility(ctx, "bucket-create-"+b.ID, ns, tool, spec); err != nil {
			d.subBucketFail(ctx, b, "create: %v", err)
			return false
		}
	}
	// 配额对照：行 nil = 无配额（live 有配额 → clear；反之 set——quota 是
	// 可变面全管理，与 topic retention 的「nil=不管理」不同：S3 配额没有
	// 引擎缺省语义，行的 nil 就是期望态的无配额）。
	live, err = d.probeBucket(ctx, ns, host, accessKey, secretKey, tool, face, b.Name)
	if err != nil {
		d.subBucketFail(ctx, b, "re-inspect: %v", err)
		return false
	}
	if !quotaMatch(b.QuotaBytes, live.QuotaBytes) {
		spec, err := face.SetBucketQuota(host, accessKey, secretKey, b.Name, b.QuotaBytes)
		if err != nil {
			d.subBucketFail(ctx, b, "render quota: %v", err)
			return false
		}
		if err := d.runAdminUtility(ctx, "bucket-quota-"+b.ID, ns, tool, spec); err != nil {
			d.subBucketFail(ctx, b, "quota: %v", err)
			return false
		}
		live, err = d.probeBucket(ctx, ns, host, accessKey, secretKey, tool, face, b.Name)
		if err != nil {
			d.subBucketFail(ctx, b, "post-quota inspect: %v", err)
			return false
		}
	}
	if live.Exists && quotaMatch(b.QuotaBytes, live.QuotaBytes) {
		d.subBucketReady(ctx, b)
		return true
	}
	d.subBucketFail(ctx, b, "post-change inspect does not match the desired state")
	return false
}

func (d *DataServices) gcBucket(ctx context.Context, ns capability.NamespaceRef, host, accessKey, secretKey string, tool adminTool, face bucketstoretemplate.BucketAdminFace, b *bucketresource.Bucket) bool {
	live, err := d.probeBucket(ctx, ns, host, accessKey, secretKey, tool, face, b.Name)
	if err == nil && !live.Exists {
		return d.hardDeleteBucketRow(ctx, b)
	}
	spec, err := face.DeleteBucket(host, accessKey, secretKey, b.Name)
	if err != nil {
		d.log.Error("subresource gc: render bucket delete", "bucket", b.ID, "err", err)
		return false
	}
	if err := d.runAdminUtility(ctx, "bucket-gc-"+b.ID, ns, tool, spec); err != nil {
		d.log.Error("subresource gc: delete bucket", "bucket", b.ID, "err", err)
		return false
	}
	return d.hardDeleteBucketRow(ctx, b)
}

func (d *DataServices) hardDeleteBucketRow(ctx context.Context, b *bucketresource.Bucket) bool {
	if err := d.repos.Buckets.Delete(ctx, b.ID); err != nil {
		d.log.Error("subresource gc: delete bucket row", "bucket", b.ID, "err", err)
		return false
	}
	d.log.Info("subresource gc: bucket removed", "bucket", b.ID, "name", b.Name, "bucket_store", b.BucketStoreID)
	return true
}

func (d *DataServices) subBucketFail(ctx context.Context, b *bucketresource.Bucket, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	d.log.Error("subresource ensure: bucket", "bucket", b.ID, "bucket_store", b.BucketStoreID, "err", msg)
	if err := d.repos.Buckets.SetStatus(ctx, b.ID, bucketresource.StatusError, truncateSubError(msg)); err != nil {
		d.log.Error("subresource ensure: set bucket status", "bucket", b.ID, "err", err)
	}
}

func (d *DataServices) subBucketReady(ctx context.Context, b *bucketresource.Bucket) {
	if b.Status == bucketresource.StatusReady && b.StatusErr == "" {
		return
	}
	if err := d.repos.Buckets.SetStatus(ctx, b.ID, bucketresource.StatusReady, ""); err != nil {
		d.log.Error("subresource ensure: set bucket status", "bucket", b.ID, "err", err)
	}
}

func (d *DataServices) probeBucket(ctx context.Context, ns capability.NamespaceRef, host, accessKey, secretKey string, tool adminTool, face bucketstoretemplate.BucketAdminFace, name string) (bucketstoretemplate.BucketLive, error) {
	spec, err := face.InspectBucket(host, accessKey, secretKey, name)
	if err != nil {
		return bucketstoretemplate.BucketLive{}, err
	}
	stdout, stderr, runErr := d.runAdminCapture(ctx, "bucket-inspect-"+sanitizeSubID(name), ns, tool, spec)
	return face.ParseBucketInspect(stdout, stderr, runErr)
}

// —— 删除披露的实况探测（受理面 PreviewDelete 消费；engine 门面委托） ——

// TopicLiveInspect 探测单 Topic 实况（删除披露的数据量行——kafka CLI 无
// per-topic 数据量面，披露行只有 partitions 实况）。
func (d *DataServices) TopicLiveInspect(ctx context.Context, pubsubID, topicID string) (pubsubtemplate.TopicLive, error) {
	row, err := d.repos.PubSubs.Get(ctx, pubsubID)
	if err != nil {
		return pubsubtemplate.TopicLive{}, err
	}
	t, err := d.repos.Topics.Get(ctx, topicID)
	if err != nil {
		return pubsubtemplate.TopicLive{}, err
	}
	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
	if err != nil {
		return pubsubtemplate.TopicLive{}, err
	}
	face := pubsubtemplate.TopicAdminFaceOf(tpl)
	if face == nil {
		return pubsubtemplate.TopicLive{}, fmt.Errorf("engine %q carries no topic admin face", row.Engine)
	}
	password, err := d.pubsubPassword(ctx, row)
	if err != nil {
		return pubsubtemplate.TopicLive{}, err
	}
	ns := capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name}
	return d.probeTopic(ctx, ns, contract.PubSubDNSName(row.ID), password, adminTool{image: tpl.Image(), user: tpl.ToolUser()}, face, t.Name)
}

// StreamLiveInspect 探测单 Stream 实况（state.messages/bytes = 删除披露的
// 将删数据量）。
func (d *DataServices) StreamLiveInspect(ctx context.Context, pubsubID, streamID string) (pubsubtemplate.StreamLive, error) {
	row, err := d.repos.PubSubs.Get(ctx, pubsubID)
	if err != nil {
		return pubsubtemplate.StreamLive{}, err
	}
	s, err := d.repos.Streams.Get(ctx, streamID)
	if err != nil {
		return pubsubtemplate.StreamLive{}, err
	}
	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
	if err != nil {
		return pubsubtemplate.StreamLive{}, err
	}
	face := pubsubtemplate.StreamAdminFaceOf(tpl)
	if face == nil {
		return pubsubtemplate.StreamLive{}, fmt.Errorf("engine %q carries no stream admin face", row.Engine)
	}
	password, err := d.pubsubPassword(ctx, row)
	if err != nil {
		return pubsubtemplate.StreamLive{}, err
	}
	ns := capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, PubSub: row.ID, PubSubName: row.Name}
	return d.probeStream(ctx, ns, contract.PubSubDNSName(row.ID), password, adminTool{image: pubsubtemplate.NatsBoxImage()}, face, s.Name)
}

// BucketLiveInspect 探测单 Bucket 实况（quota + du 数据量——删除披露）。
func (d *DataServices) BucketLiveInspect(ctx context.Context, bucketStoreID, bucketID string) (bucketstoretemplate.BucketLive, error) {
	row, err := d.repos.BucketStores.Get(ctx, bucketStoreID)
	if err != nil {
		return bucketstoretemplate.BucketLive{}, err
	}
	b, err := d.repos.Buckets.Get(ctx, bucketID)
	if err != nil {
		return bucketstoretemplate.BucketLive{}, err
	}
	tpl, err := bucketstoretemplate.For(row.Engine, row.Version)
	if err != nil {
		return bucketstoretemplate.BucketLive{}, err
	}
	face := bucketstoretemplate.BucketAdminFaceOf(tpl)
	if face == nil {
		return bucketstoretemplate.BucketLive{}, fmt.Errorf("engine %q carries no bucket admin face", row.Engine)
	}
	accessKey, secretKey, err := d.bucketStoreCredentials(ctx, row)
	if err != nil {
		return bucketstoretemplate.BucketLive{}, err
	}
	ns := capability.NamespaceRef{Team: d.projectTeamOf(row.ProjectID), Project: row.ProjectID, BucketStore: row.ID, BucketStoreName: row.Name}
	host := contract.BucketStoreDNSName(row.ID)
	live, err := d.probeBucket(ctx, ns, host, accessKey, secretKey, adminTool{image: tpl.Image(), user: tpl.ToolUser()}, face, b.Name)
	if err != nil || !live.Exists {
		return live, err
	}
	usageSpec, err := face.InspectBucketUsage(host, accessKey, secretKey, b.Name)
	if err != nil {
		return live, nil // du 失败不阻断披露——quota 探测已足（数据量缺席如实）
	}
	stdout, stderr, runErr := d.runAdminCapture(ctx, "bucket-du-"+sanitizeSubID(b.Name), ns, adminTool{image: tpl.Image(), user: tpl.ToolUser()}, usageSpec)
	usage, uerr := face.ParseBucketUsage(stdout, stderr, runErr)
	if uerr == nil && usage.Exists {
		live.Bytes, live.Objects = usage.Bytes, usage.Objects
	}
	return live, nil
}

// bucketStoreCredentials 解父实例 root AK/SK（连接材料回读）。
func (d *DataServices) bucketStoreCredentials(ctx context.Context, row *bucketstore.BucketStore) (string, string, error) {
	if d.cipher == nil {
		return "", "", fmt.Errorf("bucket store credentials require the master key (data root keys/ missing)")
	}
	sec, err := d.repos.Secrets.GetByName(ctx, row.ProjectID, row.CredentialsRef)
	if err != nil {
		return "", "", fmt.Errorf("lookup credential secret %q: %w", row.CredentialsRef, err)
	}
	plain, err := d.cipher.Open(sec.Ciphertext)
	if err != nil {
		return "", "", fmt.Errorf("decrypt credential secret %q: %w", row.CredentialsRef, err)
	}
	material, err := bucketstoretemplate.ParseConnMaterial(string(plain))
	if err != nil {
		return "", "", fmt.Errorf("credential secret %q: %w", row.CredentialsRef, err)
	}
	return material.AccessKey, material.SecretKey, nil
}

// —— 工具容器执行面 ——

// adminTool 是子资源收敛/实况探测的工具容器载体形态（镜像 + 运行用户
// ——模板 ToolUser 声明面，B8-fix：kafka/redpanda 的 admin 面以 root 跑，
// 非 root 镜像用户读不到 0600 材料；nats-box/silo 等镜像自带用户的工具
// user 空 = 镜像缺省，零行为变化）。
type adminTool struct {
	image string
	user  string
}

// runAdminCapture 执行 admin 探测命令并捕获原始输出（maintenance 读半边 +
// 带界——runUtilityContainer 同纪律；探测需要 stdout 原文进方言解析器）。
func (d *DataServices) runAdminCapture(ctx context.Context, id string, ns capability.NamespaceRef, tool adminTool, spec pubsubtemplate.AdminSpec) (string, string, error) {
	if d.utility == nil {
		return "", "", fmt.Errorf("the utility face is not assembled (data service tools unavailable)")
	}
	unlockMaintenance := d.lockMaintenance()
	defer unlockMaintenance()
	execCtx, cancel := d.boundedStep(ctx)
	defer cancel()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	req := capability.UtilityRequest{
		ID: id, Namespace: ns, Image: tool.image, User: tool.user,
		Argv: spec.Argv, Env: spec.Env, SecretFiles: spec.SecretFiles,
		Networks: d.projectNetworkFactsNamesOnly(ctx, ns.Project),
	}
	runErr := d.utility.RunUtility(execCtx, req, stdout, stderr)
	return stdout.String(), stderr.String(), runErr
}

// runAdminUtility 执行一条 admin 变更命令（ensure/GC 的执行半边；Env/材料
// 从 spec 组装——与 runAdminCapture 同锁纪律但走 runUtilityContainer 的
// 取证包装）。spec.Env 由调用方并入（探测与变更共用锁协议）。
func (d *DataServices) runAdminUtility(ctx context.Context, id string, ns capability.NamespaceRef, tool adminTool, spec pubsubtemplate.AdminSpec) error {
	req := capability.UtilityRequest{
		ID: id, Namespace: ns, Image: tool.image, User: tool.user,
		Argv: spec.Argv, Env: spec.Env, SecretFiles: spec.SecretFiles,
		Networks: d.projectNetworkFactsNamesOnly(ctx, ns.Project),
	}
	return d.runUtilityContainer(ctx, req)
}

// —— 对照帮手 ——

// retentionMatch：行 nil = 不管理（恒真）；行设值则 live 须同值显式在场。
func retentionMatch(want *int64, live *int64) bool {
	if want == nil {
		return true
	}
	return live != nil && *live == *want
}

// maxBytesMatch：行 nil = 无上限（live 未设即配）；行设值则 live 须同值。
func maxBytesMatch(want *int64, live int64, liveSet bool) bool {
	if want == nil {
		return !liveSet
	}
	return liveSet && live == *want
}

// quotaMatch：行 nil = 无配额（live 无配额即配）；行设值则 live 须同值。
func quotaMatch(want *int64, live *int64) bool {
	if want == nil {
		return live == nil
	}
	return live != nil && *live == *want
}

// subjectsEqual：排序后逐项相等（nats 语义序无关）。
func subjectsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// pubsubSubSig 是 pubsub 实例子资源行集的期望态摘要（活跃行配置 + tombstone
// 名单——增删改任何一项都失配）。
func pubsubSubSig(topics []topic.Topic, streams []stream.Stream) string {
	var b strings.Builder
	for i := range topics {
		t := &topics[i]
		retention := "-"
		if t.RetentionMS != nil {
			retention = strconv.FormatInt(*t.RetentionMS, 10)
		}
		fmt.Fprintf(&b, "t\x00%s\x00%d\x00%s\x00%t\x00", t.Name, t.Partitions, retention, t.DeletedAt != "")
	}
	for i := range streams {
		s := &streams[i]
		max := "-"
		if s.MaxBytes != nil {
			max = strconv.FormatInt(*s.MaxBytes, 10)
		}
		fmt.Fprintf(&b, "s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t\x00", s.Name, strings.Join(s.Subjects, ","), s.Retention, s.Storage, max, s.DeletedAt != "")
	}
	return b.String()
}

// bucketSubSig 是 bucket store 实例子资源行集的期望态摘要。
func bucketSubSig(buckets []bucketresource.Bucket) string {
	var b strings.Builder
	for i := range buckets {
		k := &buckets[i]
		quota := "-"
		if k.QuotaBytes != nil {
			quota = strconv.FormatInt(*k.QuotaBytes, 10)
		}
		fmt.Fprintf(&b, "b\x00%s\x00%s\x00%t\x00", k.Name, quota, k.DeletedAt != "")
	}
	return b.String()
}

// truncateSubError 钳制行上错误留痕的长度（取证尾部；行面不承载长报文）。
func truncateSubError(msg string) string {
	if len(msg) > 500 {
		return msg[:500]
	}
	return msg
}

// sanitizeSubID 把子资源名压到工具容器 ID 安全集（载体名段）。
func sanitizeSubID(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
