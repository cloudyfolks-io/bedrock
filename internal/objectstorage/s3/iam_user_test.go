package s3

import (
	"context"
	"io"
	"net/http"
	"testing"
)

func TestUserExistsAsksGetUser(t *testing.T) {
	endpoint, requests := recordingServer(t, func(_ seenRequest, w http.ResponseWriter) {
		io.WriteString(w, `<GetUserResponse><GetUserResult><User><UserName>k-8f3a</UserName></User></GetUserResult></GetUserResponse>`)
	})
	found, err := testClient().UserExists(context.Background(), endpoint, "us-east-1", testCredentials, "k-8f3a")
	if err != nil || !found {
		t.Fatalf("found %v err %v", found, err)
	}
	if form := requests()[0].form; form.Get("Action") != "GetUser" || form.Get("UserName") != "k-8f3a" {
		t.Fatalf("form %v", form)
	}
}

func TestUserExistsReportsAMissingUser(t *testing.T) {
	endpoint, _ := recordingServer(t, func(_ seenRequest, w http.ResponseWriter) { iamError(w, http.StatusNotFound, "NoSuchEntity") })
	found, err := testClient().UserExists(context.Background(), endpoint, "us-east-1", testCredentials, "k-none")
	if err != nil || found {
		t.Fatalf("found %v err %v", found, err)
	}
}

func TestUserExistsReportsADeniedCall(t *testing.T) {
	endpoint, _ := recordingServer(t, func(_ seenRequest, w http.ResponseWriter) { iamError(w, http.StatusForbidden, "AccessDenied") })
	if _, err := testClient().UserExists(context.Background(), endpoint, "us-east-1", testCredentials, "k-x"); err == nil {
		t.Fatal("a denied call looks like a missing user")
	}
}
