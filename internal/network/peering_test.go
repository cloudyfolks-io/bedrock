package network

import (
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const peeringRange = "169.254.0.0/16"

func peeringOf(name string, age int, a, b, spec, status string) v1alpha1.NetworkPeering {
	return v1alpha1.NetworkPeering{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("uid-" + name), CreationTimestamp: metav1.NewTime(time.Unix(int64(1000+age), 0))},
		Spec:       v1alpha1.NetworkPeeringSpec{NetworkRefs: []string{a, b}, ConnectCIDR: spec},
		Status:     v1alpha1.NetworkPeeringStatus{ConnectCIDR: status},
	}
}

func TestResolvePeeringAllocatesTheLowestFreeSlash30(t *testing.T) {
	self := peeringOf("a-b", 1, "a", "b", "", "")
	cidr, violation := ResolvePeering(self, []v1alpha1.NetworkPeering{self}, peeringRange)
	if violation != nil || cidr != "169.254.0.0/30" {
		t.Fatalf("cidr %q violation %+v", cidr, violation)
	}
}

func TestResolvePeeringSkipsEveryHeldSlash30(t *testing.T) {
	self := peeringOf("c-d", 9, "c", "d", "", "")
	all := []v1alpha1.NetworkPeering{
		self,
		peeringOf("a-b", 1, "a", "b", "", "169.254.0.0/30"),
		peeringOf("a-c", 2, "a", "c", "169.254.0.4/30", ""),
		peeringOf("a-d", 3, "a", "d", "", "169.254.0.12/30"),
	}
	cidr, violation := ResolvePeering(self, all, peeringRange)
	if violation != nil || cidr != "169.254.0.8/30" {
		t.Fatalf("cidr %q violation %+v", cidr, violation)
	}
}

func TestResolvePeeringKeepsTheRecordedSlash30(t *testing.T) {
	self := peeringOf("c-d", 9, "c", "d", "", "169.254.0.40/30")
	all := []v1alpha1.NetworkPeering{peeringOf("a-b", 1, "a", "b", "", "169.254.0.0/30"), self}
	cidr, violation := ResolvePeering(self, all, peeringRange)
	if violation != nil || cidr != "169.254.0.40/30" {
		t.Fatalf("cidr %q violation %+v", cidr, violation)
	}
}

func TestResolvePeeringIgnoresPeeringsThatAreBeingDeleted(t *testing.T) {
	self := peeringOf("c-d", 9, "c", "d", "", "")
	gone := peeringOf("a-b", 1, "a", "b", "", "169.254.0.0/30")
	gone.DeletionTimestamp = ptrTime(time.Unix(2000, 0))
	cidr, _ := ResolvePeering(self, []v1alpha1.NetworkPeering{gone, self}, peeringRange)
	if cidr != "169.254.0.0/30" {
		t.Fatalf("cidr %q", cidr)
	}
}

func ptrTime(t time.Time) *metav1.Time {
	value := metav1.NewTime(t)
	return &value
}

func TestResolvePeeringTakesTheConnectCIDROfTheSpec(t *testing.T) {
	self := peeringOf("a-b", 1, "a", "b", "10.9.0.0/30", "")
	cidr, violation := ResolvePeering(self, []v1alpha1.NetworkPeering{self}, peeringRange)
	if violation != nil || cidr != "10.9.0.0/30" {
		t.Fatalf("cidr %q violation %+v", cidr, violation)
	}
}

func TestResolvePeeringRefusesAConnectCIDRThatAnOlderPeeringHolds(t *testing.T) {
	older := peeringOf("a-b", 1, "a", "b", "10.9.0.0/30", "10.9.0.0/30")
	self := peeringOf("a-c", 2, "a", "c", "10.9.0.0/30", "")
	cidr, violation := ResolvePeering(self, []v1alpha1.NetworkPeering{older, self}, peeringRange)
	if cidr != "" || violation == nil || violation.Reason != ReasonConnectCIDRInUse {
		t.Fatalf("cidr %q violation %+v", cidr, violation)
	}
	if cidr, violation = ResolvePeering(older, []v1alpha1.NetworkPeering{older, self}, peeringRange); violation != nil || cidr != "10.9.0.0/30" {
		t.Fatalf("the older peering keeps it: %q %+v", cidr, violation)
	}
}

func TestResolvePeeringRefusesAConnectCIDRThatARecordedPeeringHolds(t *testing.T) {
	recorded := peeringOf("a-b", 5, "a", "b", "", "169.254.0.0/30")
	self := peeringOf("a-c", 1, "a", "c", "169.254.0.0/30", "")
	_, violation := ResolvePeering(self, []v1alpha1.NetworkPeering{recorded, self}, peeringRange)
	if violation == nil || violation.Reason != ReasonConnectCIDRInUse {
		t.Fatalf("violation %+v", violation)
	}
}

