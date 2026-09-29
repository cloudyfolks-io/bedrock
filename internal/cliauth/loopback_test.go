package cliauth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func openLoopback(op testOP, authURL, username, password string) error {
	browser := newBrowser(op)
	resp, err := browser.Get(authURL)
	if err != nil {
		return err
	}
	resp.Body.Close()
	location, err := resp.Location()
	if err != nil {
		return err
	}
	authRequest := location.Query().Get("authRequest")
	challenge, err := postJSON(browser, op.issuer+"/api/v1/login/start", "", map[string]string{"authRequest": authRequest})
	if err != nil {
		return err
	}
	challenge, err = answerUsernameAndPassword(browser, op.issuer, challenge.CSRF, username, password)
	if err != nil {
		return err
	}
	if challenge.Type != "done" || challenge.Redirect == "" {
		return fmt.Errorf("login did not finish: %+v", challenge)
	}
	final, err := browser.Get(challenge.Redirect)
	if err != nil {
		return err
	}
	final.Body.Close()
	callback, err := final.Location()
	if err != nil {
		return err
	}
	last, err := browser.Get(callback.String())
	if err != nil {
		return err
	}
	return last.Body.Close()
}

func TestLoopbackLogin(t *testing.T) {
	op := startTestOP(t)
	tokens, err := LoopbackLogin(context.Background(), op.client, op.issuer, "bedrock-cli", func(authURL string) error {
		return openLoopback(op, authURL, "alice", "correct horse battery staple")
	})
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.IDToken == "" {
		t.Fatalf("incomplete tokens: %+v", tokens)
	}
}

func TestLoopbackRefusesWrongState(t *testing.T) {
	op := startTestOP(t)
	_, err := LoopbackLogin(context.Background(), op.client, op.issuer, "bedrock-cli", func(authURL string) error {
		parsed, err := url.Parse(authURL)
		if err != nil {
			return err
		}
		redirect := parsed.Query().Get("redirect_uri")
		resp, err := http.Get(redirect + "?state=wrong&code=bogus")
		if err != nil {
			return err
		}
		return resp.Body.Close()
	})
	if err == nil || !strings.Contains(err.Error(), "state") {
		t.Fatalf("want a state error, got %v", err)
	}
}
