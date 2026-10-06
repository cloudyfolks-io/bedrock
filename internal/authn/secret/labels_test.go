package secret

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestObjectLabelEndsWithAnAlphanumericCharacter(t *testing.T) {
	for _, length := range []int{62, 63} {
		name := strings.Repeat("a", length) + "-recovery"
		owner := &v1alpha1.Credential{
			TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Credential"},
			ObjectMeta: metav1.ObjectMeta{Name: name, UID: "11111111-1111-1111-1111-111111111111"},
		}
		got := Object(owner, name, map[string][]byte{"codes": []byte("hash")})
		if problems := validation.IsValidLabelValue(got.Labels[v1alpha1.LabelName]); len(problems) != 0 {
			t.Fatalf("label %q for a %d-character prefix: %v", got.Labels[v1alpha1.LabelName], length, problems)
		}
	}
}
