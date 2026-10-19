package database

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
)

type fakeS3 struct {
	mu       sync.Mutex
	objects  map[string]int64
	created  []string
	listed   []string
	deletes  []string
	createAs string
	failList bool
}

func newFakeS3(t testing.TB) (*fakeS3, BackupStore) {
	t.Helper()
	fake := &fakeS3{objects: map[string]int64{}}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	return fake, BackupStore{S3: &s3.Client{}, Endpoint: server.URL, Region: "us-east-1"}
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bucket := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case r.Method == http.MethodPut:
		f.created = append(f.created, bucket)
		if f.createAs != "" {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, "<Error><Code>%s</Code></Error>", f.createAs)
		}
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		f.listed = append(f.listed, r.URL.RawQuery)
		if f.failList {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		f.list(w, r.URL.Query())
	case r.Method == http.MethodPost:
		body, _ := io.ReadAll(r.Body)
		for key := range f.objects {
			if strings.Contains(string(body), "<Key>"+key+"</Key>") {
				f.deletes = append(f.deletes, key)
				delete(f.objects, key)
			}
		}
		io.WriteString(w, "<DeleteResult></DeleteResult>")
	}
}

func (f *fakeS3) list(w http.ResponseWriter, query url.Values) {
	prefix := query.Get("prefix")
	keys := make([]string, 0, len(f.objects))
	for key := range f.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	var out strings.Builder
	out.WriteString("<ListBucketResult><IsTruncated>false</IsTruncated>")
	for _, key := range keys {
		fmt.Fprintf(&out, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", key, f.objects[key])
	}
	out.WriteString("</ListBucketResult>")
	io.WriteString(w, out.String())
}

func (f *fakeS3) put(key string, size int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[key] = size
}

func (f *fakeS3) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func (f *fakeS3) listCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.listed)
}
