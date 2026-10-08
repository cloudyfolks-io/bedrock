package compute

import (
	"crypto/rsa"
	"errors"
	"slices"
	"strings"

	"golang.org/x/crypto/ssh"
)

const (
	maxPublicKeyBytes = 16384
	minRSAKeyBits     = 2048
)

var (
	ErrInvalidPublicKey = errors.New("the public key is not valid")

	allowedKeyTypes = []string{
		ssh.KeyAlgoED25519,
		ssh.KeyAlgoECDSA256,
		ssh.KeyAlgoECDSA384,
		ssh.KeyAlgoECDSA521,
		ssh.KeyAlgoSKED25519,
		ssh.KeyAlgoSKECDSA256,
		ssh.KeyAlgoRSA,
	}
)

func AuthorizedKey(raw string) (string, error) {
	if len(raw) > maxPublicKeyBytes {
		return "", errors.New("the public key is too long")
	}
	line := strings.Trim(strings.TrimSuffix(strings.Trim(raw, " \t"), "\n"), " \t")
	if strings.ContainsAny(line, "\r\n") {
		return "", errors.New("the public key must be one line")
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil {
		return "", ErrInvalidPublicKey
	}
	if len(options) > 0 || len(strings.TrimSpace(string(rest))) > 0 {
		return "", errors.New("the public key must not have options")
	}
	if !slices.Contains(allowedKeyTypes, key.Type()) {
		return "", errors.New("the public key type is not supported")
	}
	if tooSmallRSA(key) {
		return "", errors.New("an RSA public key needs at least 2048 bits")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), nil
}

func tooSmallRSA(key ssh.PublicKey) bool {
	crypto, ok := key.(ssh.CryptoPublicKey)
	if !ok {
		return false
	}
	public, ok := crypto.CryptoPublicKey().(*rsa.PublicKey)
	return ok && public.N.BitLen() < minRSAKeyBits
}
