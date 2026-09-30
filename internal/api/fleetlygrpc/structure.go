package fleetlygrpc

// Structure 上下文服务实现（Projects/Apps/Secrets/Configs/Volumes/Networks）。

import (
	"context"
	"database/sql"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/api/apperr"
	"github.com/fleetlyrun/fleetly/internal/material"
	"github.com/fleetlyrun/fleetly/internal/state/app"
	"github.com/fleetlyrun/fleetly/internal/state/audit"
	configrepo "github.com/fleetlyrun/fleetly/internal/state/config"
	networkrepo "github.com/fleetlyrun/fleetly/internal/state/network"
	"github.com/fleetlyrun/fleetly/internal/state/project"
	"github.com/fleetlyrun/fleetly/internal/state/secret"
	"github.com/fleetlyrun/fleetly/internal/state/volume"
)

// ---- Projects ----

type ProjectsService struct {
	structurev1.UnimplementedProjectsServiceServer
	s *Services
}

func (svc *ProjectsService) CreateProject(ctx context.Context, req *structurev1.CreateProjectRequest) (*structurev1.CreateProjectResponse, error) {
	if req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "name: must not be empty")
	}
	p := &project.Project{ID: newID(), Name: req.GetName(), TeamID: req.GetTeamId()}
	if p.TeamID == "" {
		p.TeamID = "default"
	}
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Projects.Create(ctx, tx, p); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Source: audit.SourceAPI, Action: "project.create",
			Resource: "project/" + p.ID, AfterFP: p.Name,
		})
	})
	if err != nil {
		return nil, mapStateError(err, "project")
	}
	return &structurev1.CreateProjectResponse{Project: projectMsg(p)}, nil
}

func (svc *ProjectsService) GetProject(ctx context.Context, req *structurev1.GetProjectRequest) (*structurev1.GetProjectResponse, error) {
	p, err := svc.s.Projects.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "project")
	}
	return &structurev1.GetProjectResponse{Project: projectMsg(p)}, nil
}

func (svc *ProjectsService) ListProjects(ctx context.Context, _ *structurev1.ListProjectsRequest) (*structurev1.ListProjectsResponse, error) {
	list, err := svc.s.Projects.List(ctx, svc.s.DB.Runner())
	if err != nil {
		return nil, mapStateError(err, "project")
	}
	out := &structurev1.ListProjectsResponse{}
	for i := range list {
		out.Projects = append(out.Projects, projectMsg(&list[i]))
	}
	return out, nil
}

func (svc *ProjectsService) DeleteProject(ctx context.Context, req *structurev1.DeleteProjectRequest) (*structurev1.DeleteProjectResponse, error) {
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Projects.SoftDelete(ctx, tx, req.GetId()); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Source: audit.SourceAPI, Action: "project.delete",
			Resource: "project/" + req.GetId(),
		})
	})
	if err != nil {
		return nil, mapStateError(err, "project")
	}
	return &structurev1.DeleteProjectResponse{}, nil
}

// ---- Apps ----

type AppsService struct {
	structurev1.UnimplementedAppsServiceServer
	s *Services
}

func (svc *AppsService) CreateApp(ctx context.Context, req *structurev1.CreateAppRequest) (*structurev1.CreateAppResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	a := &app.App{ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName()}
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Apps.Create(ctx, tx, a); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Source: audit.SourceAPI, Action: "app.create",
			Resource: "app/" + a.ID, AfterFP: a.Name,
		})
	})
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	return &structurev1.CreateAppResponse{App: appMsg(a)}, nil
}

func (svc *AppsService) GetApp(ctx context.Context, req *structurev1.GetAppRequest) (*structurev1.GetAppResponse, error) {
	a, err := svc.s.Apps.Get(ctx, svc.s.DB.Runner(), req.GetId())
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	return &structurev1.GetAppResponse{App: appMsg(a)}, nil
}

