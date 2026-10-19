package objectstorage

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type fakeObject struct{ key, version string }

type fakeS3 struct {
	mu           sync.Mutex
	buckets      map[string]bool
	foreign      map[string]bool
	hideFromHead map[string]bool
	objects      map[string][]fakeObject
	versioning   map[string]string
	policies     map[string]string
	cors         map[string][]string
	calls        []string
	credentials  []string
	failWith     int
	failAdmin    int
	stats        map[string][2]int64
	quotas       map[string]url.Values
}

func newFakeS3(t *testing.T) (*fakeS3, string) {
	t.Helper()
	store := &fakeS3{
		buckets:      map[string]bool{},
		foreign:      map[string]bool{},
		hideFromHead: map[string]bool{},
		objects:      map[string][]fakeObject{},
		versioning:   map[string]string{},
		policies:     map[string]string{},
		cors:         map[string][]string{},
		stats:        map[string][2]int64{},
		quotas:       map[string]url.Values{},
	}
	server := httptest.NewServer(http.HandlerFunc(store.serve))
	t.Cleanup(server.Close)
	return store, server.URL
}

func (f *fakeS3) fill(bucket string, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buckets[bucket] = true
	for i := 0; i < count; i++ {
		f.objects[bucket] = append(f.objects[bucket], fakeObject{key: fmt.Sprintf("object-%05d", i), version: fmt.Sprintf("v%d", i)})
	}
}

func (f *fakeS3) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	taken := f.calls
	f.calls = nil
	return taken
}

func (f *fakeS3) has(bucket string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.buckets[bucket]
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	bucket := strings.TrimPrefix(r.URL.Path, "/")
	query := r.URL.Query()
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/admin/") {
		f.adminCall(w, r)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/admin/") {
		f.adminCall(w, r)
		return
	}
	kind := ""
	for _, candidate := range []string{"versioning", "policy", "cors", "versions", "delete"} {
		if _, found := query[candidate]; found {
			kind = candidate
		}
	}
	f.calls = append(f.calls, strings.TrimSpace(r.Method+" "+kind))
	f.credentials = append(f.credentials, r.Header.Get("Authorization"))
	if f.failWith != 0 {
		w.WriteHeader(f.failWith)
		return
	}
	switch kind {
	case "":
		f.bucketCall(w, r.Method, bucket)
	case "versioning":
		f.versioningCall(w, r.Method, bucket, string(body))
	case "policy":
		f.policyCall(w, r.Method, bucket, string(body))
	case "cors":
		f.corsCall(w, r.Method, bucket, string(body))
	case "versions":
		f.versionsCall(w, bucket)
	case "delete":
		f.deleteCall(w, bucket, string(body))
	}
}

func answerError(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	io.WriteString(w, `<Error><Code>`+code+`</Code></Error>`)
}

func (f *fakeS3) bucketCall(w http.ResponseWriter, method, bucket string) {
	switch method {
	case http.MethodHead:
		switch {
		case f.hideFromHead[bucket]:
			w.WriteHeader(http.StatusNotFound)
		case f.foreign[bucket]:
			w.WriteHeader(http.StatusForbidden)
		case !f.buckets[bucket]:
			w.WriteHeader(http.StatusNotFound)
		}
	case http.MethodPut:
		switch {
		case f.foreign[bucket]:
			answerError(w, http.StatusConflict, "BucketAlreadyExists")
		case f.buckets[bucket]:
			answerError(w, http.StatusConflict, "BucketAlreadyOwnedByYou")
		default:
			f.buckets[bucket] = true
		}
	case http.MethodDelete:
		switch {
		case !f.buckets[bucket]:
			answerError(w, http.StatusNotFound, "NoSuchBucket")
		case len(f.objects[bucket]) > 0:
			answerError(w, http.StatusConflict, "BucketNotEmpty")
		default:
			delete(f.buckets, bucket)
			w.WriteHeader(http.StatusNoContent)
		}
	}
}

func (f *fakeS3) versioningCall(w http.ResponseWriter, method, bucket, body string) {
	if method == http.MethodGet {
		io.WriteString(w, `<VersioningConfiguration><Status>`+f.versioning[bucket]+`</Status></VersioningConfiguration>`)
		return
	}
	status := "Enabled"
	if strings.Contains(body, "<Status>Suspended</Status>") {
		status = "Suspended"
	}
	f.versioning[bucket] = status
}

func (f *fakeS3) policyCall(w http.ResponseWriter, method, bucket, body string) {
	stored, found := f.policies[bucket]
	switch method {
	case http.MethodGet:
		if !found {
			answerError(w, http.StatusNotFound, "NoSuchBucketPolicy")
			return
		}
		io.WriteString(w, stored)
	case http.MethodPut:
		f.policies[bucket] = body
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		delete(f.policies, bucket)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *fakeS3) corsCall(w http.ResponseWriter, method, bucket, body string) {
	if method == http.MethodDelete {
		delete(f.cors, bucket)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var configuration struct {
		Origins []string `xml:"CORSRule>AllowedOrigin"`
	}
	_ = xml.Unmarshal([]byte(body), &configuration)
	f.cors[bucket] = configuration.Origins
}

func (f *fakeS3) versionsCall(w http.ResponseWriter, bucket string) {
	page := f.objects[bucket]
	truncated := len(page) > 1000
	if truncated {
		page = page[:1000]
	}
	var out strings.Builder
	fmt.Fprintf(&out, "<ListVersionsResult><IsTruncated>%v</IsTruncated>", truncated)
	for _, object := range page {
		fmt.Fprintf(&out, "<Version><Key>%s</Key><VersionId>%s</VersionId></Version>", object.key, object.version)
	}
	out.WriteString("</ListVersionsResult>")
	io.WriteString(w, out.String())
}

func (f *fakeS3) deleteCall(w http.ResponseWriter, bucket, body string) {
	var request struct {
		Objects []struct {
			Key     string `xml:"Key"`
			Version string `xml:"VersionId"`
		} `xml:"Object"`
	}
	_ = xml.Unmarshal([]byte(body), &request)
	gone := map[string]bool{}
	for _, object := range request.Objects {
		gone[object.Key+"/"+object.Version] = true
	}
	kept := f.objects[bucket][:0:0]
	for _, object := range f.objects[bucket] {
		if !gone[object.key+"/"+object.version] {
			kept = append(kept, object)
		}
	}
	f.objects[bucket] = kept
	io.WriteString(w, `<DeleteResult></DeleteResult>`)
}

func (f *fakeS3) adminCall(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	bucket := query.Get("bucket")
	f.calls = append(f.calls, "ADMIN "+r.Method+" "+r.URL.Path)
	f.credentials = append(f.credentials, r.Header.Get("Authorization"))
	if f.failAdmin != 0 {
		w.WriteHeader(f.failAdmin)
		return
	}
	if r.Method == http.MethodGet {
		numbers := f.stats[bucket]
		answer := map[string]any{"bucket": bucket, "usage": map[string]any{"rgw.main": map[string]any{"size_actual": numbers[0], "num_objects": numbers[1]}}}
		_ = json.NewEncoder(w).Encode(answer)
		return
	}
	if query.Get("enabled") == "false" {
		delete(f.quotas, bucket)
		return
	}
	f.quotas[bucket] = query
}

func (f *fakeS3) quota(bucket string) (url.Values, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	query, found := f.quotas[bucket]
	return query, found
}

func (f *fakeS3) countCalls(call string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, seen := range f.calls {
		if seen == call {
			count++
		}
	}
	return count
}
