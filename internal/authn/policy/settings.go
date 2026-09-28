package policy

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

type Settings struct {
	RequireSecondFactor bool
	SessionTTL          time.Duration
	RefreshTTL          time.Duration
	LockoutThreshold    int
	Host                string
	TLSMode             string
}

func ParseSettings(values map[string]string, vip string) (Settings, error) {
	requireSecondFactor, err := strconv.ParseBool(values["authn.require-second-factor"])
	if err != nil {
		return Settings{}, fmt.Errorf("authn.require-second-factor: %w", err)
	}
	sessionTTL, err := time.ParseDuration(values["authn.session-ttl"])
	if err != nil {
		return Settings{}, fmt.Errorf("authn.session-ttl: %w", err)
	}
	refreshTTL, err := time.ParseDuration(values["authn.refresh-ttl"])
	if err != nil {
		return Settings{}, fmt.Errorf("authn.refresh-ttl: %w", err)
	}
	lockoutThreshold, err := strconv.Atoi(values["authn.lockout-threshold"])
	if err != nil {
		return Settings{}, fmt.Errorf("authn.lockout-threshold: %w", err)
	}
	return Settings{
		RequireSecondFactor: requireSecondFactor,
		SessionTTL:          sessionTTL,
		RefreshTTL:          refreshTTL,
		LockoutThreshold:    lockoutThreshold,
		Host:                settings.PlatformHost(vip, values["platform.host"]),
		TLSMode:             values["platform.tls-mode"],
	}, nil
}

func ReadSettings(ctx context.Context, c client.Reader) (Settings, error) {
	var list v1alpha1.SettingList
	if err := c.List(ctx, &list); err != nil {
		return Settings{}, err
	}
	values := map[string]string{}
	for _, def := range settings.Catalog() {
		values[def.Key] = def.Default
	}
	for _, setting := range list.Items {
		if setting.Spec.Value != "" {
			values[setting.Name] = setting.Spec.Value
		}
	}
	var cluster v1alpha1.Cluster
	if err := c.Get(ctx, client.ObjectKey{Name: v1alpha1.ClusterName}, &cluster); err != nil {
		return Settings{}, err
	}
	return ParseSettings(values, cluster.Spec.API.VIP)
}

func Issuer(s Settings) string {
	return "https://sso." + s.Host
}
