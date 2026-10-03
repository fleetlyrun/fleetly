package fleetlygrpc

// UploadSource（F1.10，ADR-0019 附录 A）：仓内首个 client-streaming 写面。
// 首帧 meta（project_id）→ 余帧 chunk（tar 字节流）→ EOF 内容寻址落库。
// 执法链裁决（附录 A.2）：冻结在流式拦截器首帧执法（authn 后）；幂等表
// 豁免——内容寻址天然幂等（同 digest 返回同一引用，deduplicated=true）；
// 速率预算不计数——刹车=单上传上限 + 项目存量配额；15m 流硬上限。

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/authn"
	"github.com/fleetlyrun/fleetly/internal/state"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	"github.com/fleetlyrun/fleetly/internal/state/sourceupload"
	"github.com/fleetlyrun/fleetly/internal/upload"
)

// uploadStreamTimeout 是上传流硬上限（ADR-0019 附录 A.2）：512MiB 慢链路
// 合法超 unary 30s——流生命周期由取消信号管理，本上限与构建超时同量级
// （挂死流不得无限占用 tmp 文件与连接）。
const uploadStreamTimeout = 15 * time.Minute

// eventUploadStored 是 upload.stored 事件名。
const eventUploadStored = "upload.stored"

// uploadStoredPayload 是 upload.stored 事件负载（字段只增，ADR-0026 形状
// 钉扎经 schema golden）。
type uploadStoredPayload struct {
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	Digest       string `json:"digest"`
	SizeBytes    int64  `json:"size_bytes"`
	Deduplicated bool   `json:"deduplicated"`
}

// UploadSource 消费上传流：接收期 tmp+哈希+字节计数；EOF 后 blob 内容寻址
// 落位（跨项目共享）+ 行落库（同项目同内容 → 同一引用）。
func (svc *BuildsService) UploadSource(stream deliveryv1.BuildsService_UploadSourceServer) error {
	ctx, cancel := context.WithTimeout(stream.Context(), uploadStreamTimeout)
	defer cancel()

	first, err := stream.Recv()
	if err != nil {
		return err // 空流/取消/冻结拒绝（首帧执法错误面）
	}
	meta := first.GetMeta()
	if meta == nil {
		return apperr.New("E_INVALID_ARGUMENT", "the first frame must carry the upload metadata (meta), not a chunk")
	}
	if meta.GetProjectId() == "" {
		return apperr.New("E_INVALID_ARGUMENT", "meta.project_id: must not be empty")
	}
	projectID := meta.GetProjectId()
	// 行级授权（ADR-0035）：首帧后、tmp 落盘前拒绝——越权上传不占暂存与
	// 字节预算。
	if err := svc.s.authorizeProjectID(ctx, projectID); err != nil {
		return err
	}

	recv, err := svc.s.UploadStore.Begin()
	if err != nil {
		return apperr.New("E_INTERNAL", "opening the upload staging file failed").WithCause(err)
	}
	for {
		frame, rerr := stream.Recv()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			recv.Abort()
			return rerr // 客户端中断/取消
		}
		if frame.GetMeta() != nil {
			recv.Abort()
			return apperr.New("E_INVALID_ARGUMENT", "the metadata frame may only appear first")
		}
		if werr := recv.Write(frame.GetChunk()); werr != nil {
			recv.Abort()
			var tooLarge *upload.ErrTooLarge
			if errors.As(werr, &tooLarge) {
				return apperr.New("E_UPLOAD_TOO_LARGE",
					"the upload exceeds the per-upload size limit of %d bytes (received %d)",
					tooLarge.Limit, tooLarge.Received).
					WithContext("limit_bytes", strconv.FormatInt(tooLarge.Limit, 10)).
					WithContext("received_bytes", strconv.FormatInt(tooLarge.Received, 10)).
					WithSuggestion("Exclude build-irrelevant files from the directory (e.g. .git) or build from a git reference instead.")
			}
			return apperr.New("E_INTERNAL", "storing the upload failed").WithCause(werr)
		}
	}
	digest, size := recv.Digest()
	if size == 0 {
		recv.Abort()
		return apperr.New("E_INVALID_ARGUMENT", "the upload carries no bytes (an empty tar stream)")
	}
	if _, cerr := svc.s.UploadStore.Commit(recv); cerr != nil {
		return apperr.New("E_INTERNAL", "committing the upload blob failed").WithCause(cerr)
	}

	// 行落库：同项目同 digest 已在 → 重传幂等返回既有引用（内容寻址天然
	// 幂等强于键重放，ADR-0019 附录 A.2）。
	if existing, ferr := svc.s.Uploads.FindByDigest(ctx, svc.s.DB.Runner(), projectID, digest); ferr == nil {
		return stream.SendAndClose(&deliveryv1.UploadSourceResponse{
			Id: existing.ID, Digest: digest, SizeBytes: size, Deduplicated: true,
		})
	} else if !errors.Is(ferr, state.ErrNotFound) {
		return apperr.New("E_INTERNAL", "upload lookup failed").WithCause(ferr)
	}

	row := &sourceupload.Upload{ID: newID(), ProjectID: projectID, Digest: digest, SizeBytes: size}
	payload, err := json.Marshal(uploadStoredPayload{ //nolint:errcheck // 结构体字段恒可序列化
		ID: row.ID, ProjectID: row.ProjectID, Digest: digest, SizeBytes: size, Deduplicated: false,
	})
	if err != nil {
		return apperr.New("E_INTERNAL", "encoding the upload event payload failed").WithCause(err)
	}
	err = svc.s.commit(ctx, writeFact{
		checks: []acceptanceCheck{
			svc.s.parentProjectAlive(projectID),
			svc.s.uploadBytesQuota(projectID, size),
		},
		write: func(ctx context.Context, tx *sql.Tx) error {
			return svc.s.Uploads.Create(ctx, tx, row)
		},
		events: []eventFact{{name: eventUploadStored, aggregate: "project", id: projectID, payload: payload}},
		auditsFrom: func() []*audit.Entry {
			return []*audit.Entry{{
				ID: newID(), Actor: authn.ActorFromContext(ctx), Source: authn.SourceFromContext(ctx),
				Action: "upload.create", Resource: "upload/" + row.ID, AfterFP: digest,
			}}
		},
	})
	if err != nil {
		// 并发同内容：另一路先落行（唯一索引）→ 幂等返回既有引用。
		if errors.Is(err, state.ErrAlreadyExists) {
			if existing, ferr := svc.s.Uploads.FindByDigest(ctx, svc.s.DB.Runner(), projectID, digest); ferr == nil {
				return stream.SendAndClose(&deliveryv1.UploadSourceResponse{
					Id: existing.ID, Digest: digest, SizeBytes: size, Deduplicated: true,
				})
			}
		}
		return mapStateError(err, "upload")
	}
	return stream.SendAndClose(&deliveryv1.UploadSourceResponse{
		Id: row.ID, Digest: digest, SizeBytes: size,
	})
}

