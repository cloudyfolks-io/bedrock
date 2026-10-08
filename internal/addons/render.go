package addons

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

var workloadKinds = map[string]bool{"Deployment": true, "StatefulSet": true, "DaemonSet": true}

func Render(addon string, objects []*unstructured.Unstructured, vars map[string]string) ([]*unstructured.Unstructured, error) {
	copies := make([]*unstructured.Unstructured, 0, len(objects))
	for _, obj := range objects {
		copies = append(copies, obj.DeepCopy())
	}
	substituted, err := release.Substitute(release.Bundle{Groups: []release.Group{{Name: addon, Objects: copies}}}, vars)
	if err != nil {
		return nil, err
	}
	rendered := make([]*unstructured.Unstructured, 0, len(copies))
	for _, obj := range substituted.Groups[0].Objects {
		rendered = append(rendered, withAddonLabels(obj.DeepCopy(), addon))
	}
	for _, obj := range rendered {
		if workloadKinds[obj.GetKind()] {
			if err := setConfigHash(obj, rendered); err != nil {
				return nil, err
			}
		}
	}
	return rendered, nil
}

func withAddonLabels(obj *unstructured.Unstructured, addon string) *unstructured.Unstructured {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[v1alpha1.LabelKind] = "Addon"
	labels[v1alpha1.LabelName] = addon
	obj.SetLabels(labels)
	return obj
}

func setConfigHash(workload *unstructured.Unstructured, all []*unstructured.Unstructured) error {
	hash := configHash(workload, all)
	annotations, _, _ := unstructured.NestedStringMap(workload.Object, "spec", "template", "metadata", "annotations")
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[AnnotationConfigHash] = hash
	return unstructured.SetNestedStringMap(workload.Object, annotations, "spec", "template", "metadata", "annotations")
}

func configHash(workload *unstructured.Unstructured, all []*unstructured.Unstructured) string {
	names := mountedConfigMaps(workload)
	sort.Strings(names)
	digest := sha256.New()
	for _, name := range names {
		fmt.Fprintf(digest, "%s\x00", name)
		for _, config := range all {
			if config.GetKind() != "ConfigMap" || config.GetName() != name || config.GetNamespace() != workload.GetNamespace() {
				continue
			}
			data, _, _ := unstructured.NestedStringMap(config.Object, "data")
			keys := make([]string, 0, len(data))
			for key := range data {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				fmt.Fprintf(digest, "%s\x00%s\x00", key, data[key])
			}
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func mountedConfigMaps(workload *unstructured.Unstructured) []string {
	volumes, _, _ := unstructured.NestedSlice(workload.Object, "spec", "template", "spec", "volumes")
	var names []string
	for _, item := range volumes {
		volume, _ := item.(map[string]any)
		config, _ := volume["configMap"].(map[string]any)
		if name, _ := config["name"].(string); name != "" {
			names = append(names, name)
		}
	}
	return names
}
