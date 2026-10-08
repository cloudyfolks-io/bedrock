package compute

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func claimVM() v1alpha1.VirtualMachine {
	vm := fixtureVM("acme-web", "web-1")
	vm.UID = types.UID("vm-uid")
	return vm
}

func claimLabels() map[string]string {
	return v1alpha1.TenantLabels("VirtualMachine", "web-1", "acme", "web")
}

func TestResolveImage(t *testing.T) {
	ready := v1alpha1.ImageStatus{Phase: v1alpha1.ImagePhaseReady, DataSource: "ubuntu-24-04", SizeBytes: 3 << 30}
	got, err := ResolveImage(v1alpha1.ImageSpec{}, ready, "bedrock-images", v1alpha1.ImageTypeDisk)
	if err != nil || got != (ResolvedImage{Namespace: "bedrock-images", DataSource: "ubuntu-24-04", SizeGi: 3}) {
		t.Fatalf("%+v %v", got, err)
	}
	cases := map[string]struct {
		spec   v1alpha1.ImageSpec
		status v1alpha1.ImageStatus
		want   string
		reason string
	}{
		"importing":      {v1alpha1.ImageSpec{}, v1alpha1.ImageStatus{Phase: v1alpha1.ImagePhaseImporting, DataSource: "x"}, v1alpha1.ImageTypeDisk, ReasonImageNotReady},
		"no data source": {v1alpha1.ImageSpec{}, v1alpha1.ImageStatus{Phase: v1alpha1.ImagePhaseReady}, v1alpha1.ImageTypeDisk, ReasonImageNotReady},
		"iso as disk":    {v1alpha1.ImageSpec{Type: v1alpha1.ImageTypeISO}, ready, v1alpha1.ImageTypeDisk, ReasonImageWrongType},
		"disk as iso":    {v1alpha1.ImageSpec{Type: v1alpha1.ImageTypeDisk}, ready, v1alpha1.ImageTypeISO, ReasonImageWrongType},
		"default as iso": {v1alpha1.ImageSpec{}, ready, v1alpha1.ImageTypeISO, ReasonImageWrongType},
	}
	for name, tc := range cases {
		_, err := ResolveImage(tc.spec, tc.status, "ns", tc.want)
		imageErr, ok := err.(ImageError)
		if !ok || imageErr.Reason != tc.reason {
			t.Errorf("%s: %v", name, err)
		}
	}
	sizes := []struct {
		bytes int64
		spec  int32
		want  int32
	}{{3221225472, 0, 3}, {3221225473, 0, 4}, {0, 20, 20}, {0, 0, 0}}
	for _, tc := range sizes {
		got, _ := ResolveImage(v1alpha1.ImageSpec{SizeGi: tc.spec}, v1alpha1.ImageStatus{Phase: v1alpha1.ImagePhaseReady, DataSource: "x", SizeBytes: tc.bytes}, "ns", v1alpha1.ImageTypeDisk)
		if got.SizeGi != tc.want {
			t.Errorf("%d/%d gave %d want %d", tc.bytes, tc.spec, got.SizeGi, tc.want)
		}
	}
}

func TestRootClaimFromDataSource(t *testing.T) {
	vm := claimVM()
	claim := RootClaim(vm, claimLabels(), &ResolvedImage{Namespace: "bedrock-images", DataSource: "ubuntu-24-04", SizeGi: 3})
	if claim.GetAPIVersion() != "cdi.kubevirt.io/v1beta1" || claim.GetKind() != "DataVolume" || claim.GetName() != "web-1-root" || claim.GetNamespace() != "acme-web" {
		t.Fatalf("%+v", claim.Object)
	}
	ref, _, _ := unstructured.NestedMap(claim.Object, "spec", "sourceRef")
	if !reflect.DeepEqual(ref, map[string]any{"kind": "DataSource", "name": "ubuntu-24-04", "namespace": "bedrock-images"}) {
		t.Fatalf("sourceRef %+v", ref)
	}
	size, _, _ := unstructured.NestedString(claim.Object, "spec", "storage", "resources", "requests", "storage")
	if _, found, _ := unstructured.NestedString(claim.Object, "spec", "storage", "storageClassName"); size != "40Gi" || found {
		t.Fatalf("storage %+v", claim.Object["spec"])
	}
	vm.Spec.RootDisk.StorageClass = "block-retain"
	withClass := RootClaim(vm, claimLabels(), &ResolvedImage{Namespace: "acme-web", DataSource: "mine"})
	if class, _, _ := unstructured.NestedString(withClass.Object, "spec", "storage", "storageClassName"); class != "block-retain" {
		t.Fatalf("class %q", class)
	}
	if ns, _, _ := unstructured.NestedString(withClass.Object, "spec", "sourceRef", "namespace"); ns != "acme-web" {
		t.Fatalf("namespace %q", ns)
	}
}

func TestRootClaimBlankWithoutImage(t *testing.T) {
	claim := RootClaim(claimVM(), claimLabels(), nil)
	if _, found, _ := unstructured.NestedMap(claim.Object, "spec", "source", "blank"); !found {
		t.Fatalf("%+v", claim.Object["spec"])
	}
	if _, found, _ := unstructured.NestedMap(claim.Object, "spec", "sourceRef"); found {
		t.Fatal("a blank claim has no sourceRef")
	}
}

func TestRootClaimAllowsClaimAdoption(t *testing.T) {
	claim := RootClaim(claimVM(), claimLabels(), nil)
	if claim.GetAnnotations()["cdi.kubevirt.io/allowClaimAdoption"] != "true" {
		t.Fatalf("%+v", claim.GetAnnotations())
	}
}

func TestRootClaimOwnerAndLabels(t *testing.T) {
	vm := claimVM()
	claim := RootClaim(vm, claimLabels(), nil)
	if !reflect.DeepEqual(claim.GetLabels(), claimLabels()) {
		t.Fatalf("%+v", claim.GetLabels())
	}
	refs := claim.GetOwnerReferences()
	if len(refs) != 1 || !reflect.DeepEqual(refs[0], OwnerRef(vm)) {
		t.Fatalf("%+v", refs)
	}
}

func TestRootDiskIssue(t *testing.T) {
	issue := RootDiskIssue(3, &ResolvedImage{SizeGi: 4})
	if issue == nil || issue.Reason != "RootDiskTooSmall" {
		t.Fatalf("%+v", issue)
	}
	if RootDiskIssue(4, &ResolvedImage{SizeGi: 4}) != nil || RootDiskIssue(1, nil) != nil {
		t.Fatal("a fitting or absent image gives no issue")
	}
}

func TestCDROMClaim(t *testing.T) {
	claim := CDROMClaim(claimVM(), claimLabels(), 1, ResolvedImage{Namespace: "bedrock-images", DataSource: "iso-1", SizeGi: 0})
	if claim.GetName() != "web-1-cdrom-1" {
		t.Fatal(claim.GetName())
	}
	size, _, _ := unstructured.NestedString(claim.Object, "spec", "storage", "resources", "requests", "storage")
	name, _, _ := unstructured.NestedString(claim.Object, "spec", "sourceRef", "name")
	annotations := claim.GetAnnotations()
	if size != "1Gi" || name != "iso-1" || annotations["cdi.kubevirt.io/allowClaimAdoption"] != "true" || annotations["bedrock.cloudyfolks.io/attach"] != "read-only" {
		t.Fatalf("%+v", claim.Object)
	}
	if !reflect.DeepEqual(claim.GetLabels(), claimLabels()) || len(claim.GetOwnerReferences()) != 1 {
		t.Fatalf("%+v", claim.Object["metadata"])
	}
}
