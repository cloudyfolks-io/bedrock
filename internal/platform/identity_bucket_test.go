package platform

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

func TestEnsureStoreBucketChecksOwnershipAfterQuota(t *testing.T) {
	for _, tc := range []struct {
		name        string
		code        string
		listed      string
		wantSuccess bool
	}{
		{"owned after quota", "TooManyBuckets", "backup", true},
		{"owned after conflict", "BucketAlreadyExists", "backup", true},
		{"similar name", "TooManyBuckets", "backup-other", false},
		{"foreign bucket", "BucketAlreadyExists", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			keys := s3.Credentials{AccessKey: "fixture", SecretKey: "fixture"}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut && r.URL.Path == "/backup" {
					w.WriteHeader(http.StatusConflict)
					fmt.Fprintf(w, "<Error><Code>%s</Code></Error>", tc.code)
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/" || r.URL.Query().Get("prefix") != "backup" || r.Header.Get("Authorization") == "" {
					t.Errorf("unexpected ownership request")
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if r.URL.Query().Get("continuation-token") == "" {
					fmt.Fprint(w, "<ListAllMyBucketsResult><ContinuationToken>next</ContinuationToken></ListAllMyBucketsResult>")
					return
				}
				fmt.Fprintf(w, "<ListAllMyBucketsResult><Buckets><Bucket><Name>%s</Name></Bucket></Buckets></ListAllMyBucketsResult>", tc.listed)
			}))
			defer server.Close()
			err := EnsureStoreBucket(t.Context(), &s3.Client{HTTP: server.Client()}, server.URL, "test", keys, "backup")
			if (err == nil) != tc.wantSuccess || err != nil && !s3.IsCode(err, tc.code) {
				t.Fatalf("unexpected bucket result %v", err)
			}
		})
	}
}
