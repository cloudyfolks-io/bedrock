package network

import (
	"regexp"
	"slices"
	"testing"
)

func TestAllReasonsIsSortedAndUnique(t *testing.T) {
	if !slices.IsSorted(AllReasons) || len(slices.Compact(slices.Clone(AllReasons))) != len(AllReasons) {
		t.Fatal("AllReasons must be sorted and unique")
	}
	if len(AllReasons) != 51 {
		t.Fatalf("unexpected reason count %d", len(AllReasons))
	}
}

func TestReasonsAreCamelCase(t *testing.T) {
	camel := regexp.MustCompile(`^[A-Z][A-Za-z0-9]*$`)
	for _, reason := range AllReasons {
		if !camel.MatchString(reason) {
			t.Fatalf("reason %q is not CamelCase", reason)
		}
	}
}
