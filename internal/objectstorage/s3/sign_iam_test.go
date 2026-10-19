package s3

import "testing"

func TestSignMatchesBotocoreForIAM(t *testing.T) {
	request := Request{
		Service:       "iam",
		Method:        "POST",
		Host:          "rgw.test:8080",
		Path:          "/",
		Headers:       map[string]string{"Content-Type": "application/x-www-form-urlencoded"},
		PayloadSHA256: "c61af6d1acfdf02fd645225452693c272719309b1bcf2cdf027273247aefa46c",
	}
	got := Sign(request, exampleCredentials, "us-east-1", exampleTime)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/iam/aws4_request,SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date,Signature=5dd0781c1db3c58640bf2a293d0f8597995e1b58e24fc96832ba11ea9b252be9"
	if got["Authorization"] != want {
		t.Fatalf("got %q", got["Authorization"])
	}
}

func TestSignDefaultsToTheS3Service(t *testing.T) {
	plain := Request{Method: "GET", Host: "h", Path: "/", PayloadSHA256: emptyBodyHash}
	named := plain
	named.Service = "s3"
	if Sign(plain, exampleCredentials, "us-east-1", exampleTime)["Authorization"] != Sign(named, exampleCredentials, "us-east-1", exampleTime)["Authorization"] {
		t.Fatal("the default service is not s3")
	}
	other := plain
	other.Service = "iam"
	if Sign(plain, exampleCredentials, "us-east-1", exampleTime)["Authorization"] == Sign(other, exampleCredentials, "us-east-1", exampleTime)["Authorization"] {
		t.Fatal("the service does not change the signature")
	}
}