// ListUploads 列项目上传产物（新→旧 + after 游标；ADR-0026 惯例）。
func (svc *BuildsService) ListUploads(ctx context.Context, req *deliveryv1.ListUploadsRequest) (*deliveryv1.ListUploadsResponse, error) {
	if req.GetProjectId() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id: must not be empty")
	}
	if err := svc.s.authorizeProjectID(ctx, req.GetProjectId()); err != nil {
		return nil, err
	}
	rows, err := svc.s.Uploads.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetAfterUploadId(), listLimit(req.GetLimit()))
	if err != nil {
		return nil, mapStateError(err, "upload")
	}
	out := &deliveryv1.ListUploadsResponse{}
	for _, u := range rows {
		out.Uploads = append(out.Uploads, &deliveryv1.Upload{
			Id: u.ID, ProjectId: u.ProjectID, Digest: u.Digest, SizeBytes: u.SizeBytes, CreatedAt: u.CreatedAt,
		})
	}
	return out, nil
}

// uploadBytesQuota：项目上传存量配额（ADR-0019 附录 A.3：distinct digest
// 求和；(project, digest) 唯一使 SUM 即去重值；读在 finalize 事务内——
// ADR-0024 TOCTOU 口径）。
func (s *Services) uploadBytesQuota(projectID string, size int64) acceptanceCheck {
	return func(ctx context.Context, tx *sql.Tx) error {
		sum, err := s.Uploads.BytesByProject(ctx, tx, projectID)
		if err != nil {
			return err
		}
		if sum+size > s.UploadStore.Quota() {
			return apperr.New("E_QUOTA_EXCEEDED",
				"project %s would hold %d upload bytes (limit %d); wait for the retention sweep of unreferenced uploads",
				projectID, sum+size, s.UploadStore.Quota())
		}
		return nil
	}
}
