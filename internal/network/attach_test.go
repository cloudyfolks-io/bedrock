package network

import (
	"slices"
	"testing"
)

func TestPodNetworkMetadataSingleNIC(t *testing.T) {
	annotations, labels := PodNetworkMetadata([]NIC{{Namespace: "t1-prod", Subnet: "default", MAC: "02:00:00:00:00:01", SecurityGroups: []string{"web", "db"}}})
	groups := []string{FabricName("t1-prod", "db"), FabricName("t1-prod", "web")}
	slices.Sort(groups)
	prefix := "default.t1-prod.fabric.cloudyfolks.io/"
	want := map[string]string{
		prefix + "port_security":   "true",
		prefix + "security_groups": groups[0] + "," + groups[1],
		prefix + "mac_address":     "02:00:00:00:00:01",
	}
	if len(annotations) != len(want) {
		t.Fatalf("annotations %v", annotations)
	}
	for k, v := range want {
		if annotations[k] != v {
			t.Fatalf("annotation %s = %q, want %q", k, annotations[k], v)
		}
	}
	if len(labels) != 2 || labels["sg.bedrock.cloudyfolks.io/web"] != "true" || labels["sg.bedrock.cloudyfolks.io/db"] != "true" {
		t.Fatalf("labels %v", labels)
	}
}

func TestPodNetworkMetadataTwoNICs(t *testing.T) {
	annotations, _ := PodNetworkMetadata([]NIC{{Namespace: "t1-prod", Subnet: "default"}, {Namespace: "t1-prod", Subnet: "backend"}})
	if annotations["backend.t1-prod.fabric.cloudyfolks.io/default_route"] != "false" {
		t.Fatalf("annotations %v", annotations)
	}
	if _, found := annotations["default.t1-prod.fabric.cloudyfolks.io/default_route"]; found {
		t.Fatal("the first NIC keeps the default route")
	}
	if annotations["backend.t1-prod.fabric.cloudyfolks.io/port_security"] != "true" || annotations["default.t1-prod.fabric.cloudyfolks.io/port_security"] != "true" {
		t.Fatalf("annotations %v", annotations)
	}
}

func TestPodNetworkMetadataOmitsEmptyValues(t *testing.T) {
	annotations, labels := PodNetworkMetadata([]NIC{{Namespace: "t1-prod", Subnet: "default"}})
	if len(annotations) != 1 || len(labels) != 0 {
		t.Fatalf("annotations %v labels %v", annotations, labels)
	}
}

func TestPodNetworkMetadataSharesGroupLabelsAcrossNICs(t *testing.T) {
	_, labels := PodNetworkMetadata([]NIC{{Namespace: "n", Subnet: "a", SecurityGroups: []string{"web"}}, {Namespace: "n", Subnet: "b", SecurityGroups: []string{"web", "db"}}})
	if len(labels) != 2 {
		t.Fatalf("labels %v", labels)
	}
}

func TestAttachments(t *testing.T) {
	got := Attachments([]NIC{{Namespace: "t1-prod", Subnet: "default"}, {Namespace: "t1-prod", Subnet: "backend"}})
	if !slices.Equal(got, []string{"t1-prod/default", "t1-prod/backend"}) {
		t.Fatalf("attachments %v", got)
	}
}
