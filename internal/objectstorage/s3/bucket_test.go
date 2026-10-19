package s3

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

type bucketRequest struct {
	method, path, query, body, md5, authorization string
}

func bucketServer(t *testing.T, reply func(bucketRequest, http.ResponseWriter)) (string, func() []bucketRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []bucketRequest
	endpoint := serve(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		call := bucketRequest{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery, body: string(body), md5: r.Header.Get("Content-MD5"), authorization: r.Header.Get("Authorization")}
		mu.Lock()
		seen = append(seen, call)
		mu.Unlock()
		reply(call, w)
	})
	return endpoint, func() []bucketRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]bucketRequest{}, seen...)
	}
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	io.WriteString(w, `<Error><Code>`+code+`</Code><Message>key AK secret SK</Message></Error>`)
}

func TestCreateBucket(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) {})
	if err := testClient().CreateBucket(context.Background(), endpoint, "us-east-1", testCredentials, "acme-data"); err != nil {
		t.Fatal(err)
	}
	call := requests()[0]
	if call.method != "PUT" || call.path != "/acme-data" || call.query != "" || call.body != "" {
		t.Fatalf("request %+v", call)
	}
	if !strings.Contains(call.authorization, "/s3/aws4_request") {
		t.Fatalf("authorization %q", call.authorization)
	}
}

func TestBucketErrorsAreMappedToCodes(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		call   func(*Client, string) error
	}{
		{"create taken", 409, BucketAlreadyExists, func(c *Client, e string) error {
			return c.CreateBucket(context.Background(), e, "us-east-1", testCredentials, "b")
		}},
		{"create owned", 409, BucketAlreadyOwnedByYou, func(c *Client, e string) error {
			return c.CreateBucket(context.Background(), e, "us-east-1", testCredentials, "b")
		}},
		{"delete not empty", 409, BucketNotEmpty, func(c *Client, e string) error {
			return c.DeleteBucket(context.Background(), e, "us-east-1", testCredentials, "b")
		}},
		{"delete missing", 404, NoSuchBucket, func(c *Client, e string) error {
			return c.DeleteBucket(context.Background(), e, "us-east-1", testCredentials, "b")
		}},
	}
	for _, tc := range cases {
		endpoint, _ := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) { s3Error(w, tc.status, tc.code) })
		err := tc.call(testClient(), endpoint)
		if !IsCode(err, tc.code) {
			t.Fatalf("%s: error %v", tc.name, err)
		}
		if text := err.Error(); strings.Contains(text, "AK") || strings.Contains(text, "SK") {
			t.Fatalf("%s: error %q", tc.name, text)
		}
	}
}

func TestDeleteBucket(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) })
	if err := testClient().DeleteBucket(context.Background(), endpoint, "us-east-1", testCredentials, "acme-data"); err != nil {
		t.Fatal(err)
	}
	if call := requests()[0]; call.method != "DELETE" || call.path != "/acme-data" {
		t.Fatalf("request %+v", call)
	}
}

func TestHeadBucketMapsStatusToCodes(t *testing.T) {
	cases := map[int]string{http.StatusNotFound: NoSuchBucket, http.StatusForbidden: AccessDenied}
	for status, code := range cases {
		endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) { w.WriteHeader(status) })
		err := testClient().HeadBucket(context.Background(), endpoint, "us-east-1", testCredentials, "b")
		if !IsCode(err, code) {
			t.Fatalf("status %d: error %v", status, err)
		}
		if requests()[0].method != "HEAD" {
			t.Fatalf("method %q", requests()[0].method)
		}
	}
	endpoint, _ := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) {})
	if err := testClient().HeadBucket(context.Background(), endpoint, "us-east-1", testCredentials, "b"); err != nil {
		t.Fatal(err)
	}
}

func md5Of(body string) string {
	sum := md5.Sum([]byte(body))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func TestPutBucketCors(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) {})
	origins := []string{"https://a.example.test", "https://b.example.test"}
	if err := testClient().PutBucketCors(context.Background(), endpoint, "us-east-1", testCredentials, "b", origins); err != nil {
		t.Fatal(err)
	}
	call := requests()[0]
	want := `<CORSConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><CORSRule><AllowedOrigin>https://a.example.test</AllowedOrigin><AllowedOrigin>https://b.example.test</AllowedOrigin><AllowedMethod>GET</AllowedMethod><AllowedMethod>PUT</AllowedMethod><AllowedMethod>POST</AllowedMethod><AllowedMethod>DELETE</AllowedMethod><AllowedMethod>HEAD</AllowedMethod><AllowedHeader>*</AllowedHeader><ExposeHeader>ETag</ExposeHeader><MaxAgeSeconds>3000</MaxAgeSeconds></CORSRule></CORSConfiguration>`
	if call.method != "PUT" || call.query != "cors" || call.body != want || call.md5 != md5Of(want) {
		t.Fatalf("request %+v", call)
	}
}

