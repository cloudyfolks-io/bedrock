package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/zitadel/oidc/v3/pkg/op"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/methods"
	"github.com/cloudyfolks-io/bedrock/internal/authn/secret"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	UserCodeAlphabet   = "ABCDEFGHJKLMNPQRSTVWXZ"
	deviceLifetime     = 10 * time.Minute
	devicePollInterval = 5 * time.Second
	deviceFormPath     = "/login/device"
	userCodeLength     = 8
	userCodeGroup      = 4
	fieldUserCodeHash  = "spec.userCodeHash"
)

var errDeviceDecided = errors.New("device request is already decided")

func DeviceConfig() op.DeviceAuthorizationConfig {
	return op.DeviceAuthorizationConfig{
		Lifetime:     deviceLifetime,
		PollInterval: devicePollInterval,
		UserFormPath: deviceFormPath,
		UserCode:     op.UserCodeConfig{CharSet: UserCodeAlphabet, CharAmount: userCodeLength, DashInterval: userCodeGroup},
	}
}

func NormalizeUserCode(input string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToUpper(r)
	}, input)
}

func (s *Store) StoreDeviceAuthorization(ctx context.Context, clientID, deviceCode, userCode string, expires time.Time, scopes []string) error {
	if !s.created.device.Allow(liveKey, s.clock()) {
		return temporarilyUnavailable()
	}
	hash := secret.SHA256Hex(NormalizeUserCode(userCode))
	live, err := s.liveDevices(ctx, hash)
	if err != nil {
		return err
	}
	if len(live) > 0 {
		return op.ErrDuplicateUserCode
	}
	name := secret.SHA256Hex(deviceCode)
	request := v1alpha1.DeviceRequest{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: release.SystemNamespace, Labels: objectLabels("DeviceRequest", name)},
		Spec: v1alpha1.DeviceRequestSpec{
			ClientID:     clientID,
			Scopes:       slices.Clone(scopes),
			Audience:     []string{audienceBedrock, clientID},
			UserCodeHash: hash,
			ExpiresAt:    metav1.NewTime(expires),
		},
	}
	if err := s.client.Create(ctx, &request, authnOwner()); err != nil {
		return err
	}
	request.Status.State = v1alpha1.DeviceStatePending
	return s.client.Status().Update(ctx, &request, authnOwner())
}

func (s *Store) GetDeviceAuthorizatonState(ctx context.Context, clientID, deviceCode string) (*op.DeviceAuthorizationState, error) {
	var request v1alpha1.DeviceRequest
	if err := s.reader.Get(ctx, objectKey(secret.SHA256Hex(deviceCode)), &request); err != nil {
		return nil, err
	}
	if request.Spec.ClientID != clientID {
		return nil, notFoundError{kind: "DeviceRequest"}
	}
	now := s.clock()
	if due(request.Spec.ExpiresAt, now) {
		return waitingState(request), nil
	}
	switch request.Status.State {
	case v1alpha1.DeviceStateApproved:
		if err := s.client.Delete(ctx, &request, client.Preconditions{UID: &request.UID, ResourceVersion: &request.ResourceVersion}); err != nil {
			return nil, err
		}
		return deviceState(request), nil
	case v1alpha1.DeviceStateDenied:
		return deviceState(request), nil
	}
	polled := request.DeepCopy()
	polled.Status.LastPoll = &metav1.Time{Time: now}
	if err := s.client.Status().Update(ctx, polled, authnOwner()); err != nil && !apierrors.IsConflict(err) {
		return nil, err
	}
	return waitingState(request), nil
}

func waitingState(request v1alpha1.DeviceRequest) *op.DeviceAuthorizationState {
	return &op.DeviceAuthorizationState{
		ClientID: request.Spec.ClientID,
		Audience: slices.Clone(request.Spec.Audience),
		Scopes:   slices.Clone(request.Spec.Scopes),
		Expires:  request.Spec.ExpiresAt.Time,
	}
}

func deviceState(request v1alpha1.DeviceRequest) *op.DeviceAuthorizationState {
	state := waitingState(request)
	state.Done = request.Status.State == v1alpha1.DeviceStateApproved
	state.Denied = request.Status.State == v1alpha1.DeviceStateDenied
	state.Subject = request.Status.Subject
	state.AMR = slices.Clone(request.Status.AMR)
	if request.Status.AuthTime != nil {
		state.AuthTime = request.Status.AuthTime.Time
	}
	return state
}

func (s *Store) liveDevices(ctx context.Context, userCodeHash string) ([]v1alpha1.DeviceRequest, error) {
	var list v1alpha1.DeviceRequestList
	if err := s.reader.List(ctx, &list, client.InNamespace(release.SystemNamespace), client.MatchingFields{fieldUserCodeHash: userCodeHash}); err != nil {
		return nil, err
	}
	now := s.clock()
	return slices.DeleteFunc(list.Items, func(request v1alpha1.DeviceRequest) bool {
		return due(request.Spec.ExpiresAt, now)
	}), nil
}

func (s *Store) DeviceRequestByUserCode(ctx context.Context, userCode string) (v1alpha1.DeviceRequest, error) {
	live, err := s.liveDevices(ctx, secret.SHA256Hex(NormalizeUserCode(userCode)))
	if err != nil {
		return v1alpha1.DeviceRequest{}, err
	}
	pending := slices.DeleteFunc(live, func(request v1alpha1.DeviceRequest) bool {
		return request.Status.State != v1alpha1.DeviceStatePending
	})
	if len(pending) != 1 {
		return v1alpha1.DeviceRequest{}, notFoundError{kind: "DeviceRequest"}
	}
	return pending[0], nil
}

func (s *Store) ApproveDevice(ctx context.Context, name string, subject methods.Subject) error {
	return s.decideDevice(ctx, name, func(status v1alpha1.DeviceRequestStatus, now time.Time) v1alpha1.DeviceRequestStatus {
		status.State = v1alpha1.DeviceStateApproved
		status.Subject = subject.User.Name
		status.AMR = slices.Clone(subject.AMR)
		status.AuthTime = &metav1.Time{Time: now}
		return status
	})
}

func (s *Store) DenyDevice(ctx context.Context, name string) error {
	return s.decideDevice(ctx, name, func(status v1alpha1.DeviceRequestStatus, _ time.Time) v1alpha1.DeviceRequestStatus {
		status.State = v1alpha1.DeviceStateDenied
		return status
	})
}

func (s *Store) decideDevice(ctx context.Context, name string, decide func(v1alpha1.DeviceRequestStatus, time.Time) v1alpha1.DeviceRequestStatus) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var request v1alpha1.DeviceRequest
		if err := s.reader.Get(ctx, objectKey(name), &request); err != nil {
			return err
		}
		now := s.clock()
		if due(request.Spec.ExpiresAt, now) {
			return notFoundError{kind: "DeviceRequest"}
		}
		if request.Status.State != v1alpha1.DeviceStatePending {
			return errDeviceDecided
		}
		decided := request.DeepCopy()
		decided.Status = decide(*request.Status.DeepCopy(), now)
		return s.client.Status().Update(ctx, decided, authnOwner())
	})
}
