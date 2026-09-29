package cliauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/oauth2"
)

var loginScopes = []string{"openid", "profile", "email", "groups", "offline_access"}

type discoveryDocument struct {
	AuthorizationEndpoint       string `json:"authorization_endpoint"`
	TokenEndpoint               string `json:"token_endpoint"`
	DeviceAuthorizationEndpoint string `json:"device_authorization_endpoint"`
}

func discover(ctx context.Context, c *http.Client, issuer string) (discoveryDocument, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return discoveryDocument{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return discoveryDocument{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return discoveryDocument{}, fmt.Errorf("discovery %s: status %d", issuer, resp.StatusCode)
	}
	var doc discoveryDocument
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return discoveryDocument{}, err
	}
	return doc, nil
}

func endpointFor(doc discoveryDocument) oauth2.Endpoint {
	return oauth2.Endpoint{
		AuthURL:       doc.AuthorizationEndpoint,
		TokenURL:      doc.TokenEndpoint,
		DeviceAuthURL: doc.DeviceAuthorizationEndpoint,
		AuthStyle:     oauth2.AuthStyleInParams,
	}
}

func tokensFromOAuth2(token *oauth2.Token) Tokens {
	idToken, _ := token.Extra("id_token").(string)
	return Tokens{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, IDToken: idToken, Expiry: token.Expiry}
}

func DeviceLogin(ctx context.Context, c *http.Client, issuer, clientID string, prompt func(verificationURI, userCode string)) (Tokens, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c)
	doc, err := discover(ctx, c, issuer)
	if err != nil {
		return Tokens{}, err
	}
	conf := &oauth2.Config{ClientID: clientID, Endpoint: endpointFor(doc), Scopes: loginScopes}
	auth, err := conf.DeviceAuth(ctx)
	if err != nil {
		return Tokens{}, err
	}
	prompt(auth.VerificationURIComplete, auth.UserCode)
	token, err := conf.DeviceAccessToken(ctx, auth)
	if err != nil {
		return Tokens{}, err
	}
	return tokensFromOAuth2(token), nil
}

func Refresh(ctx context.Context, c *http.Client, issuer, clientID, refreshToken string) (Tokens, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, c)
	doc, err := discover(ctx, c, issuer)
	if err != nil {
		return Tokens{}, err
	}
	conf := &oauth2.Config{ClientID: clientID, Endpoint: endpointFor(doc), Scopes: loginScopes}
	source := conf.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken, Expiry: time.Unix(0, 0)})
	token, err := source.Token()
	if err != nil {
		return Tokens{}, err
	}
	return tokensFromOAuth2(token), nil
}
