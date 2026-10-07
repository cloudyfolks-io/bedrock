package operator

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestAddonKindValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	addon := &v1alpha1.Addon{
		ObjectMeta: metav1.ObjectMeta{Name: "loki"},
		Spec:       v1alpha1.AddonSpec{Enabled: true, Values: &apiextensionsv1.JSON{Raw: []byte(`{"retention":{"days":14},"replicas":1}`)}},
	}
	createValid(t, c, addon)

	var fresh v1alpha1.Addon
	if err := c.Get(ctx, client.ObjectKeyFromObject(addon), &fresh); err != nil {
		t.Fatal(err)
	}
	if string(fresh.Spec.Values.Raw) != `{"replicas":1,"retention":{"days":14}}` {
		t.Fatalf("values must keep nested fields: %s", fresh.Spec.Values.Raw)
	}

	fresh.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: "Applied", LastTransitionTime: metav1.Now()}}
	if err := c.Status().Update(ctx, &fresh); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}

	disabled := &v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: "kata"}}
	createValid(t, c, disabled)

	notObject := &v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: "bad-values"}, Spec: v1alpha1.AddonSpec{Values: &apiextensionsv1.JSON{Raw: []byte(`"text"`)}}}
	createInvalid(t, c, notObject, "spec.values")
}
