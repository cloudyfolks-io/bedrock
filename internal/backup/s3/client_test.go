package s3

import (
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeServer struct {
	mu       sync.Mutex
	objects  map[string]string
	failPuts int
	pageSize int
	puts     int
	server   *httptest.Server
}

func newFakeServer(t *testing.T) *fakeServer {
	t.Helper()
	f := &fakeServer{objects: map[string]string{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeServer) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AK/") || r.Header.Get("x-amz-content-sha256") == "" {
		http.Error(w, "unsigned", http.StatusForbidden)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	switch r.Method {
	case http.MethodPut:
		f.puts++
		if f.puts <= f.failPuts {
			http.Error(w, "slow down", http.StatusServiceUnavailable)
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = string(body)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	case http.MethodGet:
		f.list(w, r.URL.Query())
	}
}

func (f *fakeServer) list(w http.ResponseWriter, query url.Values) {
	type item struct{ Key string }
	var out struct {
		XMLName               xml.Name `xml:"ListBucketResult"`
		Contents              []item
		IsTruncated           bool
		NextContinuationToken string
	}
	var matching []string
	for key := range f.objects {
		if strings.HasPrefix(key, query.Get("prefix")) {
			matching = append(matching, key)
		}
	}
	slices.Sort(matching)
	start, _ := strconv.Atoi(query.Get("continuation-token"))
	end := min(start+f.pageSize, len(matching))
	if f.pageSize == 0 {
		end = len(matching)
	}
	for _, key := range matching[start:end] {
		out.Contents = append(out.Contents, item{key})
	}
	if end < len(matching) {
		out.IsTruncated, out.NextContinuationToken = true, strconv.Itoa(end)
	}
	xml.NewEncoder(w).Encode(out)
}

func (f *fakeServer) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func testClient(f *fakeServer, sleeps *[]time.Duration) Client {
	return Client{
		Endpoint: f.server.URL, Region: "us-east-1", Bucket: "bucket", AccessKey: "AK", SecretKey: "SK",
		HTTP: f.server.Client(), Now: time.Now,
		Sleep: func(d time.Duration) { *sleeps = append(*sleeps, d) },
	}
}

func TestPutListDelete(t *testing.T) {
	f := newFakeServer(t)
	var sleeps []time.Duration
	c := testClient(f, &sleeps)
	file := filepath.Join(t.TempDir(), "a.age")
	os.WriteFile(file, []byte("sealed"), 0o600)
	if err := c.Put(context.Background(), "p/etcd/n/a.age", file); err != nil {
		t.Fatal(err)
	}
	if f.objects["p/etcd/n/a.age"] != "sealed" {
		t.Fatalf("objects %v", f.objects)
	}
	keys, err := c.List(context.Background(), "p/etcd/n/")
	if err != nil || !slices.Equal(keys, []string{"p/etcd/n/a.age"}) {
		t.Fatalf("keys %v err %v", keys, err)
	}
	if err := c.Delete(context.Background(), "p/etcd/n/a.age"); err != nil || len(f.keys()) != 0 {
		t.Fatalf("keys %v err %v", f.keys(), err)
	}
}

func TestPutRetriesWithBackoff(t *testing.T) {
	f := newFakeServer(t)
	f.failPuts = 2
	var sleeps []time.Duration
	c := testClient(f, &sleeps)
	file := filepath.Join(t.TempDir(), "a.age")
	os.WriteFile(file, []byte("sealed"), 0o600)
	if err := c.Put(context.Background(), "a.age", file); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(sleeps, []time.Duration{time.Second, 2 * time.Second}) {
		t.Fatalf("sleeps %v", sleeps)
	}
}

func TestPutGivesUpAfterAttempts(t *testing.T) {
	f := newFakeServer(t)
	f.failPuts = 100
	var sleeps []time.Duration
	c := testClient(f, &sleeps)
	file := filepath.Join(t.TempDir(), "a.age")
	os.WriteFile(file, []byte("sealed"), 0o600)
	if err := c.Put(context.Background(), "a.age", file); err == nil || f.puts != attempts {
		t.Fatalf("puts %d err %v", f.puts, err)
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	deny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "no", http.StatusForbidden) }))
	defer deny.Close()
	var sleeps []time.Duration
	c := Client{Endpoint: deny.URL, Region: "r", Bucket: "bucket", AccessKey: "AK", SecretKey: "SK", HTTP: deny.Client(), Now: time.Now, Sleep: func(d time.Duration) { sleeps = append(sleeps, d) }}
	if _, err := c.List(context.Background(), "x/"); err == nil || len(sleeps) != 0 {
		t.Fatalf("sleeps %v err %v", sleeps, err)
	}
}

func TestListFollowsContinuationToken(t *testing.T) {
	f := newFakeServer(t)
	f.pageSize = 2
	for _, key := range []string{"p/a", "p/b", "p/c", "p/d", "p/e", "q/z"} {
		f.objects[key] = "x"
	}
	var sleeps []time.Duration
	keys, err := testClient(f, &sleeps).List(context.Background(), "p/")
	if err != nil || !slices.Equal(keys, []string{"p/a", "p/b", "p/c", "p/d", "p/e"}) {
		t.Fatalf("keys %v err %v", keys, err)
	}
}
