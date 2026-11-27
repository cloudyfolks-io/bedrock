package saxess

import (
	"context"
	"net/mail"
	"strings"
	"time"
)

type EnrollmentInvite struct {
	Token     string
	Payload   string
	ExpiresAt time.Time
}

type DeviceRegistration struct {
	InviteToken   string
	InvitePayload string
	DeviceID      string
	DeviceType    string
	Fingerprint   string
	PublicKey     string
	Signature     string
}

type RegisteredEnrollment struct {
	Token        string
	RegisteredAt time.Time
	UserID       int64
}

// CreateInvite is for trusted local administration. Deliver the token only to
// the named operator. The token and card signing payload are independent.
func (b *Backend) CreateInvite(ctx context.Context, operatorID, email string, ttl time.Duration) (EnrollmentInvite, error) {
	address, addressErr := mail.ParseAddress(email)
	if addressErr != nil || address.Address != email || !validField(email, 254) ||
		!validField(operatorID, 128) || ttl <= 0 || ttl > 15*time.Minute {
		return EnrollmentInvite{}, ErrEnrollmentRejected
	}
	token, err := credential()
	if err != nil {
		return EnrollmentInvite{}, err
	}
	payload, err := credential()
	if err != nil {
		return EnrollmentInvite{}, err
	}
	digest := credentialDigest(token)
	invite := EnrollmentInvite{Token: token, Payload: payload}
	err = b.update(ctx, func(s *State, now time.Time) error {
		op, exists := s.Operators[operatorID]
		if exists && (!op.Active || op.Email != email) {
			return ErrEnrollmentRejected
		}
		var maxUserID int64
		for id, other := range s.Operators {
			if id != operatorID && other.Active && strings.EqualFold(other.Email, email) {
				return ErrEnrollmentRejected
			}
			maxUserID = max(maxUserID, other.UserID)
		}
		if op.UserID == 0 {
			if maxUserID >= (1<<53)-1 {
				return ErrEnrollmentRejected
			}
			op.UserID = maxUserID + 1
		}
		s.prune(now)
		if _, exists := s.Invites[digest]; exists || len(s.Invites) >= 128 {
			return ErrEnrollmentRejected
		}
		if !exists && len(s.Operators) >= 128 {
			return ErrEnrollmentRejected
		}
		if s.Operators == nil {
			s.Operators = make(map[string]Operator)
		}
		if s.Invites == nil {
			s.Invites = make(map[string]StoredInvite)
		}
		op.Email, op.Active = email, true
		s.Operators[operatorID] = op
		invite.ExpiresAt = now.Add(ttl)
		s.Invites[digest] = StoredInvite{OperatorID: operatorID, Payload: payload, ExpiresAt: invite.ExpiresAt}
		return nil
	})
	if err != nil {
		return EnrollmentInvite{}, err
	}
	if now := b.now(); now.IsZero() || !now.Before(invite.ExpiresAt) {
		return EnrollmentInvite{}, ErrEnrollmentRejected
	}
	return invite, nil
}

