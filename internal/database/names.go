package database

import "k8s.io/apimachinery/pkg/types"

const (
	PlatformNamespace = "bedrock-dbaas"
	Kind              = "Database"
	QuotaName         = "databases"
	EnginesPolicy     = "engines"

	LabelManagedBy  = "app.kubernetes.io/managed-by"
	LabelInstance   = "app.kubernetes.io/instance"
	LabelComponent  = "app.kubernetes.io/component"
	ManagedBy       = "bedrock"
	ComponentGate   = "gate"
	ComponentEngine = "engine"
)

func Name(uid types.UID) string {
	return string(uid)
}

func Suffixed(uid types.UID, suffix string) string {
	return string(uid) + "-" + suffix
}
