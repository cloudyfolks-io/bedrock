package compute

import (
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestPortSecurityIsOffOnlyWhenTheFieldIsFalse(t *testing.T) {
	off, on := false, true
	cases := map[string]struct {
		field *bool
		want  bool
	}{"unset": {nil, false}, "true": {&on, false}, "false": {&off, true}}
	for label, c := range cases {
		if got := portSecurityOff(v1alpha1.VMNetwork{SubnetRef: "default", PortSecurity: c.field}); got != c.want {
			t.Errorf("%s: got %v, want %v", label, got, c.want)
		}
	}
}
