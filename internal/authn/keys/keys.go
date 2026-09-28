package keys

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"io"
	"slices"
	"time"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
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

func Generate(random io.Reader, notBefore time.Time) (Key, error) {
	private, err := ecdsa.GenerateKey(elliptic.P256(), random)
	if err != nil {
		return Key{}, err
	}
	start := notBefore.UTC().Truncate(time.Second)
	return Key{ID: v1alpha1.SigningKeyName(start), Private: private, NotBefore: start, RetireAfter: start.Add(Lifetime)}, nil
}

func Signing(keys []Key, now time.Time) (Key, bool) {
	active := slices.DeleteFunc(slices.Clone(keys), func(key Key) bool {
		return now.Before(key.NotBefore) || !now.Before(key.RetireAfter)
	})
	if len(active) == 0 {
		return Key{}, false
	}
	return slices.MaxFunc(active, byNotBefore), true
}

func Published(keys []Key, now time.Time) []Key {
	kept := slices.DeleteFunc(slices.Clone(keys), func(key Key) bool {
		return expired(key, now)
	})
	slices.SortFunc(kept, byNotBefore)
	return kept
}

func NextNotBefore(keys []Key, now time.Time) (time.Time, bool) {
	if len(keys) == 0 {
		return now, true
	}
	newest := slices.MaxFunc(keys, byNotBefore)
	if now.Before(newest.RetireAfter.Add(-PublishAhead)) {
		return time.Time{}, false
	}
	if newest.RetireAfter.Before(now) {
		return now, true
	}
	return newest.RetireAfter, true
}

func Expired(keys []Key, now time.Time) []Key {
	return slices.DeleteFunc(slices.Clone(keys), func(key Key) bool {
		return !expired(key, now)
	})
}

func expired(key Key, now time.Time) bool {
	return !now.Before(key.RetireAfter.Add(KeepAfter))
}

func byNotBefore(a, b Key) int {
	return a.NotBefore.Compare(b.NotBefore)
}