func TestResolvePeeringReportsAFullRange(t *testing.T) {
	self := peeringOf("a-c", 2, "a", "c", "", "")
	all := []v1alpha1.NetworkPeering{peeringOf("a-b", 1, "a", "b", "", "169.254.0.0/30"), self}
	cidr, violation := ResolvePeering(self, all, "169.254.0.0/30")
	if cidr != "" || violation == nil || violation.Reason != ReasonConnectCIDRInUse {
		t.Fatalf("cidr %q violation %+v", cidr, violation)
	}
}

func TestResolvePeeringRefusesASecondPeeringOfTheSamePair(t *testing.T) {
	older := peeringOf("a-b", 1, "a", "b", "", "169.254.0.0/30")
	self := peeringOf("b-a", 2, "b", "a", "", "")
	cidr, violation := ResolvePeering(self, []v1alpha1.NetworkPeering{older, self}, peeringRange)
	if cidr != "" || violation == nil || violation.Reason != ReasonPeeringExists {
		t.Fatalf("cidr %q violation %+v", cidr, violation)
	}
}

func networkNamed(name string, cidrs ...string) v1alpha1.Network {
	return v1alpha1.Network{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.NetworkSpec{TenantRef: "acme", CIDRs: cidrs}}
}

func TestPeerLinksGiveEachSideItsOwnAddressAndTheOtherSidesRanges(t *testing.T) {
	peerings := []v1alpha1.NetworkPeering{peeringOf("a-b", 1, "a", "b", "", "169.254.0.4/30")}
	networks := []v1alpha1.Network{networkNamed("a", "10.1.0.0/16"), networkNamed("b", "10.2.0.0/16", "10.3.0.0/16")}
	got := PeerLinks("a", peerings, networks)
	want := []PeerLink{{RemoteVpc: "b", LocalIP: "169.254.0.5/30", RemoteIP: "169.254.0.6", RemoteRanges: []string{"10.2.0.0/16", "10.3.0.0/16"}}}
	if !slices.EqualFunc(got, want, equalLink) {
		t.Fatalf("a: %+v", got)
	}
	got = PeerLinks("b", peerings, networks)
	want = []PeerLink{{RemoteVpc: "a", LocalIP: "169.254.0.6/30", RemoteIP: "169.254.0.5", RemoteRanges: []string{"10.1.0.0/16"}}}
	if !slices.EqualFunc(got, want, equalLink) {
		t.Fatalf("b: %+v", got)
	}
}

func equalLink(a, b PeerLink) bool {
	return a.RemoteVpc == b.RemoteVpc && a.LocalIP == b.LocalIP && a.RemoteIP == b.RemoteIP && slices.Equal(a.RemoteRanges, b.RemoteRanges)
}

func TestPeerLinksSkipWhatIsNotReady(t *testing.T) {
	networks := []v1alpha1.Network{networkNamed("a", "10.1.0.0/16"), networkNamed("b", "10.2.0.0/16"), networkNamed("c", "10.3.0.0/16")}
	deleting := peeringOf("a-c", 2, "a", "c", "", "169.254.0.8/30")
	deleting.DeletionTimestamp = ptrTime(time.Unix(2000, 0))
	peerings := []v1alpha1.NetworkPeering{
		peeringOf("a-b", 1, "a", "b", "", ""),
		deleting,
		peeringOf("a-ghost", 3, "a", "ghost", "", "169.254.0.12/30"),
		peeringOf("c-b", 4, "c", "b", "", "169.254.0.16/30"),
	}
	if got := PeerLinks("a", peerings, networks); len(got) != 0 {
		t.Fatalf("links %+v", got)
	}
}

func TestPeerLinksAreSortedByRemoteVpc(t *testing.T) {
	networks := []v1alpha1.Network{networkNamed("a", "10.1.0.0/16"), networkNamed("b", "10.2.0.0/16"), networkNamed("c", "10.3.0.0/16")}
	peerings := []v1alpha1.NetworkPeering{
		peeringOf("a-c", 1, "a", "c", "", "169.254.0.0/30"),
		peeringOf("a-b", 2, "a", "b", "", "169.254.0.4/30"),
	}
	got := PeerLinks("a", peerings, networks)
	if len(got) != 2 || got[0].RemoteVpc != "b" || got[1].RemoteVpc != "c" {
		t.Fatalf("links %+v", got)
	}
}

func TestPeeringsOfNetworkNamesTheOtherSideToo(t *testing.T) {
	peerings := []v1alpha1.NetworkPeering{peeringOf("a-b", 1, "a", "b", "", ""), peeringOf("c-d", 2, "c", "d", "", "")}
	got := NetworksOfPeering(peerings[0])
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Fatalf("requests %+v", got)
	}
	if got := PeeringsOfNetwork(peerings, "d"); len(got) != 1 || got[0].Name != "c-d" {
		t.Fatalf("requests %+v", got)
	}
}
