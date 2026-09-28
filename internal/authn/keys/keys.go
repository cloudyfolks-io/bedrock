package keys

import (
	"crypto/ecdsa"
	"time"
)

const (
	Lifetime     = 30 * 24 * time.Hour
	PublishAhead = 24 * time.Hour
	KeepAfter    = 30 * 24 * time.Hour
)

type Key struct {
	ID          string
	Private     *ecdsa.PrivateKey
	NotBefore   time.Time
	RetireAfter time.Time
}
