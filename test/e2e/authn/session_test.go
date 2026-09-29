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

func TestParseClientAddress(t *testing.T) {
	cases := []struct {
		recorded string
		wantErr  bool
	}{
		{recorded: "10.0.0.250"},
		{recorded: "100.64.0.2"},
		{recorded: "2001:db8::1"},
		{recorded: "", wantErr: true},
		{recorded: "not-an-address", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.recorded, func(t *testing.T) {
			address, err := parseClientAddress(tc.recorded)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseClientAddress(%q) = %v, %v, want error %t", tc.recorded, address, err, tc.wantErr)
			}
			if err == nil && address.String() != net.ParseIP(tc.recorded).String() {
				t.Fatalf("parseClientAddress(%q) = %v", tc.recorded, address)
			}
		})
	}
}