// RegisterDevice redeems a signed invite once. The returned mobile bearer
// credential cannot authenticate unless trusted administration approves the
// exact pending device and key. The credential is valid until revoked.
func (b *Backend) RegisterDevice(ctx context.Context, r DeviceRegistration) (RegisteredEnrollment, error) {
	if len(r.InviteToken) != 64 || len(r.InvitePayload) != 64 ||
		!validField(r.DeviceID, 128) || !validField(r.Fingerprint, 256) ||
		(r.DeviceType != "android" && r.DeviceType != "ios") ||
		!validField(r.PublicKey, 128) || !validField(r.Signature, 512) {
		return RegisteredEnrollment{}, ErrEnrollmentRejected
	}
	token, err := credential()
	if err != nil {
		return RegisteredEnrollment{}, err
	}
	result := RegisteredEnrollment{Token: token}
	tokenDigest, inviteDigest := credentialDigest(token), credentialDigest(r.InviteToken)
	err = b.update(ctx, func(s *State, now time.Time) error {
		invite, ok := s.Invites[inviteDigest]
		operator := s.Operators[invite.OperatorID]
		if !ok || invite.Payload != r.InvitePayload || !operator.Active || operator.UserID <= 0 || operator.Email == "" || !now.Before(invite.ExpiresAt) {
			return ErrEnrollmentRejected
		}
		if _, exists := s.Devices[r.DeviceID]; exists {
			return ErrRegistrationConflict
		}
		for _, other := range s.Devices {
			if !other.Revoked && other.Fingerprint == r.Fingerprint {
				return ErrRegistrationConflict
			}
		}
		if _, exists := s.Tokens[tokenDigest]; exists {
			return ErrEnrollmentRejected
		}
		if len(s.Devices) >= 256 {
			return ErrEnrollmentRejected
		}
		if err := b.verify(r.PublicKey, invite.Payload, r.Signature); err != nil {
			return ErrEnrollmentRejected
		}
		now = b.now().UTC()
		if now.IsZero() || !now.Before(invite.ExpiresAt) {
			return ErrEnrollmentRejected
		}
		s.prune(now)
		if s.Devices == nil {
			s.Devices = make(map[string]Device)
		}
		if s.Tokens == nil {
			s.Tokens = make(map[string]string)
		}
		delete(s.Invites, inviteDigest)
		result.RegisteredAt, result.UserID = now, operator.UserID
		s.Devices[r.DeviceID] = Device{
			OperatorID: invite.OperatorID, PublicKey: r.PublicKey, TokenDigest: tokenDigest,
			DeviceType: r.DeviceType, Fingerprint: r.Fingerprint, RegisteredAt: now,
		}
		s.Tokens[tokenDigest] = r.DeviceID
		return nil
	})
	if err != nil {
		return RegisteredEnrollment{}, err
	}
	return result, nil
}

// ApproveDevice enables a pending credential after the administrator verifies
// the operator, device, and card public key out of band.
func (b *Backend) ApproveDevice(ctx context.Context, operatorID, deviceID, publicKey string) error {
	return b.update(ctx, func(s *State, now time.Time) error {
		d, ok := s.Devices[deviceID]
		op := s.Operators[operatorID]
		if operatorID == "" || deviceID == "" || publicKey == "" || !ok || !op.Active || op.Email == "" ||
			d.OperatorID != operatorID || d.PublicKey != publicKey || d.Approved || d.Revoked ||
			d.TokenDigest == "" || s.Tokens[d.TokenDigest] != deviceID {
			return ErrEnrollmentRejected
		}
		s.prune(now)
		d.Approved = true
		s.Devices[deviceID] = d
		return nil
	})
}

// RevokeDevice permanently invalidates the device credential and any unused
// browser approvals made with it. Retain ownership to prevent reassignment.
func (b *Backend) RevokeDevice(ctx context.Context, operatorID, deviceID string) error {
	return b.update(ctx, func(s *State, now time.Time) error {
		d, ok := s.Devices[deviceID]
		if !ok || operatorID == "" || d.OperatorID != operatorID {
			return ErrEnrollmentRejected
		}
		s.prune(now)
		delete(s.Tokens, d.TokenDigest)
		d.Revoked, d.TokenDigest = true, ""
		s.Devices[deviceID] = d
		return nil
	})
}

// DisableOperator permanently disables the local enrollment and its approvals.
func (b *Backend) DisableOperator(ctx context.Context, operatorID string) error {
	return b.update(ctx, func(s *State, now time.Time) error {
		op, ok := s.Operators[operatorID]
		if !ok || operatorID == "" {
			return ErrEnrollmentRejected
		}
		s.prune(now)
		op.Active = false
		s.Operators[operatorID] = op
		return nil
	})
}
