package deliverygrpc

import (
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	deliveryv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/delivery/v1"
	runtimev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/runtime/v1"
	specv1 "github.com/fleetlyrun/fleetly/genproto/fleetly/spec/v1"
	"github.com/fleetlyrun/fleetly/internal/application/engine/api"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/build"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/deployment"
	"github.com/fleetlyrun/fleetly/internal/domain/delivery/revision"
	"github.com/fleetlyrun/fleetly/internal/domain/shared/capability"
)

// ---- 行 ↔ proto 映射（时间戳与状态原样：存储即契约） ----

func deploymentMsg(d deployment.Deployment) *deliveryv1.Deployment {
	return &deliveryv1.Deployment{
		Id: d.ID, Seq: d.Seq, AppId: d.AppID, FromRevision: d.FromRevision, ToRevision: d.ToRevision,
		State: string(d.State), Generation: d.Generation, IdempotencyKey: d.IdempotencyKey,
		CommitSha: d.CommitSHA, SupersededBy: d.SupersededBy, Error: d.Error,
		ObserveDeadline: d.ObserveDeadline, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt,
		FinishedAt: d.FinishedAt, PreDeployTaskId: api.PreDeployTaskID(&d),
		FromGeneration: d.FromGeneration,
	}
}

func revisionMsg(rev revision.Revision) *deliveryv1.Revision {
	msg := &deliveryv1.Revision{
		Id: rev.ID, AppId: rev.AppID, Seq: rev.Seq, Digest: rev.Digest, CreatedAt: rev.CreatedAt,
		SourceText: rev.SourceText,
	}
	var frozen specv1.AppSpec
	if err := protojson.Unmarshal(rev.Spec, &frozen); err == nil {
		for i := range frozen.Processes {
			p := frozen.Processes[i]
			msg.ProcessStrategies = append(msg.ProcessStrategies, &deliveryv1.ProcessStrategy{
				Process: p.GetName(), Strategy: p.GetStrategy(),
			})
		}
		msg.SourceKind = sourceKindOf(frozen.GetSource())
	}
	return msg
}

func sourceKindOf(src *specv1.Source) deliveryv1.SourceKind {
	switch src.GetKind().(type) {
	case *specv1.Source_Git:
		return deliveryv1.SourceKind_SOURCE_KIND_GIT
	case *specv1.Source_Image:
		return deliveryv1.SourceKind_SOURCE_KIND_IMAGE
	case *specv1.Source_Upload:
		return deliveryv1.SourceKind_SOURCE_KIND_UPLOAD
	}
	return deliveryv1.SourceKind_SOURCE_KIND_UNSPECIFIED
}

func workloadMsg(w capability.Workload) *deliveryv1.Workload {
	msg := &deliveryv1.Workload{
		Id: w.ID, Process: w.Process, Image: w.Image, Command: w.Command,
		Env: w.Env, Replicas: w.Replicas,
		Networks: w.Networks, EgressNetworks: w.EgressNetworks,
		Addressing: addressNames(w.Addressing),
		Restart:    string(w.Restart),
		StopGrace:  durationMsg(w.StopGrace),
		RestartSeq: w.RestartSeq,
	}
	for _, p := range w.Ports {
		msg.Ports = append(msg.Ports, &specv1.PortSpec{Port: p.Port, Protocol: protocolEnum(p.Protocol)})
	}
	if w.Healthcheck != nil {
		msg.Healthcheck = &deliveryv1.WorkloadHealthcheck{
			HttpPath: w.Healthcheck.HTTPPath, HttpPort: w.Healthcheck.HTTPPort,
			TcpPort: w.Healthcheck.TCPPort, Exec: w.Healthcheck.Exec,
			Interval:    durationMsg(w.Healthcheck.Interval),
			Timeout:     durationMsg(w.Healthcheck.Timeout),
			StartPeriod: durationMsg(w.Healthcheck.StartPeriod),
			Retries:     w.Healthcheck.Retries,
		}
	}
	if w.Resources != nil {
		msg.Resources = &specv1.ResourcesSpec{CpuMillis: w.Resources.CPUMillis, MemoryMb: w.Resources.MemoryMB}
	}
	msg.Placement = &specv1.PlacementSpec{NodeIds: w.Placement.NodeIDs}
	for _, v := range w.Volumes {
		msg.Volumes = append(msg.Volumes, &specv1.VolumeAttachment{
			VolumeId: v.VolumeID, Target: v.Target, ReadOnly: v.ReadOnly,
		})
	}
	return msg
}

func workloadObservationMsg(o capability.WorkloadObservation) *runtimev1.WorkloadObservation {
	return &runtimev1.WorkloadObservation{
		WorkloadId: o.WorkloadID, Generation: uint64(o.Generation),
		Image: o.Image, Command: o.Command, Replicas: o.Replicas,
		State: string(o.State), RolloutStalled: o.RolloutStalled, RolloutDetail: o.RolloutDetail,
	}
}

func protocolEnum(p capability.Protocol) specv1.Protocol {
	switch p {
	case capability.ProtocolHTTP:
		return specv1.Protocol_PROTOCOL_HTTP
	case capability.ProtocolH2C:
		return specv1.Protocol_PROTOCOL_H2C
	case capability.ProtocolTCP:
		return specv1.Protocol_PROTOCOL_TCP
	}
	return specv1.Protocol_PROTOCOL_UNSPECIFIED
}

func addressNames(addrs []capability.Address) []string {
	if len(addrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.Name)
	}
	return out
}

func durationMsg(d time.Duration) *durationpb.Duration {
	if d == 0 {
		return nil
	}
	return durationpb.New(d)
}

func buildMsg(b build.Build) *deliveryv1.Build {
	return &deliveryv1.Build{
		Id: b.ID, Seq: b.Seq, AppId: b.AppID, RevisionId: b.RevisionID, State: string(b.State),
		Digest: b.Digest, Error: b.Error, CreatedAt: b.CreatedAt, FinishedAt: b.FinishedAt,
	}
}

func marshalSpec(m proto.Message) ([]byte, error) {
	return protojson.MarshalOptions{EmitUnpopulated: false, UseProtoNames: true}.Marshal(m)
}
