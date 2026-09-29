package k0s

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

type Token struct {
	Version      string            `json:"version"`
	Roles        []string          `json:"roles"`
	K0sToken     string            `json:"k0sToken"`
	K0sConfig    []byte            `json:"k0sConfig,omitempty"`
	VIP          string            `json:"vip"`
	Image        string            `json:"image"`
	K0sVersion   string            `json:"k0sVersion"`
	K0sChecksums map[string]string `json:"k0sChecksums"`
	SupportedOS  []string          `json:"supportedOS"`
	Mirror       string            `json:"mirror,omitempty"`
	AuthnFiles   map[string][]byte `json:"authnFiles,omitempty"`
}

func EncodeToken(t Token) (string, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func DecodeToken(s string) (Token, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Token{}, fmt.Errorf("decode token: %w", err)
	}
	var t Token
	if err := json.Unmarshal(raw, &t); err != nil {
		return Token{}, fmt.Errorf("parse token: %w", err)
	}
	if t.Version == "" || t.K0sToken == "" || len(t.Roles) == 0 {
		return Token{}, fmt.Errorf("token is missing required fields")
	}
	return t, nil
}
