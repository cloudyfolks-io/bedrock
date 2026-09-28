package settings

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type Definition struct {
	Key         string
	Default     string
	Description string
	Validate    func(string) error
}

func free(string) error { return nil }

func oneOf(values ...string) func(string) error {
	return func(v string) error {
		if v == "" || slices.Contains(values, v) {
			return nil
		}
		return fmt.Errorf("value %q not in %v", v, values)
	}
}

func intMin(minimum int) func(string) error {
	return func(v string) error {
		if v == "" {
			return nil
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			return err
		}
		if n < minimum {
			return fmt.Errorf("value %d below minimum %d", n, minimum)
		}
		return nil
	}
}

func boolean(v string) error {
	if v == "" {
		return nil
	}
	_, err := strconv.ParseBool(v)
	return err
}

func float(v string) error {
	if v == "" {
		return nil
	}
	_, err := strconv.ParseFloat(v, 64)
	return err
}

func duration(min time.Duration) func(string) error {
	return func(v string) error {
		if v == "" {
			return nil
		}
		d, err := time.ParseDuration(v)
		if err != nil {
			return err
		}
		if d < min {
			return fmt.Errorf("value %s below minimum %s", d, min)
		}
		return nil
	}
}

func Catalog() []Definition {
	return []Definition{
		{"platform.host", "", "Public host name of the platform. Empty means the VIP under sslip.io.", free},
		{"platform.tls-mode", "SelfSigned", "Certificate source for the platform endpoints.", oneOf("SelfSigned", "LetsEncrypt", "Custom")},
		{"platform.additional-ca", "", "PEM bundle of extra CAs trusted for outbound connections.", free},
		{"platform.custom-tls", "", "Secret name in bedrock-system holding the custom certificate.", free},
		{"letsencrypt.email", "", "Account email for Let's Encrypt.", free},
		{"letsencrypt.solver", "http01", "ACME challenge solver.", oneOf("http01", "dns01")},
		{"storage.replicas", "1", "Ceph pool replica count.", intMin(1)},
		{"storage.network", "", "VLAN id of the dedicated storage network.", free},
		{"migration.network", "", "VLAN id of the dedicated live migration network.", free},
		{"migration.parallel", "2", "Parallel live migrations per cluster.", intMin(1)},
		{"virt.emulation", "false", "Run virtual machines with software emulation on hosts without KVM.", boolean},
		{"overcommit.cpu", "4", "CPU overcommit ratio.", intMin(1)},
		{"overcommit.memory", "1.0", "Memory overcommit ratio.", float},
		{"backup.target", "", "Backup target URL, s3:// or nfs://.", free},
		{"backup.credentials-secret", "", "Secret name in bedrock-system holding backup credentials.", free},
		{"backup.etcd-schedule", "0 2 * * *", "Cron schedule for etcd snapshots.", free},
		{"audit.level", "Metadata", "Audit level for read requests.", oneOf("Metadata", "RequestResponse")},
		{"loki.retention-days", "14", "Log retention in days when Loki is enabled.", intMin(1)},
		{"kata.enabled", "true", "Kata runtime addon.", boolean},
		{"loki.enabled", "false", "Loki and Alloy addon.", boolean},
		{"images.refresh-schedule", "0 3 * * *", "Cron schedule for catalog image refresh.", free},
		{"images.keep", "3", "Catalog image versions to keep.", intMin(1)},
		{"authn.require-second-factor", "false", "Require a second factor for every login.", boolean},
		{"authn.session-ttl", "12h", "Login session cookie lifetime.", duration(5 * time.Minute)},
		{"authn.refresh-ttl", "720h", "Refresh token lifetime.", duration(time.Hour)},
		{"authn.lockout-threshold", "5", "Failed password attempts before lockout.", intMin(1)},
	}
}

func Lookup(key string) (Definition, bool) {
	for _, def := range Catalog() {
		if def.Key == key {
			return def, true
		}
	}
	return Definition{}, false
}

func Seed(ctx context.Context, c client.Client) error {
	for _, def := range Catalog() {
		setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: def.Key, Labels: map[string]string{v1alpha1.LabelKind: "Setting", v1alpha1.LabelName: def.Key}}}
		err := c.Create(ctx, setting)
		if err != nil && !errors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}
