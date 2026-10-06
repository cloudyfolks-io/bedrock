package operator

import (
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

var restartNow = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

func restartHost(name string, pending bool, grantedAgo string) v1alpha1.Host {
	host := v1alpha1.Host{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if pending {
		host.Status.Authn = &v1alpha1.AuthnFilesStatus{WebhookRestartPending: true}
	}
	switch grantedAgo {
	case "":
	case "unparsable":
		host.Annotations = map[string]string{v1alpha1.AnnotationAuthnRestart: "soon"}
	default:
		ago, err := time.ParseDuration(grantedAgo)
		if err != nil {
			panic(err)
		}
		host.Annotations = map[string]string{v1alpha1.AnnotationAuthnRestart: restartNow.Add(-ago).Format(time.RFC3339)}
	}
	return host
}

func TestAuthnRestartGrants(t *testing.T) {
	cases := map[string]struct {
		hosts  []v1alpha1.Host
		grant  string
		revoke []string
	}{
		"no pending hosts": {
			hosts: []v1alpha1.Host{restartHost("a", false, ""), restartHost("b", false, "")},
		},
		"one pending host is granted": {
			hosts: []v1alpha1.Host{restartHost("a", false, ""), restartHost("b", true, "")},
			grant: "b",
		},
		"two pending hosts grant the lowest name": {
			hosts: []v1alpha1.Host{restartHost("c", true, ""), restartHost("a", true, ""), restartHost("b", false, "")},
			grant: "a",
		},
		"a fresh grant on a pending host blocks a second grant": {
			hosts: []v1alpha1.Host{restartHost("a", true, ""), restartHost("b", true, "1m")},
		},
		"a stuck lowest host expires with two others pending: the next name is granted": {
			hosts:  []v1alpha1.Host{restartHost("a", true, "16m"), restartHost("b", true, ""), restartHost("c", true, "")},
			grant:  "b",
			revoke: []string{"a"},
		},
		"a stuck middle host expires: the next name is granted": {
			hosts:  []v1alpha1.Host{restartHost("a", true, ""), restartHost("b", true, "20m"), restartHost("c", true, "")},
			grant:  "c",
			revoke: []string{"b"},
		},
		"the highest stuck host expires: the grant wraps to the lowest name": {
			hosts:  []v1alpha1.Host{restartHost("a", true, ""), restartHost("b", true, ""), restartHost("c", true, "16m")},
			grant:  "a",
			revoke: []string{"c"},
		},
		"a single stuck host expires: it is granted again": {
			hosts:  []v1alpha1.Host{restartHost("a", false, ""), restartHost("b", true, "16m")},
			grant:  "b",
			revoke: []string{"b"},
		},
		"a grant on a host that is no longer pending is revoked and the next pending host is granted": {
			hosts:  []v1alpha1.Host{restartHost("a", false, "1m"), restartHost("b", true, "")},
			grant:  "b",
			revoke: []string{"a"},
		},
		"a grant on a host that is no longer pending is revoked with nothing else pending": {
			hosts:  []v1alpha1.Host{restartHost("a", false, "1m")},
			revoke: []string{"a"},
		},
		"an unparsable grant is revoked": {
			hosts:  []v1alpha1.Host{restartHost("a", true, "unparsable"), restartHost("b", false, "unparsable")},
			grant:  "a",
			revoke: []string{"a", "b"},
		},
		"revokes are sorted by name": {
			hosts:  []v1alpha1.Host{restartHost("c", false, "1m"), restartHost("a", false, "1m")},
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
	hosts := []v1alpha1.Host{restartHost("a", true, "5m"), restartHost("b", true, "")}
	if got := authnRestartRequeue(hosts, "", restartNow); got != v1alpha1.AuthnRestartGrantLifetime-5*time.Minute {
		t.Fatalf("remaining lifetime of the active grant, got %s", got)
	}
	if got := authnRestartRequeue([]v1alpha1.Host{restartHost("a", true, "")}, "a", restartNow); got != v1alpha1.AuthnRestartGrantLifetime {
		t.Fatalf("a new grant lasts the full lifetime, got %s", got)
	}
	if got := authnRestartRequeue([]v1alpha1.Host{restartHost("a", false, "")}, "", restartNow); got != 0 {
		t.Fatalf("nothing granted needs no requeue, got %s", got)
	}
}
