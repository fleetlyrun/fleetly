package fleetlygrpc

// Stream 子资源管理面（B8，ADR-0096 决策 10 / design §4.7）：PubSubsService
// 内增 RPC（Stream 属 pubsub 类型，nats 引擎专属面）。受理链与 Topic 面同构
// （pubsub_topics.go 注）：父在册 + engine 方言可用性（nats → Stream；kafka
// 协议族受理拒文案指向 Topic 面）+ per 实例配额（streams 100）+ 配置校验
// （名字值域/retention·storage 值域——两轴创建后不可变，Update 不设载）。
// 删除 = 声明式 GC（wide 档——删除披露行列出流内消息数与字节量）。

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/pubsub"
	"github.com/fleetlyrun/fleetly/internal/domain/data/stream"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubsubtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
)

// stream 面事件名（usage 反扫的字面量锚点）。
const (
	eventStreamCreated = "stream.created"
	eventStreamUpdated = "stream.updated"
	eventStreamDeleted = "stream.deleted"
)

// 票面兑换动词锚。
const (
	previewVerbStreamUpdate = "/fleetly.structure.v1.PubSubsService/UpdateStream"
	previewVerbStreamDelete = "/fleetly.structure.v1.PubSubsService/DeleteStream"
)

// admitStreamParent 校验父实例与 Stream 面可用性（nats 专属）。
func (svc *PubSubsService) admitStreamParent(ctx context.Context, pubsubID string) (*pubsub.PubSub, pubsubtemplate.StreamAdminFace, error) {
	row, err := svc.s.PubSubs.Get(ctx, pubsubID)
	if err != nil {
		return nil, nil, mapStateError(err, "pubsub")
	}
	if err := svc.s.authorizeProjectID(ctx, row.ProjectID); err != nil {
		return nil, nil, err
	}
	tpl, err := pubsubtemplate.For(row.Engine, row.Version)
	if err != nil {
		return nil, nil, apperr.New("E_INVALID_ARGUMENT", "engine %q left the template registry: %s", row.Engine, err)
	}
	face := pubsubtemplate.StreamAdminFaceOf(tpl)
	if face == nil {
		// 引擎方言可用性缺席 = 能力面关态（G17：501 族——E_BROWSER_UNSUPPORTED
		// 同款先例）。
		return nil, nil, apperr.New("E_RUNTIME_FACE_UNSUPPORTED",
			"pubsub %s runs engine %q which manages topics, not streams — the stream face belongs to nats; use the topic subresource face for this instance", row.Name, row.Engine)
	}
	return row, face, nil
}

// validateStreamAxes 校验 retention/storage 值域（创建受理位）。
func validateStreamAxes(retention, storage string) error {
	switch retention {
	case "", stream.RetentionLimits, stream.RetentionInterest, stream.RetentionWorkqueue:
	default:
		return apperr.New("E_INVALID_ARGUMENT", "retention: %q is not one of limits | interest | workqueue", retention)
	}
	switch storage {
	case "", stream.StorageFile, stream.StorageMemory:
	default:
		return apperr.New("E_INVALID_ARGUMENT", "storage: %q is not one of file | memory", storage)
	}
	return nil
}

// normalizeSubjects 规范化主题过滤器集（去空、去重、升序——确定性序列化）。
func normalizeSubjects(subjects []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(subjects))
	for _, s := range subjects {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// CreateStream：受理（名字值域/轴值域/subjects 规范化）→ commit → Kick。
func (svc *PubSubsService) CreateStream(ctx context.Context, req *structurev1.CreateStreamRequest) (*structurev1.CreateStreamResponse, error) {
	parent, _, err := svc.admitStreamParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	if err := validateStreamAxes(req.GetRetention(), req.GetStorage()); err != nil {
		return nil, err
	}
	if req.GetMaxStreamSize() < 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "max_stream_size: %d is negative (0 = unlimited)", req.GetMaxStreamSize())
	}
	retention, storage := req.GetRetention(), req.GetStorage()
	if retention == "" {
		retention = stream.RetentionLimits
	}
	if storage == "" {
		storage = stream.StorageFile
	}
	subjects := normalizeSubjects(req.GetSubjects())
	var maxBytes *int64
	if req.GetMaxStreamSize() > 0 {
		maxBytes = &req.MaxStreamSize
	}
	row := &stream.Stream{
		ProjectID: parent.ProjectID, PubSubID: parent.ID, Name: req.GetName(),
		Subjects: subjects, Retention: retention, Storage: storage, MaxBytes: maxBytes,
	}
	fact, err := svc.s.factOf(ctx, parent.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventStreamCreated, Aggregate: "stream", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "stream.create", Resource: "stream/" + row.ID,
			AfterFP: row.Name + " subjects=" + strings.Join(row.Subjects, ","),
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
				n, err := st.Streams.CountByPubSub(ctx, parent.ID)
				if err != nil {
					return err
				}
				if n >= stream.QuotaPerInstance {
					return apperr.New("E_QUOTA_EXCEEDED",
						"pubsub %s already holds %d streams (limit %d per instance); delete unused streams first",
						parent.Name, n, stream.QuotaPerInstance)
				}
				return nil
			},
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Streams.Create(ctx, row)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "stream")
	}
	svc.s.Engine.KickPubSubs()
	return &structurev1.CreateStreamResponse{Stream: streamMsg(row)}, nil
}

