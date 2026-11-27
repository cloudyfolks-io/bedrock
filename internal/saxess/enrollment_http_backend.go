package saxess

import (
	"context"
	"crypto/rand"
	"errors"
	"math/big"
	"strconv"
)

var _ RegistrationBackend = (*Backend)(nil)

func (b *Backend) RegisterPendingDevice(ctx context.Context, input PendingDeviceRegistration) (RegisteredDevice, error) {
	// Use positive IDs that the app can round-trip as JSON numbers.
	number, err := rand.Int(rand.Reader, big.NewInt((1<<53)-1))
	if err != nil {
		return RegisteredDevice{}, err
	}
	id := number.Int64() + 1
	registered, err := b.RegisterDevice(ctx, DeviceRegistration{
		InviteToken: input.EnrollmentCredential, InvitePayload: input.InviteToken, DeviceID: strconv.FormatInt(id, 10),
		DeviceType: input.DeviceType, Fingerprint: input.Fingerprint, PublicKey: input.PublicKey, Signature: input.SignedMsg,
	})
	if errors.Is(err, ErrEnrollmentRejected) {
		return RegisteredDevice{}, ErrRegistrationRejected
	}
	if err != nil {
		return RegisteredDevice{}, err
	}
	return RegisteredDevice{
		ID: id, UserID: registered.UserID, DeviceType: input.DeviceType, PublicKey: input.PublicKey,
		Status: "pending", Fingerprint: input.Fingerprint, RegisteredAt: registered.RegisteredAt,
		deviceToken: registered.Token,
	}, nil
}
