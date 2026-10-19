package operator

import (
	"crypto/rand"
	"slices"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/encryption"
)

func inFlightHost(name, reported, grant string) v1alpha1.Host {
	return withGrant(controlPlaneHost(name, reported), grant)
}

func TestRevertWaitsForAnApplyInFlight(t *testing.T) {
	chain := newConfigChain(t)
	h1, h2 := encryption.Hash(chain.added), encryption.Hash(chain.promoted)
	hosts := []v1alpha1.Host{inFlightHost("cp-1", h2, h2), inFlightHost("cp-2", h1, h2), controlPlaneHost("cp-3", h1)}
	got := decideRotation(rotationInput{Action: v1alpha1.EncryptionActionRevert, Status: phaseStatus(v1alpha1.RotationPromotingKey), Secret: chain.promoted, Hosts: hosts, Now: rotationNow, Random: rand.Reader})
	if got.ClearAction || got.Secret != nil || got.Grant != "" || len(got.Revoke) != 0 || got.Status.Reverting || !strings.Contains(got.Status.Message, "cp-2") {
		t.Fatalf("decision %+v", got)
	}
	hosts[1] = withHandled(inFlightHost("cp-2", h2, h2), h2)
	proceed := decideRotation(rotationInput{Action: v1alpha1.EncryptionActionRevert, Status: phaseStatus(v1alpha1.RotationPromotingKey), Secret: chain.promoted, Hosts: hosts, Now: rotationNow, Random: rand.Reader})
	if !proceed.ClearAction || !proceed.Status.Reverting || !slices.Equal(keysOf(t, proceed.Secret), []string{"key1", "key2"}) {
		t.Fatalf("decision %+v", proceed)
	}
}

func withHandled(host v1alpha1.Host, hash string) v1alpha1.Host {
	host.Status.Encryption.HandledGrant = hash
	return host
}

func TestAHostWithAnotherGrantThanItsReportIsNotConverged(t *testing.T) {
	chain := newConfigChain(t)
	h1, h2 := encryption.Hash(chain.added), encryption.Hash(chain.promoted)
	hosts := []v1alpha1.Host{inFlightHost("cp-1", h1, h2), controlPlaneHost("cp-2", h1)}
	got := decideRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationAddingKey), Secret: chain.added, Hosts: hosts, Now: rotationNow, Random: rand.Reader})
	if got.Status.Phase != v1alpha1.RotationAddingKey || got.Grant != "" || len(got.Revoke) != 0 {
		t.Fatalf("decision %+v", got)
	}
}

func TestNoSecondGrantWhileAnEarlierGrantIsUnresolved(t *testing.T) {
	chain := newConfigChain(t)
	h1, h2 := encryption.Hash(chain.added), encryption.Hash(chain.promoted)
	hosts := []v1alpha1.Host{inFlightHost("cp-1", h2, h2), inFlightHost("cp-2", h2, "older"), controlPlaneHost("cp-3", h2)}
	hosts[1].Status.Encryption.Hash = h1
	got := decideRotation(rotationInput{Status: phaseStatus(v1alpha1.RotationPromotingKey), Secret: chain.promoted, Hosts: append(hosts, controlPlaneHost("cp-4", h1)), Now: rotationNow, Random: rand.Reader})
	if got.Grant != "" || !strings.Contains(got.Status.Message, "cp-2") {
		t.Fatalf("decision %+v", got)
	}
}

type simulatedHost struct {
	file     string
	grant    string
	handled  string
	inFlight bool
}

func TestRevertDuringAnApplyNeverOverlapsAndNeverRemovesEarly(t *testing.T) {
	chain := newConfigChain(t)
	h1, h2 := encryption.Hash(chain.added), encryption.Hash(chain.promoted)
	secret := chain.promoted
	hosts := map[string]*simulatedHost{
		"cp-1": {file: h2, grant: h2, handled: h2},
		"cp-2": {file: h1, grant: h2, inFlight: true},
		"cp-3": {file: h1},
	}
	status := phaseStatus(v1alpha1.RotationPromotingKey)
	action := v1alpha1.EncryptionActionRevert
	delay := 2
	for round := 0; round < 40 && !(status.Phase == "" && status.LastRotatedAt != nil); round++ {
		var list []v1alpha1.Host
		for _, name := range []string{"cp-1", "cp-2", "cp-3"} {
			list = append(list, simulatedReport(name, hosts[name]))
		}
		decision := decideRotation(rotationInput{Action: action, Status: status, Secret: secret, Hosts: list, Now: rotationNow, Random: rand.Reader})
		if decision.ClearAction {
			action = ""
		}
		previous := status.Phase
		status = decision.Status
		if decision.Rewrite {
			status = finishRewrite(status, 1)
		}
		if decision.Secret != nil {
			secret = decision.Secret
		}
		for _, name := range decision.Revoke {
			hosts[name].grant = ""
		}
		if decision.Grant != "" {
			hosts[decision.Grant].grant = decision.GrantHash
		}
		if status.Phase == v1alpha1.RotationRemovingKey && previous != status.Phase {
			for name, host := range hosts {
				if host.file != h1 {
					t.Fatalf("round %d: %s holds %s when the removal starts", round, name, host.file)
				}
			}
		}
		unresolved := 0
		for _, host := range hosts {
			if host.grant != "" && host.handled != host.grant && host.file != host.grant {
				unresolved++
			}
		}
		if unresolved > 1 {
			t.Fatalf("round %d: %d hosts hold an unresolved grant", round, unresolved)
		}
		delay--
		for _, name := range []string{"cp-1", "cp-2", "cp-3"} {
			host := hosts[name]
			if host.grant == "" || host.handled == host.grant {
				continue
			}
			if host.inFlight && delay > 0 {
				continue
			}
			if host.inFlight || host.grant == encryption.Hash(secret) {
				host.file = host.grant
			}
			host.handled = host.grant
			host.inFlight = false
		}
	}
	if status.Phase != "" || status.LastRotatedAt == nil {
		t.Fatalf("the revert did not end: %+v", status)
	}
}

func simulatedReport(name string, host *simulatedHost) v1alpha1.Host {
	report := controlPlaneHost(name, host.file)
	report.Status.Encryption.HandledGrant = host.handled
	if host.grant != "" {
		report = withGrant(report, host.grant)
	}
	return report
}