func (svc *PubSubsService) GetStream(ctx context.Context, req *structurev1.GetStreamRequest) (*structurev1.GetStreamResponse, error) {
	parent, _, err := svc.admitStreamParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	row, err := svc.s.Streams.Get(ctx, req.GetStreamId())
	if err != nil || row.PubSubID != parent.ID {
		return nil, mapStateError(state.ErrNotFound, "stream")
	}
	return &structurev1.GetStreamResponse{Stream: streamMsg(row)}, nil
}

// ListStreams 旧→新分页。
func (svc *PubSubsService) ListStreams(ctx context.Context, req *structurev1.ListStreamsRequest) (*structurev1.ListStreamsResponse, error) {
	parent, _, err := svc.admitStreamParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	rows, err := svc.s.Streams.ListByPubSub(ctx, parent.ID)
	if err != nil {
		return nil, mapStateError(err, "stream")
	}
	out := &structurev1.ListStreamsResponse{}
	for i := range rows {
		r := &rows[i]
		if r.Deleted() || r.Seq <= req.GetAfterStreamSeq() {
			continue
		}
		out.Streams = append(out.Streams, streamMsg(r))
		if req.GetLimit() > 0 && len(out.Streams) >= int(req.GetLimit()) {
			break
		}
	}
	return out, nil
}

// streamUpdateAdmission 是 Update/Preview 共用的受理视图（可变面：
// subjects/max_stream_size；不可变轴 retention/storage 无载——携带面在本
// RPC 不存在）。
type streamUpdateAdmission struct {
	parent   *pubsub.PubSub
	row      *stream.Stream
	nextFrom *stream.Stream
	digestOf func() string
}

func (svc *PubSubsService) admitStreamUpdate(ctx context.Context, req *structurev1.UpdateStreamRequest) (*streamUpdateAdmission, error) {
	parent, _, err := svc.admitStreamParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	row, err := svc.s.Streams.Get(ctx, req.GetStreamId())
	if err != nil || row.PubSubID != parent.ID {
		return nil, mapStateError(state.ErrNotFound, "stream")
	}
	// repeated subjects 缺席与空集在 proto3 不可分：变更 subjects 的客户端
	// 须携全集（读行合并后整集下发——proto 注记）。subjects 缺席判据 =
	// nil；携空切片 = 清空 subjects。
	if req.Subjects == nil && req.MaxStreamSize == nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "nothing to update: pass subjects and/or max_stream_size")
	}
	next := *row
	next.Subjects = row.Subjects
	next.MaxBytes = row.MaxBytes
	if req.Subjects != nil {
		next.Subjects = normalizeSubjects(req.GetSubjects())
	}
	if req.MaxStreamSize != nil {
		if req.GetMaxStreamSize() < 0 {
			return nil, apperr.New("E_INVALID_ARGUMENT", "max_stream_size: negative values are invalid (0 = unlimited)")
		}
		v := req.GetMaxStreamSize()
		if v == 0 {
			next.MaxBytes = nil
		} else {
			next.MaxBytes = &v
		}
	}
	adm := &streamUpdateAdmission{parent: parent, row: row, nextFrom: &next}
	adm.digestOf = func() string { return streamDesiredDigest(row.ID, &next) }
	return adm, nil
}

