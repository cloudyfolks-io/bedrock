package settings

import "testing"

func TestRangeConflict(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]string
		bad    bool
	}{
		{"defaults", map[string]string{}, false},
		{"disjoint", map[string]string{"database.vip-range": "203.0.113.128/26", "network.services.identity-range": "203.0.113.240/29"}, false},
		{"vip contains identity", map[string]string{"database.vip-range": "203.0.113.0/24", "network.services.identity-range": "203.0.113.240/29"}, true},
		{"identity inside vip partially", map[string]string{"database.vip-range": "203.0.113.128/25", "network.services.identity-range": "203.0.113.128/29"}, true},
		{"vip on the default load balancer range", map[string]string{"database.vip-range": "100.69.0.0/24"}, true},
		{"identity on a set load balancer range", map[string]string{"network.load-balancer.cidr": "198.51.100.0/24", "network.services.identity-range": "198.51.100.8/29"}, true},
		{"identity range empty", map[string]string{"database.vip-range": "203.0.113.128/26", "network.services.identity-range": ""}, false},
	}
	for _, c := range cases {
		if err := RangeConflict(c.values); (err != nil) != c.bad {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}
