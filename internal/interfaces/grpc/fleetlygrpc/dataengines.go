package fleetlygrpc

// DataEnginesService 实现（B2c，ADR-0094 决策 4 / ADR-0095 决策 10；design
// §2.3）：Engine Catalog——服务端从 dbtemplate 注册表现场装配的跨家族
// 引擎/版本/旋钮自描述投影（零缓存零漂移；Console/CLI 引擎取值唯一面）。
// B5 起装配三家族（database=dbtemplate.Catalog + pubsub=pubtemplate.
// Catalog + bucketstore=buckettemplate.Catalog，按家族再按引擎名序）——
// 三目录形状同形，在本面折成 proto EngineCatalog 单形。
//
// scope 裁定（B2c 探明）：authz 面（authn.policyview/interceptor）只承认
// PUBLIC-anonymous 与 SERVER-single-scope 两规范形态——「已认证、免资源
// scope」第三形态不可表达（服务级 authenticated 绊线树形在 ViewRules
// 构造期即拒）。按 ADR-0094 决策 4 降级 `databases:read`（proto 注解与
// 本文件同批注明；决策回注由编排者做）。

import (
	"context"

	structurev1 "github.com/fleetlyrun/fleetly/genproto/fleetly/structure/v1"
	"github.com/fleetlyrun/fleetly/internal/domain/services/buckettemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/dbtemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/enginetemplate"
	"github.com/fleetlyrun/fleetly/internal/domain/services/pubtemplate"
)

// DataEnginesService 是目录读面（READ 桶；无行级授权面——目录非资源行，
// scope 门即全部）。
type DataEnginesService struct {
	structurev1.UnimplementedDataEnginesServiceServer
	s *Services
}

// ListDataEngines 现场装配目录（每请求从注册表重投影——注册表常量即目录
// 真源，无缓存失效面；镜像 digest 不出目录——三层解耦的平台层，ADR-0094
// 决策 1）。
func (svc *DataEnginesService) ListDataEngines(ctx context.Context, req *structurev1.ListDataEnginesRequest) (*structurev1.ListDataEnginesResponse, error) {
	out := &structurev1.ListDataEnginesResponse{}
	for _, e := range dbtemplate.Catalog() {
		out.Engines = append(out.Engines, catalogMsg(e.Family, e.Engine, e.Dialect, e.Port, e.Versions, e.Knobs,
			e.Capabilities.Backup, e.Capabilities.Migrate, e.Capabilities.Browse, e.Capabilities.RestoreCompat, e.LicenseNote, ""))
	}
	for _, e := range pubtemplate.Catalog() {
		out.Engines = append(out.Engines, catalogMsg(e.Family, e.Engine, e.Dialect, e.Port, e.Versions, e.Knobs,
			e.Capabilities.Backup, e.Capabilities.Migrate, e.Capabilities.Browse, e.Capabilities.RestoreCompat, e.LicenseNote, e.Note))
	}
	for _, e := range buckettemplate.Catalog() {
		out.Engines = append(out.Engines, catalogMsg(e.Family, e.Engine, e.Dialect, e.Port, e.Versions, e.Knobs,
			e.Capabilities.Backup, e.Capabilities.Migrate, e.Capabilities.Browse, e.Capabilities.RestoreCompat, e.LicenseNote, ""))
	}
	return out, nil
}

// catalogMsg 折三家族目录行为 proto EngineCatalog（形状同形——参数展开
// 而非跨包共享结构体：模板包互不 import，目录行类型各自持有）。
func catalogMsg(family, engine, dialect string, port int32, versions []enginetemplate.VersionSpec, knobs []enginetemplate.Knob,
	backup, migrate, browse, restoreCompat bool, licenseNote, note string) *structurev1.EngineCatalog {
	catalog := &structurev1.EngineCatalog{
		Family:  family,
		Engine:  engine,
		Dialect: dialect,
		Port:    port,
		Knobs:   knobsMsg(knobs),
		Capabilities: &structurev1.EngineCapabilities{
			Backup:        backup,
			Migrate:       migrate,
			Browse:        browse,
			RestoreCompat: restoreCompat,
		},
		LicenseNote: licenseNote,
		Note:        note,
	}
	for _, v := range versions {
		catalog.Versions = append(catalog.Versions, &structurev1.EngineVersionInfo{
			Id: v.ID, Default: v.Default, Deprecated: v.Deprecated, Note: v.Note,
		})
	}
	return catalog
}

// knobsMsg 旋钮 schema → proto 映射（kind/class 以字符串值域承载——新
// kind 不需 proto 枚举翻面；值域真源 enginetemplate.Knob* 常量）。
func knobsMsg(knobs []enginetemplate.Knob) []*structurev1.EngineKnob {
	if len(knobs) == 0 {
		return nil
	}
	out := make([]*structurev1.EngineKnob, 0, len(knobs))
	for _, k := range knobs {
		out = append(out, &structurev1.EngineKnob{
			Key: k.Key, Kind: string(k.Kind), Min: k.Min, Max: k.Max,
			EnumValues:   append([]string(nil), k.Enum...),
			DefaultValue: k.Default, Class: string(k.Class), Category: k.Category,
		})
	}
	return out
}
