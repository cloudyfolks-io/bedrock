package login

import (
	"io"

	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
)

const csrfLength = 43

func NewCSRF(random io.Reader) (string, string, error) {
	value, err := secret.Base62(random, csrfLength)
	if err != nil {
		return "", "", err
	}
	return value, secret.SHA256Hex(value), nil
}

func CheckCSRF(hash, header string) bool {
	return hash != "" && header != "" && secret.Equal(hash, secret.SHA256Hex(header))
}
