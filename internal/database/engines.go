package database

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/mysql"
)

var (
	PodMonitorGVK = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "PodMonitor"}

	engineAddons = map[string]string{
		v1alpha1.DatabaseEnginePostgreSQL: "cnpg",
		v1alpha1.DatabaseEngineMySQL:      "percona-ps",
		v1alpha1.DatabaseEngineValkey:     "",
	}
)

func additionalEngineGVKs() []schema.GroupVersionKind {
	return append(mysql.GVKs(), PodMonitorGVK)
}

func engineAddon(obj client.Object) bool {
	for _, name := range engineAddons {
		if name != "" && obj.GetName() == name {
			return true
		}
	}
	return false
}

func engineAvailable(ctx context.Context, c client.Reader, engine string) (bool, error) {
	name, known := engineAddons[engine]
	if !known {
		return false, nil
	}
	if name == "" {
		return true, nil
	}
	var addon v1alpha1.Addon
	err := c.Get(ctx, client.ObjectKey{Name: name}, &addon)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil && addon.Spec.Enabled, err
}
