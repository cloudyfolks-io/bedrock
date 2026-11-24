package saxess

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"
)

type ChallengeState string

const (
	ChallengePending  ChallengeState = "pending"
	ChallengeApproved ChallengeState = "approved"
	ChallengeDenied   ChallengeState = "denied"
	ChallengeConsumed ChallengeState = "consumed"
)

var ErrInvalidChallenge = errors.New("invalid sAxess challenge or transition")

// Challenge is a durable snapshot. Treat its fields as read-only outside storage
// decoding. Commit each transition with a CAS against the exact loaded snapshot
// and enforce expiry at commit. Act on the result only after a successful commit.
type Challenge struct {
	Nonce                string
	OperatorID           string
	BrowserTransactionID string
	ExpiresAt            time.Time
	State                ChallengeState
}

func NewChallenge(operatorID, browserTransactionID string, now time.Time, ttl time.Duration) (Challenge, error) {
	if operatorID == "" || browserTransactionID == "" || now.IsZero() || ttl <= 0 {
		return Challenge{}, ErrInvalidChallenge
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Challenge{}, err
	}
	return Challenge{
		Nonce:                hex.EncodeToString(nonce[:]),
		OperatorID:           operatorID,
		BrowserTransactionID: browserTransactionID,
		ExpiresAt:            now.Add(ttl),
		State:                ChallengePending,
	}, nil
}

// Decide records a decision already authenticated and verified by the caller
// for this exact challenge and its bound operator. It verifies no proof itself.
func (c Challenge) Decide(operatorID, browserTransactionID, proofNonce string, now time.Time, decision ChallengeState) (Challenge, error) {
	if !c.active(operatorID, browserTransactionID, now) || proofNonce != c.Nonce || c.State != ChallengePending ||
		(decision != ChallengeApproved && decision != ChallengeDenied) {
		return Challenge{}, ErrInvalidChallenge
	}
	c.State = decision
	return c, nil
}

// Consume finalizes an approval. Denial is terminal.
func (c Challenge) Consume(operatorID, browserTransactionID string, now time.Time) (Challenge, error) {
	if !c.active(operatorID, browserTransactionID, now) || c.State != ChallengeApproved {
		return Challenge{}, ErrInvalidChallenge
	}
	c.State = ChallengeConsumed
	return c, nil
}

func (c Challenge) active(operatorID, browserTransactionID string, now time.Time) bool {
	if operatorID == "" || operatorID != c.OperatorID ||
		browserTransactionID == "" || browserTransactionID != c.BrowserTransactionID ||
		now.IsZero() || c.ExpiresAt.IsZero() || !now.Before(c.ExpiresAt) {
		return false
	}
	nonce, err := hex.DecodeString(c.Nonce)
	return err == nil && len(nonce) == 32
}
