package fleetlygrpc

// Topic 子资源管理面（B8，ADR-0096 决策 10 / design §4.7）：PubSubsService
// 内增 RPC（词汇冻结——不立新 Service：Topic 属 pubsub 家族，engine 决定
// 可用性）。受理链：父实例在册 + engine 方言可用性（kafka 协议族 → Topic；
// nats → 受理拒文案指向 Stream 面）+ per 实例配额（topics 100）+ 配置校验
// （方言层：名字值域/分区只增不减）。收敛语义：行即期望态，dataservices
// 卫星 pubsub 分族步尾段 ensure（无载体滚动）；删除 = 声明式 GC（wide 档
// 确认——删除披露行经 PreviewDeleteTopic）。

import (
	"context"
	"fmt"
	"sort"
	"strconv"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/data/topic"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// topic 面事件名（usage 反扫的字面量锚点；ADR-0096 决策 10 三件）。
const (
	eventTopicCreated = "topic.created"
	eventTopicUpdated = "topic.updated"
	eventTopicDeleted = "topic.deleted"
)

// previewVerbTopicUpdate / previewVerbTopicDelete 是票面兑换动词锚（票面
// Verb 与 ConfirmableVerbs 键同串）。
const (
	previewVerbTopicUpdate = "/fleetly.structure.v1.PubSubsService/UpdateTopic"
	previewVerbTopicDelete = "/fleetly.structure.v1.PubSubsService/DeleteTopic"
)

// subConvergedNote 是子资源 Update 回执的收敛语义披露（三子资源共用——
// 行已落、收敛段接棒、无载体滚动）。
const subConvergedNote = "desired state saved; the sub-resource convergence segment ensures the live object within its next pass (watch status via the get face until ready)"

// topicMaxPartitions 是分区上界（受理位钳——工具容器面有界；kafka 单机
// 主题分区上限常识量级 10000）。
const topicMaxPartitions = 10000

// admitTopicParent 校验父实例与 Topic 面可用性（Create/Update/Delete 共用
// 的受理前半：行在册 + 授权 + engine 方言可用性）。
func (svc *PubSubsService) admitTopicParent(ctx context.Context, pubsubID string) (*pubsub.PubSub, pubtemplate.TopicAdminFace, error) {
	row, err := svc.s.PubSubs.Get(ctx, pubsubID)
	if err != nil {
		return nil, nil, mapStateError(err, "pubsub")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, nil, err
	}
	tpl, err := pubtemplate.For(row.Engine, row.Version)
	if err != nil {
		return nil, nil, apperr.New("E_INVALID_ARGUMENT", "engine %q left the template registry: %s", row.Engine, err)
	}
	face := pubtemplate.TopicAdminFaceOf(tpl)
	if face == nil {
		// 引擎方言可用性缺席 = 能力面关态（G17：501 族——E_BROWSER_UNSUPPORTED
		// 「引擎无映射」同款先例），非调用方参数错。
		return nil, nil, apperr.New("E_RUNTIME_FACE_UNSUPPORTED",
			"pubsub %s runs engine %q which manages streams, not topics — the topic face belongs to kafka-protocol engines (kafka, redpanda); use the stream subresource face for this instance", row.Name, row.Engine)
	}
	return row, face, nil
}

// CreateTopic：受理（名字值域/分区范围/retention 非负）→ commit（父存活 +
// per 实例配额 + 行写 + 事件）→ Kick。
func (svc *PubSubsService) CreateTopic(ctx context.Context, req *structurev1.CreateTopicRequest) (*structurev1.CreateTopicResponse, error) {
	parent, face, err := svc.admitTopicParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	if err := pubtemplate.ValidateTopicName(req.GetName()); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "name: %s", err)
	}
	partitions := req.GetPartitions()
	if partitions == 0 {
		partitions = 1
	}
	if partitions < 1 || partitions > topicMaxPartitions {
		return nil, apperr.New("E_INVALID_ARGUMENT", "partitions: %d is outside 1..%d", req.GetPartitions(), topicMaxPartitions)
	}
	if req.GetRetentionMs() < 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "retention_ms: %d is negative (0 = unmanaged, engine default)", req.GetRetentionMs())
	}
	var retention *int64
	if req.GetRetentionMs() > 0 {
		retention = &req.RetentionMs
	}
	_ = face // 可用性门已过（受理校验集中在受理位；渲染在方言层）
	row := &topic.Topic{
		ProjectID: parent.ProjectID, PubSubID: parent.ID, Name: req.GetName(),
		Partitions: partitions, RetentionMS: retention,
	}
	fact, err := svc.s.factOf(ctx, parent.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventTopicCreated, Aggregate: "topic", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "topic.create", Resource: "topic/" + row.ID,
			AfterFP: row.Name + " p=" + strconv.FormatInt(int64(row.Partitions), 10),
			Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Checks: []acceptanceCheck{
			svc.s.parentProjectAlive(parent.ProjectID),
			func(ctx context.Context, st transact.Stores) error {
				n, err := st.Topics.CountByPubSub(ctx, parent.ID)
				if err != nil {
					return err
				}
				if n >= topic.QuotaPerInstance {
					return apperr.New("E_QUOTA_EXCEEDED",
						"pubsub %s already holds %d topics (limit %d per instance); delete unused topics first",
						parent.Name, n, topic.QuotaPerInstance)
				}
				return nil
			},
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Topics.Create(ctx, row)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "topic")
	}
	svc.s.Engine.KickPubSubs()
	return &structurev1.CreateTopicResponse{Topic: topicMsg(row)}, nil
}