func TestPutBucketCorsEscapesOrigins(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) {})
	if err := testClient().PutBucketCors(context.Background(), endpoint, "us-east-1", testCredentials, "b", []string{"https://a.test/?x=1&y=<2>"}); err != nil {
		t.Fatal(err)
	}
	if body := requests()[0].body; !strings.Contains(body, "https://a.test/?x=1&amp;y=&lt;2&gt;") {
		t.Fatalf("body %q", body)
	}
}

func TestDeleteBucketCors(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) { w.WriteHeader(http.StatusNoContent) })
	if err := testClient().DeleteBucketCors(context.Background(), endpoint, "us-east-1", testCredentials, "b"); err != nil {
		t.Fatal(err)
	}
	if call := requests()[0]; call.method != "DELETE" || call.query != "cors" {
		t.Fatalf("request %+v", call)
	}
}

const versionsPage = `<ListVersionsResult><IsTruncated>true</IsTruncated><NextKeyMarker>k2</NextKeyMarker><NextVersionIdMarker>v2</NextVersionIdMarker>` +
	`<Version><Key>a.txt</Key><VersionId>v1</VersionId></Version>` +
	`<Version><Key>b &amp; c.txt</Key><VersionId>null</VersionId></Version>` +
	`<DeleteMarker><Key>a.txt</Key><VersionId>v0</VersionId></DeleteMarker></ListVersionsResult>`

func TestListObjectVersionsReadsAPage(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) { io.WriteString(w, versionsPage) })
	page, err := testClient().ListObjectVersions(context.Background(), endpoint, "us-east-1", testCredentials, "b", "k1", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if call := requests()[0]; call.method != "GET" || call.path != "/b" || call.query != "key-marker=k1&max-keys=1000&version-id-marker=v1&versions" {
		t.Fatalf("request %+v", call)
	}
	want := []ObjectVersion{{Key: "a.txt", VersionID: "v1"}, {Key: "b & c.txt", VersionID: "null"}, {Key: "a.txt", VersionID: "v0"}}
	if !page.Truncated || page.NextKeyMarker != "k2" || page.NextVersionIDMarker != "v2" || len(page.Objects) != 3 {
		t.Fatalf("page %+v", page)
	}
	for i := range want {
		if page.Objects[i] != want[i] {
			t.Fatalf("objects %+v", page.Objects)
		}
	}
}

func TestListObjectVersionsFirstPageHasNoMarkers(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) {
		io.WriteString(w, `<ListVersionsResult><IsTruncated>false</IsTruncated></ListVersionsResult>`)
	})
	page, err := testClient().ListObjectVersions(context.Background(), endpoint, "us-east-1", testCredentials, "b", "", "")
	if err != nil || page.Truncated || len(page.Objects) != 0 {
		t.Fatalf("page %+v %v", page, err)
	}
	if query := requests()[0].query; query != "max-keys=1000&versions" {
		t.Fatalf("query %q", query)
	}
}

func TestDeleteObjectsSendsAnXMLBodyWithItsDigest(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) {
		io.WriteString(w, `<DeleteResult></DeleteResult>`)
	})
	objects := []ObjectVersion{{Key: "a & b.txt", VersionID: "v1"}, {Key: "c.txt"}}
	if err := testClient().DeleteObjects(context.Background(), endpoint, "us-east-1", testCredentials, "b", objects); err != nil {
		t.Fatal(err)
	}
	call := requests()[0]
	want := `<Delete xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Quiet>true</Quiet><Object><Key>a &amp; b.txt</Key><VersionId>v1</VersionId></Object><Object><Key>c.txt</Key></Object></Delete>`
	if call.method != "POST" || call.query != "delete" || call.body != want || call.md5 != md5Of(want) {
		t.Fatalf("request %+v", call)
	}
}

func TestDeleteObjectsReportsPerObjectErrors(t *testing.T) {
	endpoint, _ := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) {
		io.WriteString(w, `<DeleteResult><Error><Key>a.txt</Key><Code>AccessDenied</Code></Error></DeleteResult>`)
	})
	err := testClient().DeleteObjects(context.Background(), endpoint, "us-east-1", testCredentials, "b", []ObjectVersion{{Key: "a.txt"}})
	if !IsCode(err, "AccessDenied") {
		t.Fatalf("error %v", err)
	}
}

func TestDeleteObjectsOfNothingMakesNoCall(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) {})
	if err := testClient().DeleteObjects(context.Background(), endpoint, "us-east-1", testCredentials, "b", nil); err != nil {
		t.Fatal(err)
	}
	if len(requests()) != 0 {
		t.Fatal("a request was made")
	}
}