// UpdateStream：受理 → 票面绑定 → commit → 烧票 → Kick。
func (svc *PubSubsService) UpdateStream(ctx context.Context, req *structurev1.UpdateStreamRequest) (*structurev1.UpdateStreamResponse, error) {
	adm, err := svc.admitStreamUpdate(ctx, req)
	if err != nil {
		return nil, err
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		if err := bindSubResourceTicket(svc.s, ticket, previewVerbStreamUpdate, adm.row.ID, adm.digestOf()); err != nil {
			return nil, err
		}
	}
	fact, err := svc.s.factOf(ctx, adm.parent.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventStreamUpdated, Aggregate: "stream", ID: adm.row.ID,
			Payload: mustJSON(structureEventPayload{ID: adm.row.ID, ProjectID: adm.parent.ProjectID}),
			TeamID:  team,
			Action:  "stream.update", Resource: "stream/" + adm.row.ID,
			Detail: string(mustJSON(map[string]any{
				"subjects": adm.nextFrom.Subjects, "max_stream_size": derefInt64Value(adm.nextFrom.MaxBytes)})),
			AfterFP: adm.row.Name,
			Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Streams.UpdateDesired(ctx, adm.row.ID, adm.nextFrom.Subjects, adm.nextFrom.MaxBytes)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "stream")
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		svc.s.ConfirmTickets().DiscardConfirm(ticket)
	}
	svc.s.Engine.KickPubSubs()
	updated, err := svc.s.Streams.Get(ctx, adm.row.ID)
	if err != nil {
		return nil, mapStateError(err, "stream")
	}
	return &structurev1.UpdateStreamResponse{Stream: streamMsg(updated), ConvergedNote: subConvergedNote}, nil
}

// PreviewUpdateStream：只读预演 + 铸票。
func (svc *PubSubsService) PreviewUpdateStream(ctx context.Context, req *structurev1.PreviewUpdateStreamRequest) (*structurev1.PreviewUpdateStreamResponse, error) {
	update := req.GetUpdate()
	if update == nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "update: must not be empty (the preview carries the same update request)")
	}
	adm, err := svc.admitStreamUpdate(ctx, update)
	if err != nil {
		return nil, err
	}
	out := &structurev1.PreviewUpdateStreamResponse{}
	if !subjectsEqualFace(adm.row.Subjects, adm.nextFrom.Subjects) {
		out.Changes = append(out.Changes, &structurev1.SubResourceChange{
			Key: "subjects", From: strings.Join(adm.row.Subjects, ","), To: strings.Join(adm.nextFrom.Subjects, ",")})
	}
	if !maxBytesEqualFace(adm.row.MaxBytes, adm.nextFrom.MaxBytes) {
		out.Changes = append(out.Changes, &structurev1.SubResourceChange{
			Key: "max_stream_size", From: sizeWord(adm.row.MaxBytes), To: sizeWord(adm.nextFrom.MaxBytes)})
	}
	sort.Slice(out.Changes, func(i, j int) bool { return out.Changes[i].GetKey() < out.Changes[j].GetKey() })
	diffLines := make([]string, 0, len(out.Changes))
	for _, ch := range out.Changes {
		diffLines = append(diffLines, fmt.Sprintf("%s:%s=%s", ch.GetKey(), ch.GetFrom(), ch.GetTo()))
	}
	ticket, ttl, err := svc.s.MintConfirmTicket(governance.ConfirmTicketPayload{
		Verb:          previewVerbStreamUpdate,
		TargetApp:     adm.row.ID,
		RequestDigest: adm.digestOf(),
		PreviewDigest: sha256Hex([]byte(joinSubLines(diffLines))),
		Operator:      confirmOperator(ctx),
	})
	if err != nil {
		return nil, mapStateError(err, "ticket")
	}
	out.Ticket = ticket
	//nolint:gosec // TTL 是 600s 常量，int32 安全。
	out.TicketExpiresIn = int32(ttl.Seconds())
	return out, nil
}

