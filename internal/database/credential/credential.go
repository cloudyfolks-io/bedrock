package credential

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"math/big"
)

const (
	alphanumeric    = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	scramIterations = 4096
	PasswordLength  = 32
)

func Generate(n int) (string, error) {
	out := make([]byte, n)
	max := big.NewInt(int64(len(alphanumeric)))
	for i := range out {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("read random: %w", err)
		}
		out[i] = alphanumeric[v.Int64()]
	}
	return string(out), nil
}

func Password() (string, error) { return Generate(PasswordLength) }

func ScramSHA256(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read random: %w", err)
	}
	return ScramSHA256WithSalt(password, salt)
}

func ScramSHA256WithSalt(password string, salt []byte) (string, error) {
	salted, err := pbkdf2.Key(sha256.New, password, salt, scramIterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("derive key: %w", err)
	}
	clientKey := hmacSHA256(salted, "Client Key")
	stored := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, "Server Key")
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", scramIterations,
		base64.StdEncoding.EncodeToString(salt),
		base64.StdEncoding.EncodeToString(stored[:]),
		base64.StdEncoding.EncodeToString(serverKey)), nil
}

func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}
