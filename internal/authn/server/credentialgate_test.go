package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func alwaysMatches(*http.Request) bool { return true }

func neverMatches(*http.Request) bool { return false }

func blockingHandler(started, release chan struct{}) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		w.WriteHeader(http.StatusOK)
	})
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func panicHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("boom") })
}

func rateLimitedBody(t *testing.T, resp *http.Response) bool {
	t.Helper()
	if resp.StatusCode != http.StatusTooManyRequests {
		return false
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body["error"] == "rate_limited"
}

func TestCredentialGateLimitsInFlightChecks(t *testing.T) {
	gate := newCredentialGate(1, 150*time.Millisecond)
	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.Handle("/gated", gate.middleware(alwaysMatches, blockingHandler(started, release)))
	mux.Handle("/probe", gate.middleware(alwaysMatches, okHandler()))
	server := httptest.NewServer(mux)
	defer server.Close()

	holderDone := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get(server.URL + "/gated")
		if err != nil {
			t.Error(err)
			holderDone <- nil
			return
		}
		holderDone <- resp
	}()
	<-started

	before := time.Now()
	overflow, err := http.Get(server.URL + "/probe")
	if err != nil {
		t.Fatal(err)
	}
	if !rateLimitedBody(t, overflow) {
		t.Fatalf("overflow status %d, want 429 rate_limited", overflow.StatusCode)
	}
	if waited := time.Since(before); waited < 100*time.Millisecond {
		t.Fatalf("the overflow request must wait for the gate's timeout, waited %s", waited)
	}

	close(release)
	holder := <-holderDone
	if holder == nil || holder.StatusCode != http.StatusOK {
		t.Fatalf("holder response %+v", holder)
	}

	freed, err := http.Get(server.URL + "/probe")
	if err != nil {
		t.Fatal(err)
	}
	if freed.StatusCode != http.StatusOK {
		t.Fatalf("a released slot must accept the next request immediately: status %d", freed.StatusCode)
	}
}

func TestCredentialGateReleasesOnPanic(t *testing.T) {
	gate := newCredentialGate(1, 50*time.Millisecond)
	mux := http.NewServeMux()
	mux.Handle("/panic", gate.middleware(alwaysMatches, panicHandler()))
	mux.Handle("/probe", gate.middleware(alwaysMatches, okHandler()))
	server := httptest.NewServer(mux)
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	if _, err := client.Get(server.URL + "/panic"); err == nil {
		t.Fatal("a panicking handler must not return a normal response")
	}

	freed := make(chan *http.Response, 1)
	go func() {
		resp, err := http.Get(server.URL + "/probe")
		if err != nil {
			t.Error(err)
			freed <- nil
			return
		}
		freed <- resp
	}()
	select {
	case resp := <-freed:
		if resp == nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("request after the panic failed: %+v", resp)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("the slot was not released after the handler panicked")
	}
}

func TestCredentialGateOnlyGatesMatchingRequests(t *testing.T) {
	gate := newCredentialGate(1, 200*time.Millisecond)
	started := make(chan struct{})
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.Handle("/gated", gate.middleware(alwaysMatches, blockingHandler(started, release)))
	mux.Handle("/open", gate.middleware(neverMatches, okHandler()))
	server := httptest.NewServer(mux)
	defer server.Close()
	defer close(release)

	go func() { _, _ = http.Get(server.URL + "/gated") }()
	<-started

	resp, err := http.Get(server.URL + "/open")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("an unmatched route must never wait for a slot: status %d", resp.StatusCode)
	}
}

func TestCredentialGateRoutePredicates(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		login  bool
		acct   bool
	}{
		{"login answer", http.MethodPost, pathLoginAnswer, true, false},
		{"login answer wrong method", http.MethodGet, pathLoginAnswer, false, false},
		{"login challenge", http.MethodGet, pathLoginChallenge, true, false},
		{"login challenge wrong method", http.MethodPost, pathLoginChallenge, false, false},
		{"account password", http.MethodPost, pathAccountPassword, false, true},
		{"account totp verify", http.MethodPost, pathAccountTOTPVerify, false, true},
		{"account recovery codes", http.MethodPost, pathAccountRecoveryCodes, false, true},
		{"account recovery codes wrong method", http.MethodGet, pathAccountRecoveryCodes, false, false},
		{"account show", http.MethodGet, pathAccount, false, false},
		{"account totp begin", http.MethodPost, pathAccount + "/totp", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, tc.path, nil)
			if got := isLoginCredentialCheck(r); got != tc.login {
				t.Fatalf("isLoginCredentialCheck(%s %s) = %v, want %v", tc.method, tc.path, got, tc.login)
			}
			if got := isAccountCredentialCheck(r); got != tc.acct {
				t.Fatalf("isAccountCredentialCheck(%s %s) = %v, want %v", tc.method, tc.path, got, tc.acct)
			}
		})
	}
}
