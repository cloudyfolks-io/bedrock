package admin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

var (
	credentials = s3.Credentials{AccessKey: "AK", SecretKey: "SK"}
	fixedNow    = time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
)

type seen struct {
	method, path, authorization string
	query                       url.Values
}

func server(t *testing.T, reply func(seen, http.ResponseWriter)) (string, func() []seen) {
	t.Helper()
	var mu sync.Mutex
	var calls []seen
	handler := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := seen{method: r.Method, path: r.URL.Path, authorization: r.Header.Get("Authorization"), query: r.URL.Query()}
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		reply(call, w)
	}))
	t.Cleanup(handler.Close)
	return handler.URL, func() []seen {
		mu.Lock()
		defer mu.Unlock()
		return append([]seen{}, calls...)
	}
}

func client() *Client {
	return &Client{Now: func() time.Time { return fixedNow }}
}

func TestBucketStatsReadsTheMainPool(t *testing.T) {
	endpoint, calls := server(t, func(_ seen, w http.ResponseWriter) {
		io.WriteString(w, `{"bucket":"acme-data","usage":{"rgw.main":{"size_actual":4096,"num_objects":7},"rgw.multimeta":{"size_actual":1,"num_objects":1}}}`)
	})
	stats, err := client().BucketStats(context.Background(), endpoint, "us-east-1", credentials, "acme-data")
	if err != nil || stats.Bytes != 4096 || stats.Objects != 7 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	call := calls()[0]
	if call.method != "GET" || call.path != "/admin/bucket" || call.query.Get("bucket") != "acme-data" || call.query.Get("stats") != "true" || call.query.Get("format") != "json" {
		t.Fatalf("call %+v", call)
	}
	if !strings.Contains(call.authorization, "Credential=AK/20261008/us-east-1/s3/aws4_request") {
		t.Fatalf("authorization %q", call.authorization)
	}
}

func TestBucketStatsOfAnEmptyBucket(t *testing.T) {
	endpoint, _ := server(t, func(_ seen, w http.ResponseWriter) { io.WriteString(w, `{"bucket":"b","usage":{}}`) })
	stats, err := client().BucketStats(context.Background(), endpoint, "us-east-1", credentials, "b")
	if err != nil || stats != (Stats{}) {
		t.Fatalf("stats %+v %v", stats, err)
	}
}

func TestSetBucketQuota(t *testing.T) {
	endpoint, calls := server(t, func(_ seen, w http.ResponseWriter) {})
	if err := client().SetBucketQuota(context.Background(), endpoint, "us-east-1", credentials, "acme-data", 1073741824, 500); err != nil {
		t.Fatal(err)
	}
	call := calls()[0]
	if call.method != "PUT" || call.path != "/admin/bucket" || call.query.Get("bucket") != "acme-data" || call.query.Get("max-size") != "1073741824" || call.query.Get("max-objects") != "500" || call.query.Get("enabled") != "true" {
		t.Fatalf("call %+v", call)
	}
	if _, found := call.query["quota"]; !found {
		t.Fatalf("query %v", call.query)
	}
}

func TestSetBucketQuotaTreatsZeroAsUnlimited(t *testing.T) {
	endpoint, calls := server(t, func(_ seen, w http.ResponseWriter) {})
	if err := client().SetBucketQuota(context.Background(), endpoint, "us-east-1", credentials, "b", 0, 10); err != nil {
		t.Fatal(err)
	}
	if query := calls()[0].query; query.Get("max-size") != "-1" || query.Get("max-objects") != "10" {
		t.Fatalf("query %v", query)
	}
}

func TestRemoveBucketQuota(t *testing.T) {
	endpoint, calls := server(t, func(_ seen, w http.ResponseWriter) {})
	if err := client().RemoveBucketQuota(context.Background(), endpoint, "us-east-1", credentials, "b"); err != nil {
		t.Fatal(err)
	}
	call := calls()[0]
	if call.method != "PUT" || call.query.Get("enabled") != "false" || call.query.Get("bucket") != "b" {
		t.Fatalf("call %+v", call)
	}
}

func TestSuspendAndResumeUser(t *testing.T) {
	endpoint, calls := server(t, func(_ seen, w http.ResponseWriter) {})
	if err := client().SuspendUser(context.Background(), endpoint, "us-east-1", credentials, "acme-prod.app"); err != nil {
		t.Fatal(err)
	}
	if err := client().ResumeUser(context.Background(), endpoint, "us-east-1", credentials, "acme-prod.app"); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if got[0].method != "POST" || got[0].path != "/admin/user" || got[0].query.Get("uid") != "acme-prod.app" || got[0].query.Get("suspended") != "true" || got[1].query.Get("suspended") != "false" {
		t.Fatalf("calls %+v", got)
	}
}

func TestErrorsCarryTheCodeAndHideCredentials(t *testing.T) {
	endpoint, _ := server(t, func(_ seen, w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `{"Code":"AccessDenied","Message":"key AK secret SK","RequestId":"x"}`)
	})
	err := client().SetBucketQuota(context.Background(), endpoint, "us-east-1", credentials, "b", 1, 1)
	if !s3.IsCode(err, "AccessDenied") {
		t.Fatalf("error %v", err)
	}
	if text := err.Error(); strings.Contains(text, "AK") || strings.Contains(text, "SK") {
		t.Fatalf("error %q", text)
	}
}

func TestServerErrorsAreUnavailable(t *testing.T) {
	endpoint, _ := server(t, func(_ seen, w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) })
	if err := client().RemoveBucketQuota(context.Background(), endpoint, "us-east-1", credentials, "b"); !s3.IsUnavailable(err) {
		t.Fatalf("error %v", err)
	}
	if _, err := client().BucketStats(context.Background(), "http://127.0.0.1:1", "us-east-1", credentials, "b"); !s3.IsUnavailable(err) {
		t.Fatalf("unreachable: %v", err)
	}
}

func TestEmptyNamesMakeNoCall(t *testing.T) {
	endpoint, calls := server(t, func(_ seen, w http.ResponseWriter) {})
	if _, err := client().BucketStats(context.Background(), endpoint, "us-east-1", credentials, ""); err == nil {
		t.Fatal("empty bucket name was accepted")
	}
	if err := client().SuspendUser(context.Background(), endpoint, "us-east-1", credentials, ""); err == nil {
		t.Fatal("empty user was accepted")
	}
	if len(calls()) != 0 {
		t.Fatal("a call was made")
	}
}
