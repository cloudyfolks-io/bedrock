package database

import "testing"

func TestParseConfigRefusesOverlappingAddressRanges(t *testing.T) {
	if _, err := ParseConfig(map[string]string{"database.vip-range": "100.69.0.0/25"}); err == nil {
		t.Fatal("a database range on the load balancer range was accepted")
	}
}
