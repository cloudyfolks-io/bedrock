package v1alpha1

import (
	"slices"
	"testing"
)

func TestNetworkRanges(t *testing.T) {
	cases := map[string]struct {
		spec NetworkSpec
		want []string
	}{
		"old cidr is one range": {NetworkSpec{CIDR: "10.100.0.0/16"}, []string{"10.100.0.0/16"}},
		"cidrs are all ranges":  {NetworkSpec{CIDRs: []string{"10.0.0.0/16", "172.16.0.0/16"}}, []string{"10.0.0.0/16", "172.16.0.0/16"}},
		"no range":              {NetworkSpec{}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.spec.Ranges(); !slices.Equal(got, tc.want) {
				t.Fatalf("ranges %v, want %v", got, tc.want)
			}
		})
	}
}