// DeleteStream：行 tombstone + 事件（GC 接棒——Stream 删除即数据删除）。
func (svc *PubSubsService) DeleteStream(ctx context.Context, req *structurev1.DeleteStreamRequest) (*structurev1.DeleteStreamResponse, error) {
	parent, _, err := svc.admitStreamParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	row, err := svc.s.Streams.Get(ctx, req.GetStreamId())
	if err != nil || row.PubSubID != parent.ID {
		return nil, mapStateError(state.ErrNotFound, "stream")
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		if err := bindSubResourceTicket(svc.s, ticket, previewVerbStreamDelete, row.ID, streamDeleteDigest(row)); err != nil {
			return nil, err
		}
	}
	fact, err := svc.s.factOf(ctx, parent.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventStreamDeleted, Aggregate: "stream", ID: row.ID,
			Payload: mustJSON(structureEventPayload{ID: row.ID, ProjectID: parent.ProjectID}),
			TeamID:  team,
			Action:  "stream.delete", Resource: "stream/" + row.ID,
			BeforeFP: row.Name + " subjects=" + strings.Join(row.Subjects, ","),
			Actor:    authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.s.commit(ctx, writeFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Streams.SoftDelete(ctx, row.ID)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, mapStateError(err, "stream")
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		svc.s.ConfirmTickets().DiscardConfirm(ticket)
	}
	svc.s.Engine.KickPubSubs()
	return &structurev1.DeleteStreamResponse{}, nil
}

// PreviewDeleteStream：删除披露行（nats state 面——消息数/字节量 live 可得）
// + 铸票（wide）。
func (svc *PubSubsService) PreviewDeleteStream(ctx context.Context, req *structurev1.PreviewDeleteStreamRequest) (*structurev1.PreviewDeleteStreamResponse, error) {
	parent, _, err := svc.admitStreamParent(ctx, req.GetPubsubId())
	if err != nil {
		return nil, err
	}
	row, err := svc.s.Streams.Get(ctx, req.GetStreamId())
	if err != nil || row.PubSubID != parent.ID {
		return nil, mapStateError(state.ErrNotFound, "stream")
	}
	var dataBytes, dataMessages int64
	disclosure := fmt.Sprintf(
		"Deleting stream %s on pubsub %s destroys the retained messages on delete — this is unrecoverable. Live message totals are unavailable (instance is %s).",
		row.Name, parent.Name, parent.Status)
	if parent.Status == pubsub.StatusRunning {
		if live, lerr := svc.s.Engine.StreamLiveInspect(ctx, parent.ID, row.ID); lerr == nil && live.Exists {
			dataBytes, dataMessages = live.Bytes, live.Messages
			disclosure = fmt.Sprintf(
				"Deleting stream %s on pubsub %s destroys the retained messages on delete — this is unrecoverable. Live state: %d messages, %d bytes.",
				row.Name, parent.Name, dataMessages, dataBytes)
		} else if lerr == nil {
			disclosure = fmt.Sprintf(
				"Deleting stream %s on pubsub %s destroys the retained messages on delete — this is unrecoverable. The live stream is already absent (only the declaration row remains).",
				row.Name, parent.Name)
		}
	}
	ticket, ttl, err := svc.s.MintConfirmTicket(governance.ConfirmTicketPayload{
		Verb:          previewVerbStreamDelete,
		TargetApp:     row.ID,
		RequestDigest: streamDeleteDigest(row),
		PreviewDigest: sha256Hex([]byte(disclosure)),
		Operator:      confirmOperator(ctx),
	})
	if err != nil {
		return nil, mapStateError(err, "ticket")
	}
	//nolint:gosec // TTL 是 600s 常量，int32 安全。
	return &structurev1.PreviewDeleteStreamResponse{
		Stream: streamMsg(row), DataBytes: dataBytes, DataMessages: dataMessages,
		Disclosure: disclosure, Ticket: ticket, TicketExpiresIn: int32(ttl.Seconds()),
	}, nil
}

// streamDesiredDigest：Update 票面摘要（前缀 stream=）。
func streamDesiredDigest(streamID string, next *stream.Stream) string {
	max := "0"
	if next.MaxBytes != nil {
		max = strconv.FormatInt(*next.MaxBytes, 10)
	}
	return sha256Hex([]byte("stream=" + streamID + "\nsubjects=" + strings.Join(next.Subjects, ",") + "\nmax_stream_size=" + max + "\n"))
}

// streamDeleteDigest：Delete 票面摘要。
func streamDeleteDigest(row *stream.Stream) string {
	return sha256Hex([]byte("stream-del=" + row.ID + "\nname=" + row.Name + "\n"))
}

// streamMsg 行 → proto 映射。
func streamMsg(row *stream.Stream) *structurev1.Stream {
	msg := &structurev1.Stream{
		Id: row.ID, Seq: row.Seq, ProjectId: row.ProjectID, PubsubId: row.PubSubID, Name: row.Name,
		Subjects: row.Subjects, Retention: row.Retention, Storage: row.Storage,
		Status: row.Status, StatusError: row.StatusErr,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.MaxBytes != nil {
		msg.MaxStreamSize = *row.MaxBytes
	}
	return msg
}

// subjectsEqualFace / maxBytesEqualFace / sizeWord 是受理面的小帮手（与
// dataservices 的对照帮手同义——分层重复是有意的：interfaces 不 import
// 卫星内符号〔读径只经 domain〕）。
func subjectsEqualFace(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as, bs := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func maxBytesEqualFace(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sizeWord(v *int64) string {
	if v == nil {
		return "unlimited"
	}
	return strconv.FormatInt(*v, 10)
}
