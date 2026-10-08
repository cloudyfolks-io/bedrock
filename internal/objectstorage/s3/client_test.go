package s3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	testCredentials = Credentials{AccessKey: "AK", SecretKey: "SK"}
	testNow         = time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
)

func testClient() *Client {
	return &Client{Now: func() time.Time { return testNow }}
}

func serve(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server.URL
}

func TestGetVersioningParsesTheStatus(t *testing.T) {
	cases := map[string]string{
		`<VersioningConfiguration><Status>Enabled</Status></VersioningConfiguration>`:   "Enabled",
		`<VersioningConfiguration><Status>Suspended</Status></VersioningConfiguration>`: "Suspended",
		`<VersioningConfiguration></VersioningConfiguration>`:                           "",
	}
	for body, want := range cases {
		endpoint := serve(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) })
		got, err := testClient().GetVersioning(context.Background(), endpoint, "us-east-1", testCredentials, "data-x1y2z")
		if err != nil || got != want {
			t.Fatalf("%q: %q %v", body, got, err)
		}
	}
}

func TestPutVersioningSendsSignedXML(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	endpoint := serve(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, r.Method, r.URL.Path, r.URL.RawQuery, string(body), r.Header.Get("Authorization"), r.Header.Get("x-amz-content-sha256"))
	})
	if err := testClient().PutVersioning(context.Background(), endpoint, "us-east-1", testCredentials, "data-x1y2z", VersioningEnabled); err != nil {
		t.Fatal(err)
	}
	wantBody := `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>Enabled</Status></VersioningConfiguration>`
	if seen[0] != "PUT" || seen[1] != "/data-x1y2z" || seen[2] != "versioning" || seen[3] != wantBody {
		t.Fatalf("request %q", seen)
	}
	if !strings.HasPrefix(seen[4], "AWS4-HMAC-SHA256 Credential=AK/20261008/us-east-1/s3/aws4_request") {
		t.Fatalf("authorization %q", seen[4])
	}
	if seen[5] != hexSHA256(wantBody) {
		t.Fatalf("hash %q", seen[5])
	}
}

func TestPolicyRoundTrip(t *testing.T) {
	var mu sync.Mutex
	stored := ""
	endpoint := serve(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.RawQuery != "policy" {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			stored = string(body)
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			if stored == "" {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `<Error><Code>NoSuchBucketPolicy</Code></Error>`)
				return
			}
			io.WriteString(w, stored)
		case http.MethodDelete:
			if stored == "" {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `<Error><Code>NoSuchBucketPolicy</Code></Error>`)
				return
			}
			stored = ""
			w.WriteHeader(http.StatusNoContent)
		}
	})
	client := testClient()
	ctx := context.Background()
	document := `{"Statement":[{"Effect":"Allow"}]}`
	if err := client.PutPolicy(ctx, endpoint, "us-east-1", testCredentials, "b", document); err != nil {
		t.Fatal(err)
	}
	got, found, err := client.GetPolicy(ctx, endpoint, "us-east-1", testCredentials, "b")
	if err != nil || !found || got != document {
		t.Fatalf("get %q %v %v", got, found, err)
	}
	if err := client.DeletePolicy(ctx, endpoint, "us-east-1", testCredentials, "b"); err != nil {
		t.Fatal(err)
	}
	if got, found, err := client.GetPolicy(ctx, endpoint, "us-east-1", testCredentials, "b"); got != "" || found || err != nil {
		t.Fatalf("after delete %q %v %v", got, found, err)
	}
	if err := client.DeletePolicy(ctx, endpoint, "us-east-1", testCredentials, "b"); err != nil {
		t.Fatalf("delete of a missing policy: %v", err)
	}
}

func TestS3ErrorsHideCredentials(t *testing.T) {
	endpoint := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, `<Error><Code>AccessDenied</Code><Message>key AK secret SK</Message></Error>`)
	})
	err := testClient().PutPolicy(context.Background(), endpoint, "us-east-1", testCredentials, "b", "{}")
	if err == nil {
		t.Fatal("no error")
	}
	text := err.Error()
	if !strings.Contains(text, "403") || !strings.Contains(text, "AccessDenied") || strings.Contains(text, "AK") || strings.Contains(text, "SK") || strings.Contains(text, "secret") {
		t.Fatalf("error %q", text)
	}
}

func TestClientEncodesBucketNames(t *testing.T) {
	var mu sync.Mutex
	paths := []string{}
	endpoint := serve(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.EscapedPath())
	})
	if err := testClient().PutPolicy(context.Background(), endpoint, "us-east-1", testCredentials, "a.b-c", "{}"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != "/a.b-c" {
		t.Fatalf("paths %q", paths)
	}
	if err := testClient().PutPolicy(context.Background(), endpoint, "us-east-1", testCredentials, "", "{}"); err == nil || len(paths) != 1 {
		t.Fatalf("empty bucket: %v %q", err, paths)
	}
}
