package database

import (
	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
)

const (
	PlatformNamespace = v1alpha1.DatabasePlatformNamespace
	Kind              = "Database"
	QuotaName         = v1alpha1.DatabaseQuotaName
	EnginesPolicy     = "engines"

	LabelManagedBy  = "app.kubernetes.io/managed-by"
	LabelInstance   = "app.kubernetes.io/instance"
	LabelComponent  = "app.kubernetes.io/component"
	ManagedBy       = "bedrock"
	ComponentGate   = "gate"
	ComponentEngine = "engine"
	LabelRole       = "bedrock.cloudyfolks.io/database-role"
	RoleEngine      = "engine"
)

func Name(uid types.UID) string {
	return "db-" + string(uid)
}

func Suffixed(uid types.UID, suffix string) string {
	return Name(uid) + "-" + suffix
}
