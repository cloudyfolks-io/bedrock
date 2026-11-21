package release

import (
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func VerifyFabricPair(bundle Bundle, expectedImage string) error {
	repository, digest, pinned := strings.Cut(expectedImage, "@sha256:")
	if !pinned || repository == "" || strings.Contains(repository, "@") || !validImageDigest(digest) {
		return fmt.Errorf("use the exact Fabric image reference with its SHA-256 digest")
	}
	if !slices.Contains(bundle.Images, expectedImage) || !slices.Contains(bundle.Spec.Images, expectedImage) {
		return fmt.Errorf("the release inventories do not contain Fabric image %q", expectedImage)
	}
	var crd, controller *unstructured.Unstructured
	var crdOrder, controllerOrder int
	for _, group := range bundle.Groups {
		for _, obj := range group.Objects {
			switch {
			case obj.GetKind() == "CustomResourceDefinition" && obj.GetName() == "security-groups.fabric.cloudyfolks.io":
				if crd != nil || group.Name != "crds" {
					return fmt.Errorf("the release must contain one Fabric SecurityGroup CRD in the crds group")
				}
				crd, crdOrder = obj, group.Order
			case obj.GetKind() == "Deployment" && obj.GetNamespace() == "kube-system" && obj.GetName() == "fabric-controller":
				if controller != nil || group.Name != "fabric" {
					return fmt.Errorf("the release must contain one Fabric controller in the fabric group")
				}
				controller, controllerOrder = obj, group.Order
			}
		}
	}
	if crd == nil || controller == nil || controller.GetAPIVersion() != "apps/v1" || crdOrder >= controllerOrder {
		return fmt.Errorf("the release must install the Fabric SecurityGroup CRD before the controller")
	}
	if !fabricSecurityGroupReceiptSchema(crd) {
		return fmt.Errorf("the Fabric SecurityGroup CRD lacks the v1 ACL generation receipt")
	}
	containers, _, _ := unstructured.NestedSlice(controller.Object, "spec", "template", "spec", "containers")
	matches := 0
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok || container["name"] != "fabric-controller" {
			continue
		}
		if container["image"] != expectedImage {
			return fmt.Errorf("the Fabric controller does not use the expected image %q", expectedImage)
		}
		matches++
	}
	if matches != 1 {
		return fmt.Errorf("the release must contain one fabric-controller container")
	}
	return nil
}

func fabricSecurityGroupReceiptSchema(crd *unstructured.Unstructured) bool {
	if crd.GetAPIVersion() != "apiextensions.k8s.io/v1" {
		return false
	}
	versions, _, _ := unstructured.NestedSlice(crd.Object, "spec", "versions")
	for _, item := range versions {
		version, ok := item.(map[string]any)
		if !ok || version["name"] != "v1" {
			continue
		}
		if version["served"] != true || version["storage"] != true {
			return false
		}
		if _, enabled, err := unstructured.NestedMap(version, "subresources", "status"); err != nil || !enabled {
			return false
		}
		status, _, _ := unstructured.NestedMap(version, "schema", "openAPIV3Schema", "properties", "status", "properties")
		generation, _, _ := unstructured.NestedString(status, "observedGeneration", "type")
		ingress, _, _ := unstructured.NestedString(status, "ingressLastSyncSuccess", "type")
		egress, _, _ := unstructured.NestedString(status, "egressLastSyncSuccess", "type")
		return generation == "integer" && ingress == "boolean" && egress == "boolean"
	}
	return false
}
