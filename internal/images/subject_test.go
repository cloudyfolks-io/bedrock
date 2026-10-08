package images

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const testUID = types.UID("11111111-2222-3333-4444-555555555555")

func ubuntuImage() v1alpha1.Image {
	return v1alpha1.Image{
		ObjectMeta: metav1.ObjectMeta{Name: "ubuntu", Namespace: "acme-web", UID: testUID},
		Spec: v1alpha1.ImageSpec{
			Source: v1alpha1.ImageSource{Registry: &v1alpha1.ImageRegistrySource{Image: "quay.io/containerdisks/ubuntu:24.04"}},
			SizeGi: 10,
		},
	}
}

func TestSubjectOfImageLabelsAndOwner(t *testing.T) {
	s := SubjectOfImage(ubuntuImage(), "acme", "web", false)
	want := map[string]string{
		"bedrock.cloudyfolks.io/kind":    "Image",
		"bedrock.cloudyfolks.io/name":    "ubuntu",
		"bedrock.cloudyfolks.io/tenant":  "acme",
		"bedrock.cloudyfolks.io/project": "web",
	}
	if len(s.Labels) != len(want) {
		t.Fatalf("labels %v", s.Labels)
	}
	for k, v := range want {
		if s.Labels[k] != v {
			t.Fatalf("label %s = %q, want %q", k, s.Labels[k], v)
		}
	}
	if s.Owner.Controller == nil || !*s.Owner.Controller || s.Owner.BlockOwnerDeletion == nil || !*s.Owner.BlockOwnerDeletion {
		t.Fatalf("owner must be a blocking controller reference: %+v", s.Owner)
	}
	if s.Owner.Kind != "Image" || s.Owner.UID != testUID || s.Owner.APIVersion != "bedrock.cloudyfolks.io/v1alpha1" {
		t.Fatalf("owner %+v", s.Owner)
	}
	if s.Namespace != "acme-web" || s.Kind != "Image" {
		t.Fatalf("subject %+v", s)
	}
}

func TestSubjectOfImageWithoutTenantOmitsTheLabels(t *testing.T) {
	s := SubjectOfImage(ubuntuImage(), "", "", false)
	if _, found := s.Labels["bedrock.cloudyfolks.io/tenant"]; found {
		t.Fatalf("labels %v", s.Labels)
	}
	if _, found := s.Labels["bedrock.cloudyfolks.io/project"]; found {
		t.Fatalf("labels %v", s.Labels)
	}
}

func TestSubjectOfClusterImageUsesTheCatalogNamespace(t *testing.T) {
	img := v1alpha1.ClusterImage{ObjectMeta: metav1.ObjectMeta{Name: "ubuntu-24.04", UID: testUID}, Spec: ubuntuImage().Spec}
	s := SubjectOfClusterImage(img, true)
	if s.Namespace != "bedrock-images" || s.Kind != "ClusterImage" || !s.ExtraCA {
		t.Fatalf("subject %+v", s)
	}
	if len(s.Labels) != 2 || s.Labels["bedrock.cloudyfolks.io/kind"] != "ClusterImage" || s.Labels["bedrock.cloudyfolks.io/name"] != "ubuntu-24.04" {
		t.Fatalf("labels %v", s.Labels)
	}
	if s.Owner.Kind != "ClusterImage" {
		t.Fatalf("owner %+v", s.Owner)
	}
}
