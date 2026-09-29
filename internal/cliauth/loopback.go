package cliauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

const loopbackTimeout = 5 * time.Minute

type callbackResult struct {
	code string
	err  error
}

func callbackHandler(state string, result chan<- callbackResult) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if subtle.ConstantTimeCompare([]byte(query.Get("state")), []byte(state)) != 1 {
			fmt.Fprintln(w, "login failed: state mismatch")
			result <- callbackResult{err: fmt.Errorf("unexpected state %q", query.Get("state"))}
			return
		}
		if code := query.Get("code"); code != "" {
			fmt.Fprintln(w, "login complete, you may close this window")
			result <- callbackResult{code: code}
			return
		}
		fmt.Fprintln(w, "login failed")
		result <- callbackResult{err: fmt.Errorf("callback error: %s", query.Get("error"))}
	})
}

func LoopbackLogin(ctx context.Context, c *http.Client, issuer, clientID string, open func(url string) error) (Tokens, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c)
	doc, err := discover(ctx, c, issuer)
	if err != nil {
		return Tokens{}, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Tokens{}, err
	}
	redirectURI := fmt.Sprintf("http://%s/callback", listener.Addr().String())
	conf := &oauth2.Config{ClientID: clientID, Endpoint: endpointFor(doc), Scopes: loginScopes, RedirectURL: redirectURI}
	state := oauth2.GenerateVerifier()
	verifier := oauth2.GenerateVerifier()
	authURL := conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	result := make(chan callbackResult, 1)
	callbackServer := &http.Server{Handler: callbackHandler(state, result)}
	go callbackServer.Serve(listener)
	defer callbackServer.Close()
	if err := open(authURL); err != nil {
		return Tokens{}, err
	}
	select {
	case r := <-result:
		if r.err != nil {
			return Tokens{}, r.err
		}
		token, err := conf.Exchange(ctx, r.code, oauth2.VerifierOption(verifier))
		if err != nil {
			return Tokens{}, err
		}
		return tokensFromOAuth2(token), nil
	case <-time.After(loopbackTimeout):
		return Tokens{}, errors.New("loopback login timed out")
	}
}
