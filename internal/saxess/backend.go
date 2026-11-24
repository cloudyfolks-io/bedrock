package saxess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// StateStore serializes transactions or retries them against fresh state on
// conflicts. Callback errors abort the write. A nil Update means durable commit.
// View provides a read-only snapshot and never commits.
type StateStore interface {
	Update(context.Context, func(*State) error) error
	View(context.Context, func(*State) error) error
}

// State is the durable local Pro snapshot. Map keys are IDs, except Invites and
// Tokens, which are keyed by SHA-256 digests of their respective credentials.
type State struct {
	Operators  map[string]Operator
	Invites    map[string]StoredInvite
	Devices    map[string]Device
	Tokens     map[string]string
	Challenges map[string]StoredChallenge
	Audit      map[string]ApprovalAudit
}

type Operator struct {
	Email  string
	Active bool
	UserID int64
}

type StoredInvite struct {
	OperatorID string
	Payload    string
	ExpiresAt  time.Time
}

type Device struct {
	OperatorID   string
	PublicKey    string
	DeviceType   string
	Fingerprint  string
	RegisteredAt time.Time
	TokenDigest  string
	Approved     bool
	Revoked      bool
}

type StoredChallenge struct {
	Challenge
	CreatedAt time.Time
	DeviceID  string
}

func (c StoredChallenge) active(operatorID, browserTransactionID string, now time.Time) bool {
	return !c.CreatedAt.IsZero() && !now.Before(c.CreatedAt) && c.Challenge.active(operatorID, browserTransactionID, now)
}

type ApprovalAudit struct {
	OperatorID string
	DeviceID   string
	ApprovedAt time.Time
	Proof      string
}

type Backend struct {
	store  StateStore
	verify func(publicKey, payload, signature string) error
	now    func() time.Time
}

var (
	ErrEnrollmentRejected   = errors.New("sAxess enrollment rejected")
	ErrBackendNotConfigured = errors.New("sAxess backend is not configured")
)

func NewBackend(store StateStore, verify func(publicKey, payload, signature string) error) *Backend {
	return &Backend{store: store, verify: verify, now: time.Now}
}

var _ MobileBackend = (*Backend)(nil)

func (b *Backend) update(ctx context.Context, fn func(*State, time.Time) error) error {
	return b.withState(ctx, true, fn)
}

func (b *Backend) withState(ctx context.Context, write bool, fn func(*State, time.Time) error) error {
	if b == nil || b.store == nil || b.verify == nil || b.now == nil {
		return ErrBackendNotConfigured
	}
	run := b.store.View
	if write {
		run = b.store.Update
	}
	return run(ctx, func(s *State) error {
		now := b.now().UTC()
		if s == nil || now.IsZero() {
			return ErrBackendNotConfigured
		}
		return fn(s, now)
	})
}

// prune removes expired transient state on writes. Durable ownership and
// revocation records are retained so identifiers cannot be reassigned.
func (s *State) prune(now time.Time) {
	maps.DeleteFunc(s.Invites, func(_ string, i StoredInvite) bool { return !now.Before(i.ExpiresAt) })
	maps.DeleteFunc(s.Challenges, func(_ string, c StoredChallenge) bool { return !now.Before(c.ExpiresAt) })
	maps.DeleteFunc(s.Audit, func(_ string, e ApprovalAudit) bool {
		return !now.Before(e.ApprovedAt.Add(30 * 24 * time.Hour))
	})
}

// Drop the oldest completed record first. A live approval retains its proof.
func (s *State) reserveAudit() bool {
	if len(s.Audit) < 256 {
		return true
	}
	var oldest string
	for id, record := range s.Audit {
		if c, ok := s.Challenges[id]; ok && c.State != ChallengeConsumed {
			continue
		}
		if oldest == "" || record.ApprovedAt.Before(s.Audit[oldest].ApprovedAt) {
			oldest = id
		}
	}
	if oldest == "" {
		return false
	}
	delete(s.Audit, oldest)
	return true
}

func (s *State) approvedDevice(operatorID, deviceID string) bool {
	op, ok := s.Operators[operatorID]
	d, deviceOK := s.Devices[deviceID]
	return operatorID != "" && deviceID != "" && ok && op.Active && op.Email != "" &&
		deviceOK && d.OperatorID == operatorID && d.Approved && !d.Revoked &&
		d.PublicKey != "" && d.TokenDigest != "" && s.Tokens[d.TokenDigest] == deviceID
}

