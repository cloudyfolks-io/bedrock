package platform

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var widgetGVK = schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "Widget"}

func widget(namespace, name string, labels map[string]string, annotations map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"name": name, "namespace": namespace}}}
	obj.SetGroupVersionKind(widgetGVK)
	obj.SetLabels(labels)
	obj.SetAnnotations(annotations)
	return obj
}

func TestLabels(t *testing.T) {
	got := Labels("ObjectStorageUser", "app", "acme", "prod")
	want := map[string]string{
		"bedrock.cloudyfolks.io/kind":    "ObjectStorageUser",
		"bedrock.cloudyfolks.io/name":    "app",
		"bedrock.cloudyfolks.io/tenant":  "acme",
		"bedrock.cloudyfolks.io/project": "prod",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
}

func TestFinalizer(t *testing.T) {
	cases := map[string]string{
		"ObjectStorageUser":   "bedrock.cloudyfolks.io/object-storage-user",
		"ObjectStorageBucket": "bedrock.cloudyfolks.io/object-storage-bucket",
		"Database":            "bedrock.cloudyfolks.io/database",
	}
	for kind, want := range cases {
		if got := Finalizer(kind); got != want {
			t.Fatalf("%s: got %q", kind, got)
		}
	}
}

func TestAdoptSetsLabelsAndOwnerWithoutOwnerReference(t *testing.T) {
	input := widget("rook-ceph", "acme-prod.app", map[string]string{"keep": "me"}, nil)
	owner := Owner{Kind: "ObjectStorageUser", Name: "app", Namespace: "acme-prod", UID: types.UID("uid-1"), Tenant: "acme", Project: "prod"}
	got := Adopt(input, owner)
	if got.GetLabels()["keep"] != "me" || got.GetLabels()["bedrock.cloudyfolks.io/kind"] != "ObjectStorageUser" || got.GetLabels()["bedrock.cloudyfolks.io/project"] != "prod" {
		t.Fatalf("labels %+v", got.GetLabels())
	}
	if len(got.GetOwnerReferences()) != 0 {
		t.Fatalf("owner references %+v", got.GetOwnerReferences())
	}
	if back := OwnerOf(got); back != owner {
		t.Fatalf("owner %+v", back)
	}
	if _, touched := input.GetLabels()["bedrock.cloudyfolks.io/kind"]; touched || input.GetAnnotations() != nil {
		t.Fatal("the input changed")
	}
}

func TestAdoptOmitsAnEmptyOwnerNamespace(t *testing.T) {
	got := Adopt(widget("rook-ceph", "acme-prod", nil, nil), Owner{Kind: "Namespace", Name: "acme-prod", UID: types.UID("uid-9"), Tenant: "acme", Project: "prod"})
	if _, found := got.GetAnnotations()[AnnotationOwnerNamespace]; found {
		t.Fatalf("annotations %+v", got.GetAnnotations())
	}
}

func sweepFixture(t *testing.T) (client.Client, func(context.Context, Owner) (bool, error)) {
	t.Helper()
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(widgetGVK, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(widgetGVK.GroupVersion().WithKind("WidgetList"), &unstructured.UnstructuredList{})
	owned := func(name, owner string) *unstructured.Unstructured {
		return Adopt(widget("rook-ceph", name, nil, nil), Owner{Kind: "ObjectStorageUser", Name: owner, Namespace: "acme-prod", UID: types.UID("uid-" + owner), Tenant: "acme", Project: "prod"})
	}
	unlabeled := widget("rook-ceph", "platform-admin", nil, nil)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(owned("alive", "app"), owned("orphan", "gone"), unlabeled).Build()
	exists := func(_ context.Context, owner Owner) (bool, error) { return owner.Name == "app", nil }
	return c, exists
}

func widgetNames(t *testing.T, c client.Client) []string {
	t.Helper()
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(widgetGVK.GroupVersion().WithKind("WidgetList"))
	if err := c.List(context.Background(), list); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, item := range list.Items {
		names = append(names, item.GetName())
	}
	return names
}

func TestSweepOrphansDryRunReportsAndKeeps(t *testing.T) {
	c, exists := sweepFixture(t)
	orphans, err := SweepOrphans(context.Background(), c, []schema.GroupVersionKind{widgetGVK}, exists, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 || orphans[0].GetName() != "orphan" {
		t.Fatalf("orphans %+v", orphans)
	}
	if got := widgetNames(t, c); len(got) != 3 {
		t.Fatalf("objects %v", got)
	}
}

func TestSweepOrphansRemovesOnlyOrphans(t *testing.T) {
	c, exists := sweepFixture(t)
	orphans, err := SweepOrphans(context.Background(), c, []schema.GroupVersionKind{widgetGVK}, exists, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 1 {
		t.Fatalf("orphans %+v", orphans)
	}
	got := widgetNames(t, c)
	if len(got) != 2 || got[0] == "orphan" || got[1] == "orphan" {
		t.Fatalf("objects %v", got)
	}
}

func TestSweepOrphansIgnoresAKindThatIsNotServed(t *testing.T) {
	c, exists := sweepFixture(t)
	missing := schema.GroupVersionKind{Group: "absent.test", Version: "v1", Kind: "Thing"}
	if _, err := SweepOrphans(context.Background(), c, []schema.GroupVersionKind{missing}, exists, false); err != nil {
		t.Fatal(err)
	}
}