func (svc *PubSubsService) GetTopic(ctx context.Context, req *structurev1.GetTopicRequest) (*structurev1.GetTopicResponse, error) {
	parent, _, err := svc.admitTopicParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	row, err := svc.s.Topics.Get(ctx, req.GetTopicId())
	if err != nil || row.PubSubID != parent.ID {
		return nil, mapStateError(state.ErrNotFound, "topic")
	}
	return &structurev1.GetTopicResponse{Topic: topicMsg(row)}, nil
}

// ListTopics 旧→新分页（子资源面行序 = 创建序）。
func (svc *PubSubsService) ListTopics(ctx context.Context, req *structurev1.ListTopicsRequest) (*structurev1.ListTopicsResponse, error) {
	parent, _, err := svc.admitTopicParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	rows, err := svc.s.Topics.ListByPubSub(ctx, parent.ID)
	if err != nil {
		return nil, mapStateError(err, "topic")
	}
	// 活跃行回显（tombstone 行是 GC 挂起面——用户面不可见）；升序 seq + 游标。
	out := &structurev1.ListTopicsResponse{}
	for i := range rows {
		r := &rows[i]
		if r.Deleted() || r.Seq <= req.GetAfterTopicSeq() {
			continue
		}
		out.Topics = append(out.Topics, topicMsg(r))
		if req.GetLimit() > 0 && len(out.Topics) >= int(req.GetLimit()) {
			break
		}
	}
	return out, nil
}

// topicUpdateAdmission 是 Update/Preview 共用的受理视图。
type topicUpdateAdmission struct {
	parent   *pubsub.PubSub
	row      *topic.Topic
	face     pubtemplate.TopicAdminFace
	nextFrom *topic.Topic // 合并后的完整期望态（from = 行现值）
	digestOf func() string
}

// admitTopicUpdate：不可变锚校验 + 期望态合并 + 方言约束（分区只增不减）
// + digest 铸造。
func (svc *PubSubsService) admitTopicUpdate(ctx context.Context, req *structurev1.UpdateTopicRequest) (*topicUpdateAdmission, error) {
	parent, face, err := svc.admitTopicParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	row, err := svc.s.Topics.Get(ctx, req.GetTopicId())
	if err != nil || row.PubSubID != parent.ID {
		return nil, mapStateError(state.ErrNotFound, "topic")
	}
	if req.Partitions == nil && req.RetentionMs == nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "nothing to update: pass partitions and/or retention_ms")
	}
	next := *row
	next.RetentionMS = row.RetentionMS
	if req.Partitions != nil {
		p := req.GetPartitions()
		if p < 1 || p > topicMaxPartitions {
			return nil, apperr.New("E_INVALID_ARGUMENT", "partitions: %d is outside 1..%d", p, topicMaxPartitions)
		}
		// 引擎约束（方言层受理半边）：分区只增不减。
		if err := face.ValidateTopicPartitionsChange(row.Partitions, p); err != nil {
			return nil, apperr.New("E_INVALID_ARGUMENT", "partitions: %s", err)
		}
		next.Partitions = p
	}
	if req.RetentionMs != nil {
		if req.GetRetentionMs() < 0 {
			return nil, apperr.New("E_INVALID_ARGUMENT", "retention_ms: negative values are invalid (0 = unmanaged, engine default)")
		}
		v := req.GetRetentionMs()
		if v == 0 {
			next.RetentionMS = nil
		} else {
			next.RetentionMS = &v
		}
	}
	adm := &topicUpdateAdmission{parent: parent, row: row, face: face, nextFrom: &next}
	adm.digestOf = func() string { return topicDesiredDigest(row.ID, &next) }
	return adm, nil
}

