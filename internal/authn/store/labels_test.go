package store

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestObjectLabelsEndWithAnAlphanumericCharacter(t *testing.T) {
	for _, name := range []string{strings.Repeat("a", 62) + "-x", strings.Repeat("a", 62) + ".x", strings.Repeat("a", 70)} {
		value := objectLabels("Session", name)[v1alpha1.LabelName]
		if problems := validation.IsValidLabelValue(value); len(problems) != 0 {
			t.Fatalf("label %q for %q: %v", value, name, problems)
		}
	}
}
