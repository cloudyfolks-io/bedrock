package operator

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const wantBlockImageFeatures = "layering,exclusive-lock,object-map,fast-diff,deep-flatten"

func TestBlockClassesCarryTheImageFeatures(t *testing.T) {
	for _, class := range []*unstructured.Unstructured{blockStorageClass("block", "Delete", true), blockStorageClass("block-retain", "Retain", false)} {
		features, _, _ := unstructured.NestedString(class.Object, "parameters", "imageFeatures")
		if features != wantBlockImageFeatures {
			t.Fatalf("%s imageFeatures %q", class.GetName(), features)
		}
	}
}

func legacyBlockClass(t *testing.T, c client.Client) {
	t.Helper()
	legacyBlockClassOwnedBy(t, c, v1alpha1.OperatorFieldManager)
}

func legacyBlockClassOwnedBy(t *testing.T, c client.Client, manager string) {
	t.Helper()
	reclaim := corev1.PersistentVolumeReclaimDelete
	binding := storagev1.VolumeBindingImmediate
	expansion := true
	legacy := &storagev1.StorageClass{
		ObjectMeta:           metav1.ObjectMeta{Name: "block", Annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"}},
		Provisioner:          rbdProvisioner,
		Parameters:           map[string]string{"clusterID": storageNamespace, "pool": blockPoolName, "imageFormat": "2", "imageFeatures": "layering"},
		ReclaimPolicy:        &reclaim,
		VolumeBindingMode:    &binding,
		AllowVolumeExpansion: &expansion,
	}
	if err := c.Create(context.Background(), legacy, client.FieldOwner(manager)); err != nil {
		t.Fatal(err)
	}
}

func TestApplyReplacesAStorageClassWhoseParametersChanged(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	legacyBlockClass(t, c)
	var before storagev1.StorageClass
	if err := c.Get(ctx, client.ObjectKey{Name: "block"}, &before); err != nil {
		t.Fatal(err)
	}

	reconciler := &AddonReconciler{Client: c}
	addon := Addon{Name: "storage", Condition: "StorageReady"}
	rendered := Rendered{Objects: []*unstructured.Unstructured{blockStorageClass("block", "Delete", true)}}
	condition, applied := reconciler.applyRendered(ctx, addon, rendered)
	if !applied {
		t.Fatalf("apply failed: %+v", condition)
	}

	var after storagev1.StorageClass
	if err := c.Get(ctx, client.ObjectKey{Name: "block"}, &after); err != nil {
		t.Fatal(err)
	}
	if after.Parameters["imageFeatures"] != wantBlockImageFeatures {
		t.Fatalf("parameters %+v", after.Parameters)
	}
	if after.Annotations["storageclass.kubernetes.io/is-default-class"] != "true" {
		t.Fatalf("annotations %+v", after.Annotations)
	}
	if after.UID == before.UID {
		t.Fatal("a class with new parameters must be replaced, not patched")
	}
}

func TestApplyKeepsAStorageClassWhoseParametersAreEqual(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	reconciler := &AddonReconciler{Client: c}
	addon := Addon{Name: "storage", Condition: "StorageReady"}
	rendered := Rendered{Objects: []*unstructured.Unstructured{blockStorageClass("block", "Delete", true)}}
	if _, applied := reconciler.applyRendered(ctx, addon, rendered); !applied {
		t.Fatal("first apply failed")
	}
	var first storagev1.StorageClass
	if err := c.Get(ctx, client.ObjectKey{Name: "block"}, &first); err != nil {
		t.Fatal(err)
	}
	if _, applied := reconciler.applyRendered(ctx, addon, rendered); !applied {
		t.Fatal("second apply failed")
	}
	var second storagev1.StorageClass
	if err := c.Get(ctx, client.ObjectKey{Name: "block"}, &second); err != nil {
		t.Fatal(err)
	}
	if first.UID != second.UID {
		t.Fatal("an unchanged class must not be replaced")
	}
}

func TestReplacingAStorageClassKeepsExistingVolumes(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	legacyBlockClass(t, c)
	class := "block"
	volume := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pvc-existing"},
		Spec: corev1.PersistentVolumeSpec{
			Capacity:         corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: class,
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				Driver:           rbdProvisioner,
				VolumeHandle:     "handle",
				VolumeAttributes: map[string]string{"imageFeatures": "layering", "pool": blockPoolName},
			}},
		},
	}
	if err := c.Create(ctx, volume); err != nil {
		t.Fatal(err)
	}

	reconciler := &AddonReconciler{Client: c}
	addon := Addon{Name: "storage", Condition: "StorageReady"}
	rendered := Rendered{Objects: []*unstructured.Unstructured{blockStorageClass("block", "Delete", true)}}
	if _, applied := reconciler.applyRendered(ctx, addon, rendered); !applied {
		t.Fatal("apply failed")
	}

	var kept corev1.PersistentVolume
	if err := c.Get(ctx, client.ObjectKey{Name: "pvc-existing"}, &kept); err != nil {
		t.Fatal(err)
	}
	if kept.Spec.CSI.VolumeAttributes["imageFeatures"] != "layering" || kept.Spec.StorageClassName != class {
		t.Fatalf("existing volume changed: %+v", kept.Spec)
	}
}

