package datagrpc

import (
	"context"
	"fmt"
	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/application/authn"
	"github.com/fleetlyrun/fleetly/internal/application/governance"
	"github.com/fleetlyrun/fleetly/internal/application/transact"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketresource"
	"github.com/fleetlyrun/fleetly/internal/domain/data/bucketstore"
	"github.com/fleetlyrun/fleetly/internal/domain/services/bucketstoretemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/writes"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/apperr"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/state"
	"github.com/fleetlyrun/fleetly/internal/interfaces/grpc/grpcapi"
	"sort"
)

// CreateBucket：受理（S3 名值域/quota 非负）→ commit → Kick。
func (svc *BucketStoresService) CreateBucket(ctx context.Context, req *structurev1.CreateBucketRequest) (*structurev1.CreateBucketResponse, error) {
	parent, _, err := svc.admitBucketParent(ctx, req.GetBucketStoreId())
	if err != nil {
		return nil, err
	}
	if err := bucketstoretemplate.ValidateBucketName(req.GetName()); err != nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "name: %s", err)
	}
	if req.GetQuotaBytes() < 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "quota_bytes: %d is negative (0 = no quota)", req.GetQuotaBytes())
	}
	var quota *int64
	if req.GetQuotaBytes() > 0 {
		quota = &req.QuotaBytes
	}
	row := &bucketresource.Bucket{
		ProjectID: parent.ProjectID, BucketStoreID: parent.ID, Name: req.GetName(), QuotaBytes: quota,
	}
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, parent.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventBucketCreated, Aggregate: "bucket", ID: row.ID,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: row.ID, ProjectID: row.ProjectID}),
			TeamID:  team,
			Action:  "bucket.create", Resource: "bucket/" + row.ID,
			AfterFP: row.Name + " quota=" + sizeWord(row.QuotaBytes),
			Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Checks: []grpcapi.Check{
			parentProjectAlive(parent.ProjectID),
			func(ctx context.Context, st transact.Stores) error {
				n, err := st.Buckets.CountByBucketStore(ctx, parent.ID)
				if err != nil {
					return err
				}
				if n >= bucketresource.QuotaPerInstance {
					return apperr.New("E_QUOTA_EXCEEDED",
						"bucket store %s already holds %d buckets (limit %d per instance); delete unused buckets first",
						parent.Name, n, bucketresource.QuotaPerInstance)
				}
				return nil
			},
		},
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Buckets.Create(ctx, row)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "bucket")
	}
	svc.deps.Data.KickBucketStores()
	return &structurev1.CreateBucketResponse{Bucket: bucketMsg(row)}, nil
}

func (svc *BucketStoresService) GetBucket(ctx context.Context, req *structurev1.GetBucketRequest) (*structurev1.GetBucketResponse, error) {
	parent, _, err := svc.admitBucketParent(ctx, req.GetBucketStoreId())
	if err != nil {
		return nil, err
	}
	row, err := svc.deps.Buckets.Get(ctx, req.GetBucketId())
	if err != nil || row.BucketStoreID != parent.ID {
		return nil, grpcapi.MapStateError(state.ErrNotFound, "bucket")
	}
	return &structurev1.GetBucketResponse{Bucket: bucketMsg(row)}, nil
}

// ListBuckets 旧→新分页。
func (svc *BucketStoresService) ListBuckets(ctx context.Context, req *structurev1.ListBucketsRequest) (*structurev1.ListBucketsResponse, error) {
	parent, _, err := svc.admitBucketParent(ctx, req.GetBucketStoreId())
	if err != nil {
		return nil, err
	}
	rows, err := svc.deps.Buckets.ListByBucketStore(ctx, parent.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "bucket")
	}
	out := &structurev1.ListBucketsResponse{}
	for i := range rows {
		r := &rows[i]
		if r.Deleted() || r.Seq <= req.GetAfterBucketSeq() {
			continue
		}
		out.Buckets = append(out.Buckets, bucketMsg(r))
		if req.GetLimit() > 0 && len(out.Buckets) >= int(req.GetLimit()) {
			break
		}
	}
	return out, nil
}

// bucketUpdateAdmission 是 Update/Preview 共用的受理视图（可变面：quota）。
type bucketUpdateAdmission struct {
	parent   *bucketstore.BucketStore
	row      *bucketresource.Bucket
	nextFrom *bucketresource.Bucket
	digestOf func() string
}

func (svc *BucketStoresService) admitBucketUpdate(ctx context.Context, req *structurev1.UpdateBucketRequest) (*bucketUpdateAdmission, error) {
	parent, _, err := svc.admitBucketParent(ctx, req.GetBucketStoreId())
	if err != nil {
		return nil, err
	}
	row, err := svc.deps.Buckets.Get(ctx, req.GetBucketId())
	if err != nil || row.BucketStoreID != parent.ID {
		return nil, grpcapi.MapStateError(state.ErrNotFound, "bucket")
	}
	if req.QuotaBytes == nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "nothing to update: pass quota_bytes")
	}
	if req.GetQuotaBytes() < 0 {
		return nil, apperr.New("E_INVALID_ARGUMENT", "quota_bytes: negative values are invalid (0 = no quota)")
	}
	next := *row
	next.QuotaBytes = row.QuotaBytes
	if req.GetQuotaBytes() == 0 {
		next.QuotaBytes = nil
	} else {
		v := req.GetQuotaBytes()
		next.QuotaBytes = &v
	}
	adm := &bucketUpdateAdmission{parent: parent, row: row, nextFrom: &next}
	adm.digestOf = func() string { return bucketDesiredDigest(row.ID, &next) }
	return adm, nil
}