func (svc *AppsService) ListApps(ctx context.Context, req *structurev1.ListAppsRequest) (*structurev1.ListAppsResponse, error) {
	list, err := svc.s.Apps.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId())
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	out := &structurev1.ListAppsResponse{}
	for i := range list {
		out.Apps = append(out.Apps, appMsg(&list[i]))
	}
	return out, nil
}

func (svc *AppsService) DeleteApp(ctx context.Context, req *structurev1.DeleteAppRequest) (*structurev1.DeleteAppResponse, error) {
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Apps.SoftDelete(ctx, tx, req.GetId()); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Source: audit.SourceAPI, Action: "app.delete",
			Resource: "app/" + req.GetId(),
		})
	})
	if err != nil {
		return nil, mapStateError(err, "app")
	}
	return &structurev1.DeleteAppResponse{}, nil
}

// ---- Secrets（值永不回显；写路径审计全落） ----

type SecretsService struct {
	structurev1.UnimplementedSecretsServiceServer
	s *Services
}

func (svc *SecretsService) PutSecret(ctx context.Context, req *structurev1.PutSecretRequest) (*structurev1.PutSecretResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	if svc.s.Cipher == nil {
		return nil, apperr.New("E_SECRET_UNAVAILABLE", "the secret facility is unavailable (no master key)")
	}
	ct, err := svc.s.Cipher.Seal([]byte(req.GetValue()))
	if err != nil {
		return nil, mapStateError(err, "secret")
	}
	row := &secret.Secret{
		ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(),
		Ciphertext: ct, Fingerprint: material.Fingerprint([]byte(req.GetValue())),
	}
	err = svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Secrets.Upsert(ctx, tx, row); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Source: audit.SourceAPI, Action: "secret.put",
			Resource: "secret/" + row.Name, AfterFP: row.Fingerprint,
		})
	})
	if err != nil {
		return nil, mapStateError(err, "secret")
	}
	return &structurev1.PutSecretResponse{Secret: secretMsg(*row)}, nil
}

func (svc *SecretsService) ListSecrets(ctx context.Context, req *structurev1.ListSecretsRequest) (*structurev1.ListSecretsResponse, error) {
	list, err := svc.s.Secrets.ListFingerprints(ctx, svc.s.DB.Runner(), req.GetProjectId())
	if err != nil {
		return nil, mapStateError(err, "secret")
	}
	out := &structurev1.ListSecretsResponse{}
	for _, row := range list {
		out.Secrets = append(out.Secrets, secretMsg(row))
	}
	return out, nil
}

func (svc *SecretsService) DeleteSecret(ctx context.Context, req *structurev1.DeleteSecretRequest) (*structurev1.DeleteSecretResponse, error) {
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Secrets.SoftDelete(ctx, tx, req.GetProjectId(), req.GetName()); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Source: audit.SourceAPI, Action: "secret.delete",
			Resource: "secret/" + req.GetName(),
		})
	})
	if err != nil {
		return nil, mapStateError(err, "secret")
	}
	return &structurev1.DeleteSecretResponse{}, nil
}

// ---- Configs（版本化可回读） ----

type ConfigsService struct {
	structurev1.UnimplementedConfigsServiceServer
	s *Services
}

func (svc *ConfigsService) PutConfig(ctx context.Context, req *structurev1.PutConfigRequest) (*structurev1.PutConfigResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	row := &configrepo.Config{ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(), Content: []byte(req.GetContent())}
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Configs.Create(ctx, tx, row); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Source: audit.SourceAPI, Action: "config.put",
			Resource: "config/" + row.Name, AfterFP: material.Fingerprint(row.Content),
		})
	})
	if err != nil {
		return nil, mapStateError(err, "config")
	}
	return &structurev1.PutConfigResponse{Config: configMsg(*row, false)}, nil
}

