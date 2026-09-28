package secret

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/argon2"
)

type Params struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
	SaltLen   uint32
	KeyLen    uint32
}

var DefaultParams = Params{Time: 3, MemoryKiB: 64 * 1024, Threads: 2, SaltLen: 16, KeyLen: 32}

const (
	maxDecodedMemoryKiB = 256 * 1024
	maxDecodedTime      = 10
	minDecodedSaltLen   = 8
	maxDecodedSaltLen   = 64
	minDecodedKeyLen    = 8
	maxDecodedKeyLen    = 64
)

func Hash(random io.Reader, params Params, plain string) (string, error) {
	salt := make([]byte, params.SaltLen)
	if _, err := io.ReadFull(random, salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(plain), salt, params.Time, params.MemoryKiB, params.Threads, params.KeyLen)
	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		params.MemoryKiB, params.Time, params.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func Verify(encoded, plain string) (bool, error) {
	params, salt, key, err := decodePHC(encoded)
	if err != nil {
		return false, err
	}
	candidate := argon2.IDKey([]byte(plain), salt, params.Time, params.MemoryKiB, params.Threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(candidate, key) == 1, nil
}

func decodePHC(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return Params{}, nil, nil, errors.New("secret: malformed argon2id hash")
	}
	var memory, time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return Params{}, nil, nil, errors.New("secret: malformed argon2id parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, errors.New("secret: malformed argon2id salt")
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return Params{}, nil, nil, errors.New("secret: malformed argon2id key")
	}
	if memory > maxDecodedMemoryKiB || time > maxDecodedTime || threads == 0 ||
		len(salt) < minDecodedSaltLen || len(salt) > maxDecodedSaltLen ||
		len(key) < minDecodedKeyLen || len(key) > maxDecodedKeyLen {
		return Params{}, nil, nil, errors.New("secret: argon2id parameters out of bounds")
	}
	return Params{Time: time, MemoryKiB: memory, Threads: threads, SaltLen: uint32(len(salt)), KeyLen: uint32(len(key))}, salt, key, nil
}
