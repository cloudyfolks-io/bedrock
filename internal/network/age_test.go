package network

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func aged(created time.Time, namespace, name string) *metav1.ObjectMeta {
	return &metav1.ObjectMeta{CreationTimestamp: metav1.NewTime(created), Namespace: namespace, Name: name}
}

func TestOlderComparesTimestampThenNamespaceThenName(t *testing.T) {
	epoch := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		a, b *metav1.ObjectMeta
		want bool
	}{
		"earlier timestamp wins":   {aged(epoch, "z", "z"), aged(epoch.Add(time.Second), "a", "a"), true},
		"later timestamp loses":    {aged(epoch.Add(time.Second), "a", "a"), aged(epoch, "z", "z"), false},
		"same time, namespace":     {aged(epoch, "a", "z"), aged(epoch, "b", "a"), true},
		"same time, name":          {aged(epoch, "a", "a"), aged(epoch, "a", "b"), true},
		"same object is not older": {aged(epoch, "a", "a"), aged(epoch, "a", "a"), false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Older(c.a, c.b); got != c.want {
				t.Fatalf("Older = %v, want %v", got, c.want)
			}
		})
	}
}
