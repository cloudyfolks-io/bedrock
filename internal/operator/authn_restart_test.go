package operator

import (
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var restartNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func idleHost(name string) v1alpha1.Host {
	return v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func pendingHost(name string) v1alpha1.Host {
	host := idleHost(name)
	host.Status.Authn = &v1alpha1.AuthnFilesStatus{WebhookRestartPending: true}
	return host
}

func grantedWith(host v1alpha1.Host, value string) v1alpha1.Host {
	host.Annotations = map[string]string{v1alpha1.AnnotationAuthnRestart: value}
	return host
}

func grantedAgo(host v1alpha1.Host, ago time.Duration) v1alpha1.Host {
	return grantedWith(host, restartNow.Add(-ago).Format(time.RFC3339))
}

func TestAuthnRestartGrants(t *testing.T) {
	cases := map[string]struct {
		hosts  []v1alpha1.Host
		grant  string
		revoke []string
	}{
		"no pending hosts": {
			hosts: []v1alpha1.Host{idleHost("a"), idleHost("b")},
		},
		"one pending host is granted": {
			hosts: []v1alpha1.Host{idleHost("a"), pendingHost("b")},
			grant: "b",
		},
		"two pending hosts grant the lowest name": {
			hosts: []v1alpha1.Host{pendingHost("c"), pendingHost("a"), idleHost("b")},
			grant: "a",
		},
		"a fresh grant on a pending host blocks a second grant": {
			hosts: []v1alpha1.Host{pendingHost("a"), grantedAgo(pendingHost("b"), 1*time.Minute)},
		},
		"a stuck lowest host expires with two others pending: the next name is granted": {
			hosts:  []v1alpha1.Host{grantedAgo(pendingHost("a"), 16*time.Minute), pendingHost("b"), pendingHost("c")},
			grant:  "b",
			revoke: []string{"a"},
		},
		"a stuck middle host expires: the next name is granted": {
			hosts:  []v1alpha1.Host{pendingHost("a"), grantedAgo(pendingHost("b"), 20*time.Minute), pendingHost("c")},
			grant:  "c",
			revoke: []string{"b"},
		},
		"the highest stuck host expires: the grant wraps to the lowest name": {
			hosts:  []v1alpha1.Host{pendingHost("a"), pendingHost("b"), grantedAgo(pendingHost("c"), 16*time.Minute)},
			grant:  "a",
			revoke: []string{"c"},
		},
		"a single stuck host expires: it is granted again": {
			hosts:  []v1alpha1.Host{idleHost("a"), grantedAgo(pendingHost("b"), 16*time.Minute)},
			grant:  "b",
			revoke: []string{"b"},
		},
		"a grant on a host that is no longer pending is revoked and the next pending host is granted": {
			hosts:  []v1alpha1.Host{grantedAgo(idleHost("a"), 1*time.Minute), pendingHost("b")},
			grant:  "b",
			revoke: []string{"a"},
		},
		"a grant on a host that is no longer pending is revoked with nothing else pending": {
			hosts:  []v1alpha1.Host{grantedAgo(idleHost("a"), 1*time.Minute)},
			revoke: []string{"a"},
		},
		"an unparsable grant is revoked": {
			hosts:  []v1alpha1.Host{grantedWith(pendingHost("a"), "soon"), grantedWith(idleHost("b"), "soon")},
			grant:  "a",
			revoke: []string{"a", "b"},
		},
		"revokes are sorted by name": {
			hosts:  []v1alpha1.Host{grantedAgo(idleHost("c"), 1*time.Minute), grantedAgo(idleHost("a"), 1*time.Minute)},
			revoke: []string{"a", "c"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			before := slices.Clone(tc.hosts)
			grant, revoke := authnRestartGrants(tc.hosts, restartNow)
			if grant != tc.grant || !slices.Equal(revoke, tc.revoke) {
				t.Fatalf("grant %q revoke %v, want grant %q revoke %v", grant, revoke, tc.grant, tc.revoke)
			}
			for i := range before {
				if before[i].Name != tc.hosts[i].Name {
					t.Fatal("the input must stay in order")
				}
			}
		})
	}
}

func TestAuthnRestartRequeue(t *testing.T) {
	hosts := []v1alpha1.Host{grantedAgo(pendingHost("a"), 5*time.Minute), pendingHost("b")}
	if got := authnRestartRequeue(hosts, "", restartNow); got != v1alpha1.AuthnRestartGrantLifetime-5*time.Minute {
		t.Fatalf("remaining lifetime of the active grant, got %s", got)
	}
	if got := authnRestartRequeue([]v1alpha1.Host{pendingHost("a")}, "a", restartNow); got != v1alpha1.AuthnRestartGrantLifetime {
		t.Fatalf("a new grant lasts the full lifetime, got %s", got)
	}
	if got := authnRestartRequeue([]v1alpha1.Host{idleHost("a")}, "", restartNow); got != 0 {
		t.Fatalf("nothing granted needs no requeue, got %s", got)
	}
}
