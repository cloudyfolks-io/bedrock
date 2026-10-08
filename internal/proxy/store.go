package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	StoreNamespace = "bedrock-proxy"

	sessionPrefix  = "ps-"
	loginPrefix    = "pl-"
	sessionKey     = "record"
	loginKey       = "login"
	kindSession    = "ProxySession"
	kindLogin      = "ProxyLogin"
	labelNameChars = 32
)

var (
	ErrNotFound = errors.New("proxy record not found")
	ErrClaimed  = errors.New("proxy record is claimed")
)

type Claim struct {
	Holder string    `json:"holder"`
	Until  time.Time `json:"until"`
}

type Record struct {
	Subject      string    `json:"subject"`
	DisplayName  string    `json:"displayName"`
	Email        string    `json:"email"`
	Groups       []string  `json:"groups"`
	SessionName  string    `json:"sessionName"`
	Access       string    `json:"access"`
	AccessExpiry time.Time `json:"accessExpiry"`
	Refresh      string    `json:"refresh"`
	CSRF         string    `json:"csrf"`
	CreatedAt    time.Time `json:"createdAt"`
	ExpiresAt    time.Time `json:"expiresAt"`
	Claim        *Claim    `json:"claim,omitempty"`
}

type LoginState struct {
	Verifier  string    `json:"verifier"`
	Nonce     string    `json:"nonce"`
	ReturnTo  string    `json:"returnTo"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type Store struct {
	client client.Client
	clock  func() time.Time
}

func NewStore(c client.Client, clock func() time.Time) *Store {
	return &Store{client: c, clock: clock}
}

func (s *Store) CreateSession(ctx context.Context, cookieHash string, rec Record) error {
	secret, err := recordSecret(sessionPrefix, kindSession, cookieHash, sessionKey, rec)
	if err != nil {
		return err
	}
	return s.client.Create(ctx, secret, client.FieldOwner(v1alpha1.ProxyFieldManager))
}

func (s *Store) Session(ctx context.Context, cookieHash string) (Record, error) {
	_, rec, err := s.readSession(ctx, cookieHash)
	return rec, err
}

func (s *Store) ClaimRefresh(ctx context.Context, cookieHash, holder string, until time.Time) (Record, error) {
	secret, rec, err := s.readSession(ctx, cookieHash)
	if err != nil {
		return Record{}, err
	}
	if rec.Claim != nil && rec.Claim.Until.After(s.clock()) {
		return Record{}, ErrClaimed
	}
	claimed := withClaim(rec, &Claim{Holder: holder, Until: until})
	if err := s.writeSession(ctx, secret, claimed); err != nil {
		if apierrors.IsConflict(err) {
			return Record{}, ErrClaimed
		}
		return Record{}, err
	}
	return claimed, nil
}

func (s *Store) CompleteRefresh(ctx context.Context, cookieHash string, rec Record) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, _, err := s.readSession(ctx, cookieHash)
		if err != nil {
			return err
		}
		return s.writeSession(ctx, secret, withClaim(rec, nil))
	})
}

func (s *Store) ReleaseClaim(ctx context.Context, cookieHash string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		secret, rec, err := s.readSession(ctx, cookieHash)
		if err != nil {
			return err
		}
		return s.writeSession(ctx, secret, withClaim(rec, nil))
	})
}

func (s *Store) DeleteSession(ctx context.Context, cookieHash string) error {
	return ignoreMissing(s.client.Delete(ctx, &corev1.Secret{ObjectMeta: objectMeta(sessionPrefix, cookieHash)}))
}

func (s *Store) CreateLogin(ctx context.Context, stateHash string, ls LoginState) error {
	secret, err := recordSecret(loginPrefix, kindLogin, stateHash, loginKey, ls)
	if err != nil {
		return err
	}
	return s.client.Create(ctx, secret, client.FieldOwner(v1alpha1.ProxyFieldManager))
}

func (s *Store) TakeLogin(ctx context.Context, stateHash string) (LoginState, error) {
	var secret corev1.Secret
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: StoreNamespace, Name: loginPrefix + stateHash}, &secret); err != nil {
		return LoginState{}, notFound(err)
	}
	uid := secret.UID
	if err := s.client.Delete(ctx, &secret, client.Preconditions{UID: &uid}); err != nil {
		if apierrors.IsNotFound(err) || apierrors.IsConflict(err) {
			return LoginState{}, ErrNotFound
		}
		return LoginState{}, err
	}
	var ls LoginState
	if err := json.Unmarshal(secret.Data[loginKey], &ls); err != nil {
		return LoginState{}, ErrNotFound
	}
	if !ls.ExpiresAt.After(s.clock()) {
		return LoginState{}, ErrNotFound
	}
	return ls, nil
}

func (s *Store) Sweep(ctx context.Context) (int, error) {
	var secrets corev1.SecretList
	if err := s.client.List(ctx, &secrets, client.InNamespace(StoreNamespace), client.MatchingLabelsSelector{Selector: storeKinds()}); err != nil {
		return 0, err
	}
	now := s.clock()
	deleted := 0
	for i := range secrets.Items {
		if !expiredAt(secrets.Items[i], now) {
			continue
		}
		if err := ignoreMissing(s.client.Delete(ctx, &secrets.Items[i])); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, nil
}

func (s *Store) readSession(ctx context.Context, cookieHash string) (*corev1.Secret, Record, error) {
	var secret corev1.Secret
	if err := s.client.Get(ctx, client.ObjectKey{Namespace: StoreNamespace, Name: sessionPrefix + cookieHash}, &secret); err != nil {
		return nil, Record{}, notFound(err)
	}
	var rec Record
	if err := json.Unmarshal(secret.Data[sessionKey], &rec); err != nil {
		return nil, Record{}, fmt.Errorf("decode session record: %w", err)
	}
	return &secret, rec, nil
}

func (s *Store) writeSession(ctx context.Context, secret *corev1.Secret, rec Record) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	updated := secret.DeepCopy()
	updated.Data = map[string][]byte{sessionKey: data}
	return s.client.Update(ctx, updated, client.FieldOwner(v1alpha1.ProxyFieldManager))
}

func withClaim(rec Record, claim *Claim) Record {
	rec.Claim = claim
	return rec
}

func recordSecret(prefix, kind, hash, key string, value any) (*corev1.Secret, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	meta := objectMeta(prefix, hash)
	meta.Labels = map[string]string{
		v1alpha1.LabelKind: kind,
		v1alpha1.LabelName: hash[:min(labelNameChars, len(hash))],
	}
	return &corev1.Secret{ObjectMeta: meta, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{key: data}}, nil
}

func objectMeta(prefix, hash string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: StoreNamespace, Name: prefix + hash}
}

func storeKinds() labels.Selector {
	requirement, _ := labels.NewRequirement(v1alpha1.LabelKind, selection.In, []string{kindSession, kindLogin})
	return labels.NewSelector().Add(*requirement)
}

func expiredAt(secret corev1.Secret, now time.Time) bool {
	var expiry struct {
		ExpiresAt time.Time `json:"expiresAt"`
	}
	for _, key := range []string{sessionKey, loginKey} {
		if raw, found := secret.Data[key]; found {
			if err := json.Unmarshal(raw, &expiry); err != nil {
				return true
			}
			return !expiry.ExpiresAt.After(now)
		}
	}
	return true
}

func notFound(err error) error {
	if apierrors.IsNotFound(err) {
		return ErrNotFound
	}
	return err
}

func ignoreMissing(err error) error {
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
