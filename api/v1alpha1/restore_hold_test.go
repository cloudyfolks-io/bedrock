package v1alpha1

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRestoreHolderTreatsThePresenceOfTheKeyAsAHold(t *testing.T) {
	cases := []struct {
		name        string
		annotations map[string]string
		holder      string
		held        bool
	}{
		{"no annotations", nil, "", false},
		{"other annotation", map[string]string{"a": "b"}, "", false},
		{"named hold", map[string]string{AnnotationRestoreHold: "restore-1"}, "restore-1", true},
		{"empty hold", map[string]string{AnnotationRestoreHold: ""}, "", true},
	}
	for _, tc := range cases {
		holder, held := RestoreHolder(&metav1.ObjectMeta{Annotations: tc.annotations})
		if holder != tc.holder || held != tc.held {
			t.Errorf("%s: got %q %v, want %q %v", tc.name, holder, held, tc.holder, tc.held)
		}
	}
}
