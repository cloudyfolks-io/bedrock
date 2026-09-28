package secret

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

const base62Alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

const apiTokenPrefix = "brk_"

const apiTokenRandomLen = 40

func SHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func Equal(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func FromAlphabet(random io.Reader, alphabet string, n int) (string, error) {
	if len(alphabet) == 0 || len(alphabet) > 256 {
		return "", errors.New("secret: alphabet must hold 1 to 256 characters")
	}
	limit := 256 - (256 % len(alphabet))
	out := make([]byte, 0, n)
	buf := make([]byte, 1)
	for len(out) < n {
		if _, err := io.ReadFull(random, buf); err != nil {
			return "", err
		}
		if int(buf[0]) >= limit {
			continue
		}
		out = append(out, alphabet[int(buf[0])%len(alphabet)])
	}
	return string(out), nil
}

func Base62(random io.Reader, n int) (string, error) {
	return FromAlphabet(random, base62Alphabet, n)
}

func NewAPIToken(random io.Reader) (string, error) {
	value, err := Base62(random, apiTokenRandomLen)
	if err != nil {
		return "", err
	}
	return apiTokenPrefix + value, nil
}

func IsAPIToken(value string) bool {
	if len(value) != len(apiTokenPrefix)+apiTokenRandomLen || !strings.HasPrefix(value, apiTokenPrefix) {
		return false
	}
	for _, r := range value[len(apiTokenPrefix):] {
		if !strings.ContainsRune(base62Alphabet, r) {
			return false
		}
	}
	return true
}