// UpdateTopic：受理 → 票面内容绑定（peek）→ commit（行写 + 事件）→ 烧票
// → Kick。
func (svc *PubSubsService) UpdateTopic(ctx context.Context, req *structurev1.UpdateTopicRequest) (*structurev1.UpdateTopicResponse, error) {
	adm, err := svc.admitTopicUpdate(ctx, req)
	if err != nil {
		return nil, err
	}
	// skip 通道无票面绑定物；票面通道锁外 peek（子资源行写自身 tx 原子——
	// 家族面在 utilityMu 锁内验票的结构性差异，见 pubsub_update.go 注）。
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		if err := bindSubResourceTicket(svc.s, ticket, previewVerbTopicUpdate, adm.row.ID, adm.digestOf()); err != nil {
			return nil, err
		}
	}
	fact, err := svc.s.factOf(ctx, adm.parent.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventTopicUpdated, Aggregate: "topic", ID: adm.row.ID,
			Payload: mustJSON(structureEventPayload{ID: adm.row.ID, ProjectID: adm.parent.ProjectID}),
			TeamID:  team,
			Action:  "topic.update", Resource: "topic/" + adm.row.ID,
			Detail:  string(mustJSON(map[string]any{"partitions": adm.nextFrom.Partitions, "retention_ms": derefInt64Value(adm.nextFrom.RetentionMS)})),
			AfterFP: adm.row.Name,
			Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Topics.UpdateDesired(ctx, adm.row.ID, adm.nextFrom.Partitions, adm.nextFrom.RetentionMS)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "topic")
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		svc.s.ConfirmTickets().DiscardConfirm(ticket)
	}
	svc.s.Engine.KickPubSubs()
	updated, err := svc.s.Topics.Get(ctx, adm.row.ID)
	if err != nil {
		return nil, mapStateError(err, "topic")
	}
	return &structurev1.UpdateTopicResponse{Topic: topicMsg(updated), ConvergedNote: subConvergedNote}, nil
}

// PreviewUpdateTopic：只读预演 + 铸票（mint 族）。
func (svc *PubSubsService) PreviewUpdateTopic(ctx context.Context, req *structurev1.PreviewUpdateTopicRequest) (*structurev1.PreviewUpdateTopicResponse, error) {
	update := req.GetUpdate()
	if update == nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "update: must not be empty (the preview carries the same update request)")
	}
	adm, err := svc.admitTopicUpdate(ctx, update)
	if err != nil {
		return nil, err
	}
	out := &structurev1.PreviewUpdateTopicResponse{}
	if adm.nextFrom.Partitions != adm.row.Partitions {
		out.Changes = append(out.Changes, &structurev1.SubResourceChange{
			Key: "partitions", From: strconv.FormatInt(int64(adm.row.Partitions), 10), To: strconv.FormatInt(int64(adm.nextFrom.Partitions), 10)})
	}
	if !retentionEqual(adm.row.RetentionMS, adm.nextFrom.RetentionMS) {
		out.Changes = append(out.Changes, &structurev1.SubResourceChange{
			Key: "retention_ms", From: retentionWord(adm.row.RetentionMS), To: retentionWord(adm.nextFrom.RetentionMS)})
	}
	sort.Slice(out.Changes, func(i, j int) bool { return out.Changes[i].GetKey() < out.Changes[j].GetKey() })
	diffLines := make([]string, 0, len(out.Changes))
	for _, ch := range out.Changes {
		diffLines = append(diffLines, fmt.Sprintf("%s:%s=%s", ch.GetKey(), ch.GetFrom(), ch.GetTo()))
	}
	ticket, ttl, err := svc.s.MintConfirmTicket(governance.ConfirmTicketPayload{
		Verb:          previewVerbTopicUpdate,
		TargetApp:     adm.row.ID,
		RequestDigest: adm.digestOf(),
		PreviewDigest: sha256Hex([]byte(joinSubLines(diffLines))),
		Operator:      confirmOperator(ctx),
	})
	if err != nil {
		return nil, mapStateError(err, "ticket")
	}
	out.Ticket = ticket
	//nolint:gosec // TTL 是 600s 常量（confirmTicketTTL），int32 安全。
	out.TicketExpiresIn = int32(ttl.Seconds())
	return out, nil
}

