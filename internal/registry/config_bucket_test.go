package registry

import "testing"

func TestParseConfigRefusesOneBucketForBoth(t *testing.T) {
	if _, err := ParseConfig(map[string]string{"registry.bucket": "bedrock-database-backups"}); err == nil {
		t.Fatal("the registry bucket equal to the backup bucket was accepted")
	}
}
