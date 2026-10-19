package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func listing(keys map[string]int64, truncated bool, token string) string {
	var out strings.Builder
	out.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	fmt.Fprintf(&out, "<IsTruncated>%v</IsTruncated>", truncated)
	if token != "" {
		out.WriteString("<NextContinuationToken>" + token + "</NextContinuationToken>")
	}
	for key, size := range keys {
		fmt.Fprintf(&out, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", key, size)
	}
	out.WriteString("</ListBucketResult>")
	return out.String()
}

func TestListObjectsV2SendsASortedSignedQueryAndParsesThePage(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) {
		io.WriteString(w, listing(map[string]int64{"uid/wals/1": 100}, true, "next token"))
	})
	page, err := testClient().ListObjectsV2(context.Background(), endpoint, "us-east-1", testCredentials, "backups", "uid/", "tok/1")
	if err != nil {
		t.Fatal(err)
	}
	call := requests()[0]
	if call.method != "GET" || call.path != "/backups" || call.query != "continuation-token=tok%2F1&list-type=2&max-keys=1000&prefix=uid%2F" {
		t.Fatalf("request %+v", call)
	}
	if !strings.Contains(call.authorization, "/s3/aws4_request") {
		t.Fatalf("authorization %q", call.authorization)
	}
	if len(page.Objects) != 1 || page.Objects[0] != (Object{Key: "uid/wals/1", Size: 100}) || !page.Truncated || page.NextToken != "next token" {
		t.Fatalf("page %+v", page)
	}
}

func TestListObjectsV2WithoutPrefixAndTokenHasOnlyTheFixedParts(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) { io.WriteString(w, listing(nil, false, "")) })
	page, err := testClient().ListObjectsV2(context.Background(), endpoint, "us-east-1", testCredentials, "backups", "", "")
	if err != nil || len(page.Objects) != 0 || page.Truncated {
		t.Fatalf("page %+v err %v", page, err)
	}
	if got := requests()[0].query; got != "list-type=2&max-keys=1000" {
		t.Fatalf("query %q", got)
	}
}

func TestListObjectsV2MapsErrors(t *testing.T) {
	endpoint, _ := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) { s3Error(w, 404, NoSuchBucket) })
	if _, err := testClient().ListObjectsV2(context.Background(), endpoint, "us-east-1", testCredentials, "backups", "", ""); !IsCode(err, NoSuchBucket) {
		t.Fatalf("error %v", err)
	}
	endpoint, _ = bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) { s3Error(w, 503, "SlowDown") })
	if _, err := testClient().ListObjectsV2(context.Background(), endpoint, "us-east-1", testCredentials, "backups", "", ""); !IsUnavailable(err) {
		t.Fatalf("error %v", err)
	}
	endpoint, _ = bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) { io.WriteString(w, "not xml") })
	if _, err := testClient().ListObjectsV2(context.Background(), endpoint, "us-east-1", testCredentials, "backups", "", ""); err == nil {
		t.Fatal("an unreadable answer was accepted")
	}
}

func TestUsageOfPrefixSumsEveryPage(t *testing.T) {
	endpoint, requests := bucketServer(t, func(call bucketRequest, w http.ResponseWriter) {
		values, _ := url.ParseQuery(call.query)
		switch values.Get("continuation-token") {
		case "":
			io.WriteString(w, listing(map[string]int64{"uid/a": 10, "uid/b": 20}, true, "t1"))
		case "t1":
			io.WriteString(w, listing(map[string]int64{"uid/c": 30}, true, "t2"))
		default:
			io.WriteString(w, listing(map[string]int64{"uid/d": 40}, false, ""))
		}
	})
	usage, err := testClient().UsageOfPrefix(context.Background(), endpoint, "us-east-1", testCredentials, "backups", "uid/")
	if err != nil || usage != (PrefixUsage{Bytes: 100, Objects: 4}) || len(requests()) != 3 {
		t.Fatalf("usage %+v err %v requests %d", usage, err, len(requests()))
	}
}

func TestDeletePrefixDeletesUntilTheListingIsEmpty(t *testing.T) {
	var mu sync.Mutex
	remaining := map[string]int64{"uid/a": 1, "uid/b": 2, "uid/c": 3}
	var deleted []string
	endpoint, _ := bucketServer(t, func(call bucketRequest, w http.ResponseWriter) {
		mu.Lock()
		defer mu.Unlock()
		if call.method == "GET" {
			io.WriteString(w, listing(remaining, false, ""))
			return
		}
		for key := range remaining {
			if strings.Contains(call.body, "<Key>"+key+"</Key>") {
				deleted = append(deleted, key)
				delete(remaining, key)
			}
		}
		io.WriteString(w, "<DeleteResult></DeleteResult>")
	})
	if err := testClient().DeletePrefix(context.Background(), endpoint, "us-east-1", testCredentials, "backups", "uid-0001/"); err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 || len(deleted) != 3 {
		t.Fatalf("remaining %v deleted %v", remaining, deleted)
	}
}

func TestDeletePrefixOfAnEmptyPrefixMakesNoDeleteCall(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) { io.WriteString(w, listing(nil, false, "")) })
	if err := testClient().DeletePrefix(context.Background(), endpoint, "us-east-1", testCredentials, "backups", "uid-0001/"); err != nil {
		t.Fatal(err)
	}
	if calls := requests(); len(calls) != 1 || calls[0].method != "GET" {
		t.Fatalf("calls %+v", calls)
	}
}

func TestDeletePrefixStopsOnADeleteFailure(t *testing.T) {
	endpoint, _ := bucketServer(t, func(call bucketRequest, w http.ResponseWriter) {
		if call.method == "GET" {
			io.WriteString(w, listing(map[string]int64{"uid/a": 1}, false, ""))
			return
		}
		s3Error(w, 403, AccessDenied)
	})
	if err := testClient().DeletePrefix(context.Background(), endpoint, "us-east-1", testCredentials, "backups", "uid-0001/"); !IsCode(err, AccessDenied) {
		t.Fatalf("error %v", err)
	}
}

func TestDeletePrefixRefusesAnEmptyOrShortPrefix(t *testing.T) {
	endpoint, requests := bucketServer(t, func(_ bucketRequest, w http.ResponseWriter) { io.WriteString(w, listing(map[string]int64{"a/x": 1}, false, "")) })
	for _, prefix := range []string{"", "/", "a/", "uid", "0b0c0d0e-1111-4222-8333-444455556666"} {
		if err := testClient().DeletePrefix(context.Background(), endpoint, "us-east-1", testCredentials, "backups", prefix); err == nil {
			t.Errorf("prefix %q accepted", prefix)
		}
	}
	if len(requests()) != 0 {
		t.Fatalf("calls %+v", requests())
	}
}