// DeleteTopic：行 tombstone + 事件（GC 由收敛段接棒——Topic 删除即数据
// 删除，wide 票已在受理位确认过删除披露）。
func (svc *PubSubsService) DeleteTopic(ctx context.Context, req *structurev1.DeleteTopicRequest) (*structurev1.DeleteTopicResponse, error) {
	parent, _, err := svc.admitTopicParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	row, err := svc.s.Topics.Get(ctx, req.GetTopicId())
	if err != nil || row.PubSubID != parent.ID {
		return nil, mapStateError(state.ErrNotFound, "topic")
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		if err := bindSubResourceTicket(svc.s, ticket, previewVerbTopicDelete, row.ID, topicDeleteDigest(row)); err != nil {
			return nil, err
		}
	}
	fact, err := svc.s.factOf(ctx, parent.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventTopicDeleted, Aggregate: "topic", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: parent.ProjectID}),
			TeamID:  team,
			Action:  "topic.delete", Resource: "topic/" + row.ID,
			BeforeFP: row.Name + " p=" + strconv.FormatInt(int64(row.Partitions), 10),
			Actor:    authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Topics.SoftDelete(ctx, row.ID)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "topic")
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		svc.s.ConfirmTickets().DiscardConfirm(ticket)
	}
	svc.s.Engine.KickPubSubs()
	return &structurev1.DeleteTopicResponse{}, nil
}

// PreviewDeleteTopic：删除披露行（live 实况 + 不可恢复披露）+ 铸票（wide）。
// kafka CLI 无 per-topic 数据量面——data_bytes/data_messages 恒 0，披露文案
// 如实（TopicDataUnavailableNote）。
func (svc *PubSubsService) PreviewDeleteTopic(ctx context.Context, req *structurev1.PreviewDeleteTopicRequest) (*structurev1.PreviewDeleteTopicResponse, error) {
	parent, _, err := svc.admitTopicParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	row, err := svc.s.Topics.Get(ctx, req.GetTopicId())
	if err != nil || row.PubSubID != parent.ID {
		return nil, mapStateError(state.ErrNotFound, "topic")
	}
	var dataBytes, dataMessages int64
	disclosure := fmt.Sprintf(
		"Deleting topic %s on pubsub %s destroys the partitioned data on delete — this is unrecoverable. Live partitions: %d. %s.",
		row.Name, parent.Name, row.Partitions, pubtemplate.TopicDataUnavailableNote)
	if parent.Status == pubsub.StatusRunning {
		if live, lerr := svc.s.Engine.TopicLiveInspect(ctx, parent.ID, row.ID); lerr == nil && live.Exists {
			disclosure = fmt.Sprintf(
				"Deleting topic %s on pubsub %s destroys the partitioned data on delete — this is unrecoverable. Live partitions: %d. %s.",
				row.Name, parent.Name, live.Partitions, pubtemplate.TopicDataUnavailableNote)
		}
	}
	ticket, ttl, err := svc.s.MintConfirmTicket(governance.ConfirmTicketPayload{
		Verb:          previewVerbTopicDelete,
		TargetApp:     row.ID,
		RequestDigest: topicDeleteDigest(row),
		PreviewDigest: sha256Hex([]byte(disclosure)),
		Operator:      confirmOperator(ctx),
	})
	if err != nil {
		return nil, mapStateError(err, "ticket")
	}
	//nolint:gosec // TTL 是 600s 常量，int32 安全。
	return &structurev1.PreviewDeleteTopicResponse{
		Topic: topicMsg(row), DataBytes: dataBytes, DataMessages: dataMessages,
		Disclosure: disclosure, Ticket: ticket, TicketExpiresIn: int32(ttl.Seconds()),
	}, nil
}

