package policy

import (
	"context"
	"errors"
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

func ParseSettings(values map[string]string) (Settings, error) {
	host := settingValue(values, "platform.host")
	if host == "" {
		return Settings{}, errors.New("platform.host is not set")
	}
	requireSecondFactor, err := strconv.ParseBool(settingValue(values, "authn.require-second-factor"))
	if err != nil {
		return Settings{}, fmt.Errorf("authn.require-second-factor: %w", err)
	}
	sessionTTL, err := time.ParseDuration(settingValue(values, "authn.session-ttl"))
	if err != nil {
		return Settings{}, fmt.Errorf("authn.session-ttl: %w", err)
	}
	refreshTTL, err := time.ParseDuration(settingValue(values, "authn.refresh-ttl"))
	if err != nil {
		return Settings{}, fmt.Errorf("authn.refresh-ttl: %w", err)
	}
	lockoutThreshold, err := strconv.Atoi(settingValue(values, "authn.lockout-threshold"))
	if err != nil {
		return Settings{}, fmt.Errorf("authn.lockout-threshold: %w", err)
	}
	return Settings{
		RequireSecondFactor: requireSecondFactor,
		SessionTTL:          sessionTTL,
		RefreshTTL:          refreshTTL,
		LockoutThreshold:    lockoutThreshold,
		Host:                host,
		TLSMode:             settingValue(values, "platform.tls-mode"),
	}, nil
}

func settingValue(values map[string]string, key string) string {
	if value := values[key]; value != "" {
		return value
	}
	def, _ := settings.Lookup(key)
	return def.Default
}

func ReadSettings(ctx context.Context, c client.Reader) (Settings, error) {
	var list v1alpha1.SettingList
	if err := c.List(ctx, &list); err != nil {
		return Settings{}, err
	}
	values := make(map[string]string, len(list.Items))
	for _, setting := range list.Items {
		values[setting.Name] = setting.Spec.Value
	}
	return ParseSettings(values)
}

func Issuer(s Settings) string {
	return "https://sso." + s.Host
}