func (s *State) mobilePrincipal(p MobilePrincipal) bool {
	return s.approvedDevice(p.EmployeeID, p.DeviceID) && s.Operators[p.EmployeeID].Email == p.Email
}

func (b *Backend) Authenticate(ctx context.Context, token string) (MobilePrincipal, error) {
	if len(token) != 64 {
		return MobilePrincipal{}, ErrMobileUnauthorized
	}
	digest := credentialDigest(token)
	var p MobilePrincipal
	err := b.withState(ctx, false, func(s *State, _ time.Time) error {
		deviceID := s.Tokens[digest]
		d := s.Devices[deviceID]
		if d.TokenDigest != digest || !s.approvedDevice(d.OperatorID, deviceID) {
			return ErrMobileUnauthorized
		}
		p = MobilePrincipal{EmployeeID: d.OperatorID, Email: s.Operators[d.OperatorID].Email, DeviceID: deviceID}
		return nil
	})
	if err != nil {
		return MobilePrincipal{}, err
	}
	return p, nil
}

// CreateChallenge starts a trusted browser flow. The transaction ID stays
// server-side. The mobile request ID is the independent random challenge nonce.
func (b *Backend) CreateChallenge(ctx context.Context, operatorID, browserTransactionID string, ttl time.Duration) (Challenge, error) {
	if !validField(operatorID, 128) || !validField(browserTransactionID, 512) || ttl <= 0 || ttl > 5*time.Minute {
		return Challenge{}, ErrInvalidChallenge
	}
	// Generate randomness once so retries are deterministic. Set expiry using
	// the clock of the attempt that commits.
	nonce, err := credential()
	if err != nil {
		return Challenge{}, err
	}
	var c Challenge
	err = b.update(ctx, func(s *State, now time.Time) error {
		if op := s.Operators[operatorID]; !op.Active || op.Email == "" {
			return ErrInvalidChallenge
		}
		s.prune(now)
		for _, existing := range s.Challenges {
			if existing.BrowserTransactionID == browserTransactionID {
				if !existing.active(operatorID, browserTransactionID, now) ||
					(existing.State != ChallengePending && existing.State != ChallengeApproved) {
					return ErrInvalidChallenge
				}
				c = existing.Challenge
				return nil
			}
		}
		c = Challenge{Nonce: nonce, OperatorID: operatorID, BrowserTransactionID: browserTransactionID,
			State: ChallengePending, ExpiresAt: now.Add(ttl)}
		if _, exists := s.Challenges[c.Nonce]; exists || len(s.Challenges) >= 128 {
			return ErrInvalidChallenge
		}
		if s.Challenges == nil {
			s.Challenges = make(map[string]StoredChallenge)
		}
		s.Challenges[c.Nonce] = StoredChallenge{Challenge: c, CreatedAt: now}
		return nil
	})
	if err != nil {
		return Challenge{}, err
	}
	if now := b.now(); now.IsZero() || !now.Before(c.ExpiresAt) {
		return Challenge{}, ErrInvalidChallenge
	}
	return c, nil
}