// topicDesiredDigest 是 Update 票面 RequestDigest 单源（前缀 topic=——族
// 前缀分立；预览铸票与执行面同参数调用，漂移即 MISMATCH）。
func topicDesiredDigest(topicID string, next *topic.Topic) string {
	retention := "0"
	if next.RetentionMS != nil {
		retention = strconv.FormatInt(*next.RetentionMS, 10)
	}
	return sha256Hex([]byte("topic=" + topicID + "\npartitions=" + strconv.FormatInt(int64(next.Partitions), 10) + "\nretention_ms=" + retention + "\n"))
}

// topicDeleteDigest 是 Delete 票面摘要（行身份 + 名字——同名重建的新行是
// 新 id，票不可跨行复用）。
func topicDeleteDigest(row *topic.Topic) string {
	return sha256Hex([]byte("topic-del=" + row.ID + "\nname=" + row.Name + "\n"))
}

// topicMsg 行 → proto 映射。
func topicMsg(row *topic.Topic) *structurev1.Topic {
	msg := &structurev1.Topic{
		Id: row.ID, Seq: row.Seq, ProjectId: row.ProjectID, PubsubId: row.PubSubID, Name: row.Name,
		Partitions: row.Partitions, Status: row.Status, StatusError: row.StatusError,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.RetentionMS != nil {
		msg.RetentionMs = *row.RetentionMS
	}
	return msg
}

// retentionEqual / retentionWord / derefInt64Value / joinSubLines /
// bindSubResourceTicket 是子资源三面共用的小帮手（stream/bucket 面同义消费）。
func retentionEqual(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func retentionWord(v *int64) string {
	if v == nil {
		return "unmanaged"
	}
	return strconv.FormatInt(*v, 10)
}

func derefInt64Value(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func joinSubLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}

// bindSubResourceTicket 是子资源写动词的票面内容绑定（Verb/Target/digest
// 三比对；烧票由调用方在 commit 成功后 DiscardConfirm——先变更后烧票，
// 同库并发同票的第二请求 peek 即见已烧前的命中也可能双过，但 commit 行写
// 幂等收敛〔UpdateDesired 全量替换 / SoftDelete 幂等〕，双兑无破坏面）。
func bindSubResourceTicket(s *Services, ticket, verb, targetID, digest string) error {
	payload, status := s.ConfirmTickets().PeekConfirm(ticket)
	switch status {
	case governance.ConfirmPeekHit:
	case governance.ConfirmPeekExpired:
		return apperr.New("E_CONFIRMATION_STALE",
			"the confirmation ticket has expired").
			WithSuggestion("Run the preview again and confirm the fresh ticket.")
	case governance.ConfirmPeekConsumed:
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket was already redeemed; check the sub-resource state instead of resending the ticket").
			WithSuggestion("The ticket is single-use. Query the resulting state; mint a fresh preview ticket for a new change.")
	default:
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket is not valid for this operation (unknown or already redeemed)").
			WithSuggestion("Mint a fresh ticket from the matching preview RPC; tickets are single-purpose and bound to the previewing operator.")
	}
	if payload.Verb != verb || payload.TargetApp != targetID {
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket was minted for a different operation or target").
			WithSuggestion("Mint a fresh preview ticket for this sub-resource.")
	}
	if payload.RequestDigest != digest {
		return apperr.New("E_CONFIRMATION_MISMATCH",
			"the confirmation ticket does not match the current desired state").
			WithSuggestion("The sub-resource row moved since the preview. Run the preview again and confirm the fresh ticket.")
	}
	return nil
}