// UpdateBucket：受理 → 票面绑定 → commit → 烧票 → Kick。
func (svc *BucketStoresService) UpdateBucket(ctx context.Context, req *structurev1.UpdateBucketRequest) (*structurev1.UpdateBucketResponse, error) {
	adm, err := svc.admitBucketUpdate(ctx, req)
	if err != nil {
		return nil, err
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		if err := bindSubResourceTicket(svc.deps, ticket, previewVerbBucketUpdate, adm.row.ID, adm.digestOf()); err != nil {
			return nil, err
		}
	}
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, adm.parent.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventBucketUpdated, Aggregate: "bucket", ID: adm.row.ID,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: adm.row.ID, ProjectID: adm.parent.ProjectID}),
			TeamID:  team,
			Action:  "bucket.update", Resource: "bucket/" + adm.row.ID,
			Detail:  string(grpcapi.MustJSON(map[string]any{"quota_bytes": derefInt64Value(adm.nextFrom.QuotaBytes)})),
			AfterFP: adm.row.Name,
			Actor:   authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Buckets.UpdateDesired(ctx, adm.row.ID, adm.nextFrom.QuotaBytes)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "bucket")
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		svc.deps.Tickets.DiscardConfirm(ticket)
	}
	svc.deps.Data.KickBucketStores()
	updated, err := svc.deps.Buckets.Get(ctx, adm.row.ID)
	if err != nil {
		return nil, grpcapi.MapStateError(err, "bucket")
	}
	return &structurev1.UpdateBucketResponse{Bucket: bucketMsg(updated), ConvergedNote: subConvergedNote}, nil
}

// PreviewUpdateBucket：只读预演 + 铸票。
func (svc *BucketStoresService) PreviewUpdateBucket(ctx context.Context, req *structurev1.PreviewUpdateBucketRequest) (*structurev1.PreviewUpdateBucketResponse, error) {
	update := req.GetUpdate()
	if update == nil {
		return nil, apperr.New("E_INVALID_ARGUMENT", "update: must not be empty (the preview carries the same update request)")
	}
	adm, err := svc.admitBucketUpdate(ctx, update)
	if err != nil {
		return nil, err
	}
	out := &structurev1.PreviewUpdateBucketResponse{}
	if !quotaEqualFace(adm.row.QuotaBytes, adm.nextFrom.QuotaBytes) {
		out.Changes = append(out.Changes, &structurev1.SubResourceChange{
			Key: "quota_bytes", From: sizeWord(adm.row.QuotaBytes), To: sizeWord(adm.nextFrom.QuotaBytes)})
	}
	sort.Slice(out.Changes, func(i, j int) bool { return out.Changes[i].GetKey() < out.Changes[j].GetKey() })
	diffLines := make([]string, 0, len(out.Changes))
	for _, ch := range out.Changes {
		diffLines = append(diffLines, fmt.Sprintf("%s:%s=%s", ch.GetKey(), ch.GetFrom(), ch.GetTo()))
	}
	ticket, ttl, err := svc.deps.Tickets.MintConfirm(governance.ConfirmTicketPayload{
		Verb:          previewVerbBucketUpdate,
		TargetApp:     adm.row.ID,
		RequestDigest: adm.digestOf(),
		PreviewDigest: grpcapi.SHA256Hex([]byte(joinSubLines(diffLines))),
		Operator:      grpcapi.ConfirmOperator(ctx),
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "ticket")
	}
	out.Ticket = ticket
	//nolint:gosec // TTL 是 600s 常量，int32 安全。
	out.TicketExpiresIn = int32(ttl.Seconds())
	return out, nil
}

