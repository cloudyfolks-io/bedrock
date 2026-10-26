package operator

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

var (
	configMapGVK  = schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}
	deploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
)

func validateSettings(values map[string]string, keys ...string) error {
	for _, key := range keys {
		definition, _ := settings.Lookup(key)
		if err := definition.Validate(values[key]); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return nil
}

func bundleObject(bundle release.Bundle, group string, gvk schema.GroupVersionKind, key client.ObjectKey, vars map[string]string) (*unstructured.Unstructured, bool, error) {
	for _, candidate := range bundle.Groups {
		if candidate.Name != group {
			continue
		}
		for _, obj := range candidate.Objects {
			if obj.GroupVersionKind() != gvk || obj.GetNamespace() != key.Namespace || obj.GetName() != key.Name {
				continue
			}
			substituted, err := release.Substitute(release.Bundle{Groups: []release.Group{{Name: group, Objects: []*unstructured.Unstructured{obj}}}}, vars)
			if err != nil {
				return nil, false, err
			}
			return substituted.Groups[0].Objects[0], true, nil
		}
	}
	return nil, false, nil
}
