package settings

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func Effective(ctx context.Context, c client.Reader) (map[string]string, error) {
	var list v1alpha1.SettingList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, def := range Catalog() {
		values[def.Key] = def.Default
	}
	for _, setting := range list.Items {
		if setting.Spec.Value != "" {
			values[setting.Name] = setting.Spec.Value
		}
	}
	return values, nil
}