func (svc *ConfigsService) GetConfig(ctx context.Context, req *structurev1.GetConfigRequest) (*structurev1.GetConfigResponse, error) {
	var (
		row *configrepo.Config
		err error
	)
	if req.GetVersion() > 0 {
		row, err = svc.s.Configs.GetVersion(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetName(), req.GetVersion())
	} else {
		row, err = svc.s.Configs.Latest(ctx, svc.s.DB.Runner(), req.GetProjectId(), req.GetName())
	}
	if err != nil {
		return nil, mapStateError(err, "config")
	}
	return &structurev1.GetConfigResponse{Config: configMsg(*row, true)}, nil
}

func (svc *ConfigsService) ListConfigs(ctx context.Context, req *structurev1.ListConfigsRequest) (*structurev1.ListConfigsResponse, error) {
	// 列表面只回 Project 内最新版（版本明细随版本面扩展）。
	list, err := svc.s.Configs.LatestByProject(ctx, svc.s.DB.Runner(), req.GetProjectId())
	if err != nil {
		return nil, mapStateError(err, "config")
	}
	out := &structurev1.ListConfigsResponse{}
	for _, row := range list {
		out.Configs = append(out.Configs, configMsg(row, false))
	}
	return out, nil
}

// ---- Volumes ----

type VolumesService struct {
	structurev1.UnimplementedVolumesServiceServer
	s *Services
}

func (svc *VolumesService) CreateVolume(ctx context.Context, req *structurev1.CreateVolumeRequest) (*structurev1.CreateVolumeResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	row := &volume.Volume{
		ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(),
		PinnedNodeID: req.GetPinnedNodeId(),
	}
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Volumes.Create(ctx, tx, row); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Source: audit.SourceAPI, Action: "volume.create",
			Resource: "volume/" + row.Name, AfterFP: row.PinnedNodeID,
		})
	})
	if err != nil {
		return nil, mapStateError(err, "volume")
	}
	return &structurev1.CreateVolumeResponse{Volume: volumeMsg(*row)}, nil
}

func (svc *VolumesService) ListVolumes(ctx context.Context, req *structurev1.ListVolumesRequest) (*structurev1.ListVolumesResponse, error) {
	list, err := svc.s.Volumes.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId())
	if err != nil {
		return nil, mapStateError(err, "volume")
	}
	out := &structurev1.ListVolumesResponse{}
	for _, row := range list {
		out.Volumes = append(out.Volumes, volumeMsg(row))
	}
	return out, nil
}

// ---- Networks ----

type NetworksService struct {
	structurev1.UnimplementedNetworksServiceServer
	s *Services
}

func (svc *NetworksService) CreateNetwork(ctx context.Context, req *structurev1.CreateNetworkRequest) (*structurev1.CreateNetworkResponse, error) {
	if req.GetProjectId() == "" || req.GetName() == "" {
		return nil, apperr.New("E_INVALID_ARGUMENT", "project_id and name: must not be empty")
	}
	row := &networkrepo.Network{
		ID: newID(), ProjectID: req.GetProjectId(), Name: req.GetName(), EgressNone: req.GetEgressNone(),
	}
	err := svc.s.DB.Tx(ctx, func(tx *sql.Tx) error {
		if err := svc.s.Networks.Create(ctx, tx, row); err != nil {
			return err
		}
		return svc.s.Audits.Append(ctx, tx, &audit.Entry{
			ID: newID(), Source: audit.SourceAPI, Action: "network.create",
			Resource: "network/" + row.Name, AfterFP: req.String(),
		})
	})
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	return &structurev1.CreateNetworkResponse{Network: networkMsg(*row)}, nil
}

func (svc *NetworksService) ListNetworks(ctx context.Context, req *structurev1.ListNetworksRequest) (*structurev1.ListNetworksResponse, error) {
	list, err := svc.s.Networks.ListByProject(ctx, svc.s.DB.Runner(), req.GetProjectId())
	if err != nil {
		return nil, mapStateError(err, "network")
	}
	out := &structurev1.ListNetworksResponse{}
	for _, row := range list {
		out.Networks = append(out.Networks, networkMsg(row))
	}
	return out, nil
}
