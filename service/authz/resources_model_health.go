package authz

const ResourceModelHealth = "model_health"

var (
	ModelHealthProbe  = Permission{Resource: ResourceModelHealth, Action: "probe"}
	ModelHealthRules  = Permission{Resource: ResourceModelHealth, Action: "rules"}
	ModelHealthNotify = Permission{Resource: ResourceModelHealth, Action: "notify"}
	ModelHealthExport = Permission{Resource: ResourceModelHealth, Action: "export"}
)

func init() {
	RegisterResource(ResourceDefinition{Resource: ResourceModelHealth, LabelKey: "Model Health", Actions: []ActionDefinition{
		{Action: ModelHealthProbe.Action, LabelKey: "Run model health checks"},
		{Action: ModelHealthRules.Action, LabelKey: "Manage model health rules"},
		{Action: ModelHealthNotify.Action, LabelKey: "Manage model health notifications"},
		{Action: ModelHealthExport.Action, LabelKey: "Export model health evidence"},
	}})
}
