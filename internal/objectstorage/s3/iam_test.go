package s3

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type seenRequest struct {
	method, path, query, contentType, authorization string
	form                                            url.Values
}

func recordingServer(t *testing.T, reply func(seenRequest, http.ResponseWriter)) (string, func() []seenRequest) {
	t.Helper()
	var mu sync.Mutex
	var seen []seenRequest
	endpoint := serve(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		call := seenRequest{method: r.Method, path: r.URL.EscapedPath(), query: r.URL.RawQuery, contentType: r.Header.Get("Content-Type"), authorization: r.Header.Get("Authorization"), form: form}
		mu.Lock()
		seen = append(seen, call)
		mu.Unlock()
		reply(call, w)
	})
	return endpoint, func() []seenRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenRequest{}, seen...)
	}
}

func iamError(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	io.WriteString(w, `<ErrorResponse><Error><Type>Sender</Type><Code>`+code+`</Code><Message>key AK secret SK</Message></Error></ErrorResponse>`)
}

func TestPutUserPolicySendsASignedForm(t *testing.T) {
	endpoint, requests := recordingServer(t, func(_ seenRequest, w http.ResponseWriter) {
		io.WriteString(w, `<PutUserPolicyResponse></PutUserPolicyResponse>`)
	})
	document := `{"Version":"2012-10-17","Statement":[]}`
	if err := testClient().PutUserPolicy(context.Background(), endpoint, "us-east-1", testCredentials, "acme-prod.app", "access", document); err != nil {
		t.Fatal(err)
	}
	seen := requests()
	if len(seen) != 1 {
		t.Fatalf("requests %+v", seen)
	}
	call := seen[0]
	if call.method != "POST" || call.path != "/" || call.contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("request %+v", call)
	}
	want := url.Values{"Action": {"PutUserPolicy"}, "UserName": {"acme-prod.app"}, "PolicyName": {"access"}, "PolicyDocument": {document}, "Version": {"2010-05-08"}}
	for key := range want {
		if call.form.Get(key) != want.Get(key) {
			t.Fatalf("form %s = %q", key, call.form.Get(key))
		}
	}
	if !strings.Contains(call.authorization, "/iam/aws4_request") || !strings.Contains(call.authorization, "SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("authorization %q", call.authorization)
	}
}

func TestPutUserPolicyMapsMalformedDocument(t *testing.T) {
	endpoint, _ := recordingServer(t, func(_ seenRequest, w http.ResponseWriter) {
		iamError(w, http.StatusBadRequest, "MalformedPolicyDocument")
	})
	err := testClient().PutUserPolicy(context.Background(), endpoint, "us-east-1", testCredentials, "u", "access", "{")
	if !IsCode(err, MalformedPolicyDocument) {
		t.Fatalf("error %v", err)
	}
	if text := err.Error(); strings.Contains(text, "AK") || strings.Contains(text, "SK") {
		t.Fatalf("error %q", text)
	}
}

func TestGetUserPolicyReadsRawAndEncodedDocuments(t *testing.T) {
	raw := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow"}]}`
	cases := map[string]string{
		raw:                 raw,
		url.PathEscape(raw): raw,
	}
	for sent, want := range cases {
		endpoint, requests := recordingServer(t, func(_ seenRequest, w http.ResponseWriter) {
			io.WriteString(w, `<GetUserPolicyResponse><GetUserPolicyResult><UserName>u</UserName><PolicyName>access</PolicyName><PolicyDocument>`+xmlEscape(sent)+`</PolicyDocument></GetUserPolicyResult></GetUserPolicyResponse>`)
		})
		got, found, err := testClient().GetUserPolicy(context.Background(), endpoint, "us-east-1", testCredentials, "u", "access")
		if err != nil || !found || got != want {
			t.Fatalf("got %q %v %v", got, found, err)
		}
		if form := requests()[0].form; form.Get("Action") != "GetUserPolicy" || form.Get("PolicyName") != "access" {
			t.Fatalf("form %+v", form)
		}
	}
}

func xmlEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func TestGetUserPolicyReportsAMissingPolicy(t *testing.T) {
	endpoint, _ := recordingServer(t, func(_ seenRequest, w http.ResponseWriter) {
		iamError(w, http.StatusNotFound, "NoSuchEntity")
	})
	got, found, err := testClient().GetUserPolicy(context.Background(), endpoint, "us-east-1", testCredentials, "u", "access")
	if got != "" || found || err != nil {
		t.Fatalf("got %q %v %v", got, found, err)
	}
}

func TestDeleteUserPolicyToleratesAMissingPolicy(t *testing.T) {
	endpoint, requests := recordingServer(t, func(_ seenRequest, w http.ResponseWriter) {
		iamError(w, http.StatusNotFound, "NoSuchEntity")
	})
	if err := testClient().DeleteUserPolicy(context.Background(), endpoint, "us-east-1", testCredentials, "u", "access"); err != nil {
		t.Fatal(err)
	}
	if form := requests()[0].form; form.Get("Action") != "DeleteUserPolicy" || form.Get("UserName") != "u" {
		t.Fatalf("form %+v", form)
	}
}

func TestDeleteUserPolicyKeepsOtherFailures(t *testing.T) {
	endpoint, _ := recordingServer(t, func(_ seenRequest, w http.ResponseWriter) {
		iamError(w, http.StatusForbidden, "AccessDenied")
	})
	if err := testClient().DeleteUserPolicy(context.Background(), endpoint, "us-east-1", testCredentials, "u", "access"); !IsCode(err, "AccessDenied") {
		t.Fatalf("error %v", err)
	}
}

func TestIAMServerErrorIsUnavailable(t *testing.T) {
	endpoint, _ := recordingServer(t, func(_ seenRequest, w http.ResponseWriter) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	err := testClient().PutUserPolicy(context.Background(), endpoint, "us-east-1", testCredentials, "u", "access", "{}")
	if !IsUnavailable(err) {
		t.Fatalf("error %v", err)
	}
}
