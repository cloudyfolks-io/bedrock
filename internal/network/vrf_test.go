package network

import "testing"

func TestRefusalBeforeProvisioningPrefersTheRangeBreach(t *testing.T) {
	ranges, vrf := &Violation{Reason: ReasonCIDRReserved}, &Violation{Reason: ReasonVRFReserved}
	if got := RefusalBeforeProvisioning(ranges, vrf); got != ranges {
		t.Fatalf("got %+v", got)
	}
	if got := RefusalBeforeProvisioning(nil, vrf); got != vrf {
		t.Fatalf("got %+v", got)
	}
	if got := RefusalBeforeProvisioning(nil, nil); got != nil {
		t.Fatalf("got %+v", got)
	}
}

func TestRefusalAfterProvisioningKeepsOnlyAVRFInUse(t *testing.T) {
	inUse := &Violation{Reason: ReasonVRFInUse}
	if got := RefusalAfterProvisioning(inUse); got != inUse {
		t.Fatalf("got %+v", got)
	}
	if got := RefusalAfterProvisioning(&Violation{Reason: ReasonVRFReserved}); got != nil {
		t.Fatalf("got %+v", got)
	}
	if got := RefusalAfterProvisioning(nil); got != nil {
		t.Fatalf("got %+v", got)
	}
}
