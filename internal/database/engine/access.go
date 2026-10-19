package engine

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
)

const alphanumeric = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

var (
	ErrExists      = errors.New("it already exists")
	ErrNotFound    = errors.New("it does not exist")
	ErrUnavailable = errors.New("the database refused the command")
)

type Grant struct {
	Database string
	Level    string
}

type UserSpec struct {
	Name      string
	Password  string
	Grants    []Grant
	KeyPrefix string
}

type UserInfo struct {
	Name      string
	Grants    []Grant
	KeyPrefix string
}

type Target struct {
	Selector  string
	Container string
	Command   []string
}

type Stats struct {
	DiskUsedBytes int64
	Connections   int
	LagSeconds    *float64
}

type Runner interface {
	Run(ctx context.Context, stdin string) (string, error)
}

func IsExists(err error) bool { return errors.Is(err, ErrExists) }

func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

func RandomString(length int) (string, error) {
	out := make([]byte, length)
	limit := big.NewInt(int64(len(alphanumeric)))
	for i := range out {
		index, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		out[i] = alphanumeric[index.Int64()]
	}
	return string(out), nil
}

func RandomPassword() (string, error) { return RandomString(32) }

func Lines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
