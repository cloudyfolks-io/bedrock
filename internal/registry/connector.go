package registry

import (
	"context"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/addons"
	"github.com/cloudyfolks-io/bedrock/internal/registry/harbor"
)

const (
	adminUsername    = "admin"
	adminPasswordKey = "HARBOR_ADMIN_PASSWORD"
	HarborURL        = "http://harbor-core." + Namespace + ".svc"
)

type Connector struct {
	Client  client.Client
	BaseURL string
	HTTP    *http.Client
}

func (c Connector) baseURL() string {
	if c.BaseURL != "" {
		return c.BaseURL
	}
	return HarborURL
}

func (c Connector) WithPassword(password string) *harbor.Client {
	return &harbor.Client{BaseURL: c.baseURL(), Username: adminUsername, Password: password, HTTP: c.HTTP}
}

func (c Connector) AdminPassword(ctx context.Context) (string, bool, error) {
	var secret corev1.Secret
	err := c.Client.Get(ctx, client.ObjectKey{Namespace: Namespace, Name: adminSecret}, &secret)
	if apierrors.IsNotFound(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	password := string(secret.Data[adminPasswordKey])
	return password, password != "", nil
}

func (c Connector) addonEnabled(ctx context.Context) (bool, error) {
	var addon v1alpha1.Addon
	err := c.Client.Get(ctx, client.ObjectKey{Name: addons.Registry}, &addon)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil && addon.Spec.Enabled, err
}

func (c Connector) Harbor(ctx context.Context) (*harbor.Client, string, error) {
	enabled, err := c.addonEnabled(ctx)
	if err != nil {
		return nil, "", err
	}
	if !enabled {
		return nil, "the registry addon is off", nil
	}
	password, found, err := c.AdminPassword(ctx)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, "the Secret harbor-admin in " + Namespace + " has no admin password", nil
	}
	return c.WithPassword(password), "", nil
}
