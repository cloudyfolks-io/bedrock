package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestAuthnRestartGrantFresh(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		annotations map[string]string
		want        bool
	}{
		"no annotations":    {nil, false},
		"just granted":      {map[string]string{AnnotationAuthnRestart: now.Format(time.RFC3339)}, true},
		"one second left":   {map[string]string{AnnotationAuthnRestart: now.Add(-AuthnRestartGrantLifetime + time.Second).Format(time.RFC3339)}, true},
		"expired":           {map[string]string{AnnotationAuthnRestart: now.Add(-AuthnRestartGrantLifetime).Format(time.RFC3339)}, false},
		"not a time":        {map[string]string{AnnotationAuthnRestart: "yes"}, false},
		"empty value":       {map[string]string{AnnotationAuthnRestart: ""}, false},
		"other annotations": {map[string]string{"other": now.Format(time.RFC3339)}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			host := Host{ObjectMeta: metav1.ObjectMeta{Annotations: tc.annotations}}
			if got := AuthnRestartGrantFresh(host, now); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
