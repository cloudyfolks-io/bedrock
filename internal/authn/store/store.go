package store

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/zitadel/oidc/v3/pkg/op"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/keys"
	"github.com/cloudyfolks-io/bedrock/internal/authn/policy"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	accessTokenLifetime = time.Hour
	idTokenLifetime     = time.Hour
	audienceBedrock     = "bedrock"
	scopeGroups         = "groups"
	maxLabelValue       = 63
)

var (
	_ op.Storage                        = (*Store)(nil)
	_ op.DeviceAuthorizationStorage     = (*Store)(nil)
	_ op.TokenExchangeStorage           = (*Store)(nil)
	_ op.CanSetUserinfoFromRequest      = (*Store)(nil)
	_ op.CanGetPrivateClaimsFromRequest = (*Store)(nil)
)

type Clock func() time.Time

type Config struct {
	Client   client.Client
	Reader   client.Reader
	Random   io.Reader
	Clock    Clock
	Settings func(context.Context) (policy.Settings, error)
	Keys     func(context.Context) ([]keys.Key, error)
}

type Store struct {
	client   client.Client
	reader   client.Reader
	random   io.Reader
	clock    Clock
	settings func(context.Context) (policy.Settings, error)
	keys     func(context.Context) ([]keys.Key, error)
}

func New(cfg Config) *Store {
	return &Store{
		client:   cfg.Client,
		reader:   cfg.Reader,
		random:   cfg.Random,
		clock:    cfg.Clock,
		settings: cfg.Settings,
		keys:     cfg.Keys,
	}
}

type notFoundError struct{ kind string }

func (e notFoundError) Error() string { return e.kind + " not found" }

func (notFoundError) IsNotFound() {}

func isNotFound(err error) bool {
	var missing notFoundError
	return apierrors.IsNotFound(err) || errors.As(err, &missing)
}

func objectKey(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: release.SystemNamespace, Name: name}
}

func objectLabels(kind, name string) map[string]string {
	return map[string]string{v1alpha1.LabelKind: kind, v1alpha1.LabelName: name[:min(len(name), maxLabelValue)]}
}

func due(expiresAt metav1.Time, now time.Time) bool {
	return !now.Before(expiresAt.Time)
}

func authnOwner() client.FieldOwner {
	return client.FieldOwner(v1alpha1.AuthnFieldManager)
}
