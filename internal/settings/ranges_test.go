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

func TestBucketConflict(t *testing.T) {
	if err := BucketConflict(map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if err := BucketConflict(map[string]string{"registry.bucket": "bedrock-database-backups"}); err == nil {
		t.Fatal("the same bucket for the registry and the database backups was accepted")
	}
	if err := Conflict("registry.bucket", map[string]string{"registry.bucket": "same", "database.backup.bucket": "same"}); err == nil {
		t.Fatal("Conflict missed the bucket clash")
	}
	if err := Conflict("storage.replicas", map[string]string{"registry.bucket": "same", "database.backup.bucket": "same"}); err != nil {
		t.Fatal("an unrelated key was refused")
	}
}
