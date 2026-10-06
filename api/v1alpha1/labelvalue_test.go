package v1alpha1

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestLabelValueIsAValidKubernetesLabelValue(t *testing.T) {
	cases := map[string]string{
		"short":                                                 "short",
		strings.Repeat("a", 63):                                 strings.Repeat("a", 63),
		strings.Repeat("a", 62) + "-x":                          strings.Repeat("a", 62),
		strings.Repeat("a", 62) + ".x":                          strings.Repeat("a", 62),
		strings.Repeat("a", 62) + "_x":                          strings.Repeat("a", 62),
		strings.Repeat("a", 60) + "-.-x":                        strings.Repeat("a", 60),
		strings.Repeat("a", 30) + "." + strings.Repeat("b", 40): strings.Repeat("a", 30) + "." + strings.Repeat("b", 32),
		"": "",
	}
	for name, want := range cases {
		got := LabelValue(name)
		if got != want {
			t.Fatalf("LabelValue(%q) = %q, want %q", name, got, want)
		}
		if problems := validation.IsValidLabelValue(got); len(problems) != 0 {
			t.Fatalf("LabelValue(%q) = %q is not a label value: %v", name, got, problems)
		}
	}
}
