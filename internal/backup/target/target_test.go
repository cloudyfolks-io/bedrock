package target

import (
	"errors"
	"strings"
	"testing"
)

func TestParseAccepts(t *testing.T) {
	cases := []struct {
		raw  string
		want Target
	}{
		{"s3://s3.eu-west-1.amazonaws.com/my-bucket/bedrock/prod?region=eu-west-1", Target{Scheme: "https", Endpoint: "s3.eu-west-1.amazonaws.com", Bucket: "my-bucket", Prefix: "bedrock/prod", Region: "eu-west-1"}},
		{"s3://seaweedfs.backup-e2e.svc:8333/bedrock-backups?insecure=true", Target{Scheme: "http", Endpoint: "seaweedfs.backup-e2e.svc:8333", Bucket: "bedrock-backups", Prefix: "", Region: "us-east-1"}},
		{"s3://storage.example.com/bucket-1/", Target{Scheme: "https", Endpoint: "storage.example.com", Bucket: "bucket-1", Prefix: "", Region: "us-east-1"}},
	}
	for _, tc := range cases {
		got, err := Parse(tc.raw)
		if err != nil {
			t.Fatalf("%s: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("%s: got %+v want %+v", tc.raw, got, tc.want)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"nfs://nas.example.com/export", "nfs targets are not supported"},
		{"http://host/bucket", "scheme must be s3"},
		{"s3://host", "bucket is missing"},
		{"s3:///bucket", "endpoint is missing"},
		{"s3://user:pw@host/bucket", "credentials in the URL"},
		{"s3://host/Bucket", "bucket name"},
		{"s3://host/ab", "bucket name"},
		{"s3://host/-bucket", "bucket name"},
		{"s3://host/buc_ket", "bucket name"},
		{"s3://host/bucket/../x", "prefix"},
		{"s3://host/bucket//x", "prefix"},
		{"s3://host:0/bucket", "port"},
		{"s3://host:70000/bucket", "port"},
		{"s3://host:abc/bucket", "port"},
		{"s3://host/bucket?x=1", "unknown query key"},
		{"s3://host/bucket?insecure=yes", "insecure must be true"},
		{"s3://host/bucket?region=", "region"},
		{"s3://host/bucket#frag", "fragment"},
	}
	for _, tc := range cases {
		_, err := Parse(tc.raw)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: got %v, want substring %q", tc.raw, err, tc.want)
		}
	}
	if _, err := Parse(""); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty: got %v", err)
	}
}

func TestS3URL(t *testing.T) {
	if got := S3URL(Target{Scheme: "http", Endpoint: "h:8333"}); got != "http://h:8333" {
		t.Fatalf("got %s", got)
	}
}