func (b *Backend) GetRequests(ctx context.Context, p MobilePrincipal) ([]MobileRequest, error) {
	var requests []MobileRequest
	err := b.withState(ctx, false, func(s *State, now time.Time) error {
		if !s.mobilePrincipal(p) {
			return ErrMobileUnauthorized
		}
		requests = nil
		for id, c := range s.Challenges {
			if id != c.Nonce || !c.active(p.EmployeeID, c.BrowserTransactionID, now) {
				continue
			}
			var status string
			switch c.State {
			case ChallengePending:
				status = "pending"
			case ChallengeApproved:
				status = "verified"
			case ChallengeDenied:
				status = "rejected"
			case ChallengeConsumed:
				status = "used"
			default:
				continue
			}
			requests = append(requests, MobileRequest{
				AuthReqID: id,
				Status:    status,
				CreatedAt: c.CreatedAt,
				Notify: MobileRequestNotify{
					Data: MobileRequestData{
						AuthReqID: id, SignPayload: c.Nonce, Type: "DASHBOARD_LOGIN_AUTH_REQUEST",
						AuthorizationDetails: `[{"type":"biometric_auth","purpose":"secure_login","resource":"admin","actions":["authenticate"],"device_binding":"nfc-card"}]`,
						Timestamp:            strconv.FormatInt(c.CreatedAt.Unix(), 10),
					},
					Notification: MobileRequestNotification{Title: "Admin login", Body: "Approve only if you requested this login."},
				},
			})
		}
		slices.SortFunc(requests, func(a, b MobileRequest) int {
			if cmp := a.CreatedAt.Compare(b.CreatedAt); cmp != 0 {
				return cmp
			}
			return strings.Compare(a.AuthReqID, b.AuthReqID)
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return requests, nil
}

func (b *Backend) SendConsent(ctx context.Context, p MobilePrincipal, consent MobileConsent) error {
	var expiry time.Time
	err := b.update(ctx, func(s *State, now time.Time) error {
		if !s.mobilePrincipal(p) || consent.Email != p.Email || consent.DeviceID != p.DeviceID ||
			!validField(consent.SignedMsg, 512) {
			return ErrMobileConsentRejected
		}
		c, ok := s.Challenges[consent.AuthReqID]
		if !ok || c.Nonce != consent.AuthReqID || c.State != ChallengePending ||
			!c.active(p.EmployeeID, c.BrowserTransactionID, now) {
			return ErrMobileConsentRejected
		}
		if err := b.verify(s.Devices[p.DeviceID].PublicKey, c.Nonce, consent.SignedMsg); err != nil {
			return ErrMobileConsentRejected
		}
		// Recheck time after verification, not just when the transaction started.
		now = b.now().UTC()
		if !c.active(p.EmployeeID, c.BrowserTransactionID, now) {
			return ErrMobileConsentRejected
		}
		decided, err := c.Decide(p.EmployeeID, c.BrowserTransactionID, c.Nonce, now, ChallengeApproved)
		if err != nil {
			return ErrMobileConsentRejected
		}
		s.prune(now)
		if !s.reserveAudit() {
			return ErrMobileConsentRejected
		}
		c.Challenge, c.DeviceID = decided, p.DeviceID
		expiry = c.ExpiresAt
		s.Challenges[consent.AuthReqID] = c
		if s.Audit == nil {
			s.Audit = make(map[string]ApprovalAudit)
		}
		s.Audit[consent.AuthReqID] = ApprovalAudit{OperatorID: p.EmployeeID, DeviceID: p.DeviceID, ApprovedAt: now, Proof: consent.SignedMsg}
		return nil
	})
	if err != nil {
		return err
	}
	if now := b.now(); now.IsZero() || !now.Before(expiry) {
		return ErrMobileConsentRejected
	}
	return nil
}

// ConsumeChallenge authorizes the trusted browser transaction once. Mobile
// approval alone never creates or receives a browser session.
func (b *Backend) ConsumeChallenge(ctx context.Context, operatorID, browserTransactionID, authReqID string) (string, error) {
	return b.consumeChallenge(ctx, operatorID, browserTransactionID, authReqID, false)
}

// RedeemBrowserApproval is retryable only for the originating browser
// transaction. Its caller must use an independent single-winner login CAS.
func (b *Backend) RedeemBrowserApproval(ctx context.Context, operatorID, browserTransactionID, authReqID string) (string, error) {
	return b.consumeChallenge(ctx, operatorID, browserTransactionID, authReqID, true)
}

func (b *Backend) consumeChallenge(ctx context.Context, operatorID, browserTransactionID, authReqID string, retryable bool) (string, error) {
	var expiry time.Time
	err := b.update(ctx, func(s *State, now time.Time) error {
		c, ok := s.Challenges[authReqID]
		audit, audited := s.Audit[authReqID]
		if !ok || c.Nonce != authReqID || !audited || audit.Proof == "" ||
			audit.OperatorID != operatorID || audit.DeviceID != c.DeviceID ||
			!s.approvedDevice(operatorID, c.DeviceID) || !c.active(operatorID, browserTransactionID, now) ||
			now.Before(audit.ApprovedAt) {
			return ErrInvalidChallenge
		}
		if retryable && c.State == ChallengeConsumed {
			expiry = c.ExpiresAt
			return nil
		}
		consumed, err := c.Consume(operatorID, browserTransactionID, now)
		if err != nil {
			return err
		}
		s.prune(now)
		c.Challenge = consumed
		expiry = consumed.ExpiresAt
		s.Challenges[authReqID] = c
		return nil
	})
	if err != nil {
		return "", err
	}
	if now := b.now(); now.IsZero() || !now.Before(expiry) {
		return "", ErrInvalidChallenge
	}
	return operatorID, nil
}

func credentialDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func credential() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func validField(value string, max int) bool {
	return value != "" && len(value) <= max && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, unicode.IsControl) < 0
}
