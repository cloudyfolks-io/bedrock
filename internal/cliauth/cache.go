package cliauth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Tokens struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	IDToken      string    `json:"idToken"`
	Expiry       time.Time `json:"expiry"`
}

func CachePath(home, issuer string) string {
	sum := sha256.Sum256([]byte(issuer))
	return filepath.Join(home, ".config", "bedrock", "tokens", hex.EncodeToString(sum[:])[:16]+".json")
}

func ReadCache(path string) (Tokens, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Tokens{}, err
	}
	var tokens Tokens
	if err := json.Unmarshal(data, &tokens); err != nil {
		return Tokens{}, err
	}
	return tokens, nil
}

func WriteCache(path string, tokens Tokens) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(tokens)
	if err != nil {
		return err
	}
	temp := path + ".tmp"
	if err := os.WriteFile(temp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func Fresh(tokens Tokens, now time.Time) bool {
	return tokens.Expiry.Sub(now) > 60*time.Second
}
