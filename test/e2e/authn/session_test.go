//go:build e2e

package authn

import (
	"net"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func sessionAt(name, userRef string, at time.Time) v1alpha1.Session {
	return v1alpha1.Session{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.SessionSpec{UserRef: userRef, AuthTime: metav1.NewTime(at)},
	}
}

func TestLatestSessionPicksTheNewestOfTheUser(t *testing.T) {
	now := time.Now()
	sessions := []v1alpha1.Session{
		sessionAt("old", "admin", now.Add(-time.Hour)),
		sessionAt("other", "alice", now.Add(time.Hour)),
		sessionAt("new", "admin", now),
	}
	latest, found := latestSession(sessions, "admin")
	if !found || latest.Name != "new" {
		t.Fatalf("latest session = %q (found %t), want new", latest.Name, found)
	}
	if _, found := latestSession(sessions, "carol"); found {
		t.Fatalf("found a session for a user without sessions")
	}
}

func TestCheckClientAddress(t *testing.T) {
	_, join, err := net.ParseCIDR("100.64.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	own := net.ParseIP("10.0.0.250")
	cases := []struct {
		name     string
		recorded string
		wantErr  bool
	}{
		{name: "the client's own source address", recorded: "10.0.0.250"},
		{name: "a masqueraded join address", recorded: "100.64.0.2", wantErr: true},
		{name: "another address", recorded: "10.0.0.11", wantErr: true},
		{name: "not an address", recorded: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkClientAddress(tc.recorded, join, own)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkClientAddress(%q) = %v, want error %t", tc.recorded, err, tc.wantErr)
			}
		})
	}
}