func renderedStorageClasses() []*unstructured.Unstructured {
	var classes []*unstructured.Unstructured
	for _, obj := range storageObjects(1, []v1alpha1.Host{osdHost("a", "/dev/sdb")}) {
		if obj.GetKind() == "StorageClass" {
			classes = append(classes, obj)
		}
	}
	return classes
}

func TestApplyKeepsEveryRenderedStorageClass(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	reconciler := &AddonReconciler{Client: c}
	addon := Addon{Name: "storage", Condition: "StorageReady"}
	classes := renderedStorageClasses()
	if len(classes) != 4 {
		t.Fatalf("want 4 rendered classes, got %d", len(classes))
	}
	rendered := Rendered{Objects: classes}
	if condition, applied := reconciler.applyRendered(ctx, addon, rendered); !applied {
		t.Fatalf("first apply failed: %+v", condition)
	}
	first := map[string]storagev1.StorageClass{}
	for _, class := range classes {
		var live storagev1.StorageClass
		if err := c.Get(ctx, client.ObjectKey{Name: class.GetName()}, &live); err != nil {
			t.Fatal(err)
		}
		first[class.GetName()] = live
	}
	for range 2 {
		if condition, applied := reconciler.applyRendered(ctx, addon, rendered); !applied {
			t.Fatalf("repeat apply failed: %+v", condition)
		}
	}
	for name, before := range first {
		var after storagev1.StorageClass
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &after); err != nil {
			t.Fatal(err)
		}
		if after.UID != before.UID {
			t.Fatalf("class %s was replaced by a repeat apply", name)
		}
	}
}

func TestApplyNeverReplacesAForeignStorageClass(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()
	legacyBlockClassOwnedBy(t, c, "someone-else")
	var before storagev1.StorageClass
	if err := c.Get(ctx, client.ObjectKey{Name: "block"}, &before); err != nil {
		t.Fatal(err)
	}

	reconciler := &AddonReconciler{Client: c}
	addon := Addon{Name: "storage", Condition: "StorageReady"}
	rendered := Rendered{Objects: []*unstructured.Unstructured{blockStorageClass("block", "Delete", true)}}
	condition, applied := reconciler.applyRendered(ctx, addon, rendered)
	if applied || condition.Reason != "ApplyFailed" || !strings.Contains(condition.Message, "not owned") {
		t.Fatalf("condition %+v applied %v", condition, applied)
	}

	var after storagev1.StorageClass
	if err := c.Get(ctx, client.ObjectKey{Name: "block"}, &after); err != nil {
		t.Fatal(err)
	}
	if after.UID != before.UID || after.Parameters["imageFeatures"] != "layering" {
		t.Fatalf("foreign class changed: %+v", after.Parameters)
	}
}
