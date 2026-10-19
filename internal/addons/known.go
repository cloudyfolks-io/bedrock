package addons

import (
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type Value struct {
	Key      string
	Default  string
	Validate func(string) error
}

type Definition struct {
	Name           string
	EnabledSetting string
	Values         []Value
	Preflight      func(hosts []v1alpha1.Host) Verdict
	Check          func(Facts) Verdict
	Vars           func(values, settings map[string]string) (map[string]string, error)
	Adjust         func(objects []*unstructured.Unstructured, settings map[string]string) ([]*unstructured.Unstructured, error)
}

func Known() []Definition {
	return []Definition{kataDefinition(), lokiDefinition(), clustersDefinition(), cnpgDefinition(), registryDefinition(), perconaDefinition()}
}

func Lookup(name string) (Definition, bool) {
	for _, def := range Known() {
		if def.Name == name {
			return def, true
		}
	}
	return Definition{}, false
}

func kataDefinition() Definition {
	return Definition{Name: Kata, EnabledSetting: "kata.enabled", Preflight: KataPreflight}
}

func clustersDefinition() Definition {
	return Definition{Name: Clusters, EnabledSetting: "clusters.enabled"}
}

func cnpgDefinition() Definition {
	return Definition{Name: CNPG, EnabledSetting: "cnpg.enabled"}
}

func perconaDefinition() Definition {
	return Definition{Name: PerconaPS, EnabledSetting: "percona-ps.enabled"}
}

func lokiDefinition() Definition {
	return Definition{
		Name:           Loki,
		EnabledSetting: "loki.enabled",
		Values: []Value{
			{Key: "persistence.size", Default: "20Gi", Validate: positiveQuantity},
			{Key: "persistence.storage-class", Default: "block", Validate: dnsSubdomain},
		},
		Vars: lokiVars,
	}
}

func positiveQuantity(value string) error {
	quantity, err := resource.ParseQuantity(value)
	if err != nil {
		return err
	}
	if quantity.Sign() <= 0 {
		return fmt.Errorf("quantity %q must be positive", value)
	}
	return nil
}

func dnsSubdomain(value string) error {
	if problems := validation.IsDNS1123Subdomain(value); len(problems) > 0 {
		return fmt.Errorf("%q is not a DNS-1123 subdomain: %s", value, problems[0])
	}
	return nil
}

func lokiVars(values, settings map[string]string) (map[string]string, error) {
	days, err := strconv.Atoi(settings["loki.retention-days"])
	if err != nil || days < 1 {
		return nil, fmt.Errorf("setting loki.retention-days must be a positive whole number, got %q", settings["loki.retention-days"])
	}
	return map[string]string{
		VarName(Loki, "retention"):                 strconv.Itoa(days*24) + "h",
		VarName(Loki, "persistence.size"):          values["persistence.size"],
		VarName(Loki, "persistence.storage-class"): values["persistence.storage-class"],
	}, nil
}
