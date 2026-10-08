package addons

import (
	"maps"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func jsonOf(raw []byte) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: raw}
}

func TestValuesOfReadsFlatAndNestedKeys(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want map[string]string
	}{
		{"flat", `{"persistence.size":"5Gi","persistence.storage-class":"fast"}`, map[string]string{"persistence.size": "5Gi", "persistence.storage-class": "fast"}},
		{"nested", `{"persistence":{"size":"5Gi"}}`, map[string]string{"persistence.size": "5Gi"}},
		{"number", `{"persistence.size":5}`, map[string]string{"persistence.size": "5"}},
		{"empty", `{}`, map[string]string{}},
	}
	for _, c := range cases {
		got, err := valuesOf(v1alpha1.Addon{Spec: v1alpha1.AddonSpec{Values: jsonOf([]byte(c.raw))}})
		if err != nil || !maps.Equal(got, c.want) {
			t.Errorf("%s: got %v %v", c.name, got, err)
		}
	}
	if got, err := valuesOf(v1alpha1.Addon{}); err != nil || len(got) != 0 {
		t.Errorf("nil values: %v %v", got, err)
	}
	if _, err := valuesOf(v1alpha1.Addon{Spec: v1alpha1.AddonSpec{Values: jsonOf([]byte(`[1]`))}}); err == nil {
		t.Error("array accepted")
	}
}
