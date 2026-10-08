package s3

import (
	"reflect"
	"testing"
	"time"
)

var (
	exampleCredentials = Credentials{AccessKey: "AKIAIOSFODNN7EXAMPLE", SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"}
	exampleTime        = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	emptyBodyHash      = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

func TestSignMatchesTheAWSExamples(t *testing.T) {
	cases := []struct {
		name          string
		request       Request
		authorization string
	}{
		{
			"get object",
			Request{Method: "GET", Host: "examplebucket.s3.amazonaws.com", Path: "/test.txt", Headers: map[string]string{"Range": "bytes=0-9"}, PayloadSHA256: emptyBodyHash},
			"AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=host;range;x-amz-content-sha256;x-amz-date,Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			"put object",
			Request{Method: "PUT", Host: "examplebucket.s3.amazonaws.com", Path: "/test$file.text", Headers: map[string]string{"x-amz-storage-class": "REDUCED_REDUNDANCY", "Date": "Fri, 24 May 2013 00:00:00 GMT"}, PayloadSHA256: "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072"},
			"AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class,Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			"get lifecycle",
			Request{Method: "GET", Host: "examplebucket.s3.amazonaws.com", Path: "/", RawQuery: "lifecycle", PayloadSHA256: emptyBodyHash},
			"AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=host;x-amz-content-sha256;x-amz-date,Signature=fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			"list objects",
			Request{Method: "GET", Host: "examplebucket.s3.amazonaws.com", Path: "/", RawQuery: "max-keys=2&prefix=J", PayloadSHA256: emptyBodyHash},
			"AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request,SignedHeaders=host;x-amz-content-sha256;x-amz-date,Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Sign(tc.request, exampleCredentials, "us-east-1", exampleTime)
			want := map[string]string{"Authorization": tc.authorization, "x-amz-date": "20130524T000000Z", "x-amz-content-sha256": tc.request.PayloadSHA256}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestSignEncodesThePath(t *testing.T) {
	if got := canonicalPath("/a b/ü+x.txt"); got != "/a%20b/%C3%BC%2Bx.txt" {
		t.Fatalf("path %q", got)
	}
	headers := map[string]string{"Range": "bytes=0-9"}
	request := Request{Method: "GET", Host: "h", Path: "/a b/ü+x.txt", Headers: headers, PayloadSHA256: emptyBodyHash}
	first := Sign(request, exampleCredentials, "us-east-1", exampleTime)
	second := Sign(request, exampleCredentials, "us-east-1", exampleTime)
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(headers, map[string]string{"Range": "bytes=0-9"}) {
		t.Fatalf("sign is not pure: %+v %+v", first, headers)
	}
}

func TestSignSortsAndEncodesTheQuery(t *testing.T) {
	unsorted := Request{Method: "GET", Host: "h", Path: "/", RawQuery: "b=2&a=1&a=%2F", PayloadSHA256: emptyBodyHash}
	sorted := Request{Method: "GET", Host: "h", Path: "/", RawQuery: "a=%2F&a=1&b=2", PayloadSHA256: emptyBodyHash}
	if !reflect.DeepEqual(Sign(unsorted, exampleCredentials, "us-east-1", exampleTime), Sign(sorted, exampleCredentials, "us-east-1", exampleTime)) {
		t.Fatal("query order changes the signature")
	}
	if got := canonicalQuery("b=2&a=1&a=%2F"); got != "a=%2F&a=1&b=2" {
		t.Fatalf("query %q", got)
	}
}

func TestSignSortsQueryByKeyThenValue(t *testing.T) {
	if got := canonicalQuery("a-b=1&a=2&a=1"); got != "a=1&a=2&a-b=1" {
		t.Fatalf("query %q", got)
	}
}
