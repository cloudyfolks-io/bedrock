package v1alpha1

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostChecksOmitsUnknownKVM(t *testing.T) {
	raw, err := json.Marshal(HostChecks{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "kvm") {
		t.Fatalf("got %s", raw)
	}
	yes := true
	raw, err = json.Marshal(HostChecks{KVM: &yes})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"kvm":true`) {
		t.Fatalf("got %s", raw)
	}
}