// DeleteBucket：行 tombstone + 事件（GC 接棒——mcli rb --force 连带对象）。
func (svc *BucketStoresService) DeleteBucket(ctx context.Context, req *structurev1.DeleteBucketRequest) (*structurev1.DeleteBucketResponse, error) {
	parent, _, err := svc.admitBucketParent(ctx, req.GetBucketStoreId())
	if err != nil {
		return nil, err
	}
	row, err := svc.deps.Buckets.Get(ctx, req.GetBucketId())
	if err != nil || row.BucketStoreID != parent.ID {
		return nil, grpcapi.MapStateError(state.ErrNotFound, "bucket")
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		if err := bindSubResourceTicket(svc.deps, ticket, previewVerbBucketDelete, row.ID, bucketDeleteDigest(row)); err != nil {
			return nil, err
		}
	}
	fact, err := grpcapi.FactOf(ctx, svc.deps.Projects, parent.ProjectID, func(team string) writes.FactRow {
		return writes.FactRow{
			Name: eventBucketDeleted, Aggregate: "bucket", ID: row.ID,
			Payload: grpcapi.MustJSON(grpcapi.StructureEventPayload{ID: row.ID, ProjectID: parent.ProjectID}),
			TeamID:  team,
			Action:  "bucket.delete", Resource: "bucket/" + row.ID,
			BeforeFP: row.Name + " quota=" + sizeWord(row.QuotaBytes),
			Actor:    authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
		}
	})
	if err != nil {
		return nil, err
	}
	err = svc.deps.Commit(ctx, grpcapi.WriteFact{
		Write: func(ctx context.Context, st transact.Stores) error {
			return st.Buckets.SoftDelete(ctx, row.ID)
		},
		Facts: []func() writes.FactRow{fact},
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "bucket")
	}
	if ticket := req.GetConfirmation().GetTicket(); ticket != "" {
		svc.deps.Tickets.DiscardConfirm(ticket)
	}
	svc.deps.Data.KickBucketStores()
	return &structurev1.DeleteBucketResponse{}, nil
}

// PreviewDeleteBucket：删除披露行（mcli du 对象量/字节量 live 可得）+ 铸票。
func (svc *BucketStoresService) PreviewDeleteBucket(ctx context.Context, req *structurev1.PreviewDeleteBucketRequest) (*structurev1.PreviewDeleteBucketResponse, error) {
	parent, _, err := svc.admitBucketParent(ctx, req.GetBucketStoreId())
	if err != nil {
		return nil, err
	}
	row, err := svc.deps.Buckets.Get(ctx, req.GetBucketId())
	if err != nil || row.BucketStoreID != parent.ID {
		return nil, grpcapi.MapStateError(state.ErrNotFound, "bucket")
	}
	var dataBytes, dataObjects int64
	disclosure := fmt.Sprintf(
		"Deleting bucket %s on bucket store %s force-removes the bucket and every object in it — this is unrecoverable. Live object totals are unavailable (instance is %s).",
		row.Name, parent.Name, parent.Status)
	if parent.Status == bucketstore.StatusRunning {
		if live, lerr := svc.deps.Data.BucketLiveInspect(ctx, parent.ID, row.ID); lerr == nil && live.Exists {
			dataBytes, dataObjects = live.Bytes, live.Objects
			disclosure = fmt.Sprintf(
				"Deleting bucket %s on bucket store %s force-removes the bucket and every object in it — this is unrecoverable. Live state: %d objects, %d bytes.",
				row.Name, parent.Name, dataObjects, dataBytes)
		} else if lerr == nil {
			disclosure = fmt.Sprintf(
				"Deleting bucket %s on bucket store %s force-removes the bucket and every object in it — this is unrecoverable. The live bucket is already absent (only the declaration row remains).",
				row.Name, parent.Name)
		}
	}
	ticket, ttl, err := svc.deps.Tickets.MintConfirm(governance.ConfirmTicketPayload{
		Verb:          previewVerbBucketDelete,
		TargetApp:     row.ID,
		RequestDigest: bucketDeleteDigest(row),
		PreviewDigest: grpcapi.SHA256Hex([]byte(disclosure)),
		Operator:      grpcapi.ConfirmOperator(ctx),
	})
	if err != nil {
		return nil, grpcapi.MapStateError(err, "ticket")
	}
	//nolint:gosec // TTL 是 600s 常量，int32 安全。
	return &structurev1.PreviewDeleteBucketResponse{
		Bucket: bucketMsg(row), DataBytes: dataBytes, DataObjects: dataObjects,
		Disclosure: disclosure, Ticket: ticket, TicketExpiresIn: int32(ttl.Seconds()),
	}, nil
}

// bucketDesiredDigest：Update 票面摘要（前缀 bucket=）。
func bucketDesiredDigest(bucketID string, next *bucketresource.Bucket) string {
	return grpcapi.SHA256Hex([]byte("bucket=" + bucketID + "\nquota_bytes=" + sizeWord(next.QuotaBytes) + "\n"))
}

// bucketDeleteDigest：Delete 票面摘要。
func bucketDeleteDigest(row *bucketresource.Bucket) string {
	return grpcapi.SHA256Hex([]byte("bucket-del=" + row.ID + "\nname=" + row.Name + "\n"))
}

// quotaEqualFace 是受理面的 quota 对照（sizeWord/subjectsEqualFace 同族）。
func quotaEqualFace(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// bucketMsg 行 → proto 映射。
func bucketMsg(row *bucketresource.Bucket) *structurev1.Bucket {
	msg := &structurev1.Bucket{
		Id: row.ID, Seq: row.Seq, ProjectId: row.ProjectID, BucketStoreId: row.BucketStoreID, Name: row.Name,
		Status: row.Status, StatusError: row.StatusErr,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
	if row.QuotaBytes != nil {
		msg.QuotaBytes = *row.QuotaBytes
	}
	return msg
}
