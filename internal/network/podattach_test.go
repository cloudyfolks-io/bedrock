package network

import (
	"maps"
	"strings"
	"testing"
)

func TestPodAttachmentPinsTheAddressAndNamesGroups(t *testing.T) {
	attachment := PodAttachment(NIC{Namespace: "t1-prod", Subnet: "default", Address: "10.0.0.7", SecurityGroups: []string{"web", "db"}})
	prefix := "default.t1-prod.fabric.cloudyfolks.io/"
	want := map[string]string{
		"v1.multus-cni.io/default-network": "t1-prod/default",
		prefix + "port_security":           "true",
		prefix + "security_groups":         FabricName("t1-prod", "db") + "," + FabricName("t1-prod", "web"),
		prefix + "ip_address":              "10.0.0.7",
	}
	if !maps.Equal(attachment.Annotations, want) {
		t.Fatalf("annotations %v, want %v", attachment.Annotations, want)
	}
	wantLabels := map[string]string{"sg.bedrock.cloudyfolks.io/web": "true", "sg.bedrock.cloudyfolks.io/db": "true"}
	if !maps.Equal(attachment.Labels, wantLabels) {
		t.Fatalf("labels %v, want %v", attachment.Labels, wantLabels)
	}
}

func TestPodAttachmentUsesTheDefaultGroupWithoutAddress(t *testing.T) {
	attachment := PodAttachment(NIC{Namespace: "t1-prod", Subnet: "back"})
	prefix := "back.t1-prod.fabric.cloudyfolks.io/"
	want := map[string]string{
		"v1.multus-cni.io/default-network": "t1-prod/back",
		prefix + "port_security":           "true",
		prefix + "security_groups":         "default-t1-prod",
	}
	if !maps.Equal(attachment.Annotations, want) {
		t.Fatalf("annotations %v, want %v", attachment.Annotations, want)
	}
	if len(attachment.Labels) != 0 {
		t.Fatalf("labels %v", attachment.Labels)
	}
}

func TestPodAttachmentNeverTurnsOffTheDefaultRoute(t *testing.T) {
	attachment := PodAttachment(NIC{Namespace: "t1-prod", Subnet: "default", Address: "10.0.0.7"})
	for key := range attachment.Annotations {
		if strings.HasSuffix(key, "default_route") {
			t.Fatalf("annotation %s must not exist", key)
		}
	}
}

func TestPodAttachmentSharesTheBytesOfPodNetworkMetadata(t *testing.T) {
	nic := NIC{Namespace: "t1-prod", Subnet: "default", MAC: "02:00:00:00:00:01", Address: "10.0.0.9", SecurityGroups: []string{"web"}}
	attachment := PodAttachment(nic)
	annotations, labels := PodNetworkMetadata([]NIC{nic})
	for key, value := range annotations {
		if attachment.Annotations[key] != value {
			t.Fatalf("annotation %s = %q, want %q", key, attachment.Annotations[key], value)
		}
	}
	if len(attachment.Annotations) != len(annotations)+1 {
		t.Fatalf("annotations %v", attachment.Annotations)
	}
	if !maps.Equal(attachment.Labels, labels) {
		t.Fatalf("labels %v, want %v", attachment.Labels, labels)
	}
}
