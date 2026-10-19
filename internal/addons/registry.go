package addons

import (
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	RegistryNamespace      = "bedrock-registry"
	registryEnabledSetting = "registry.enabled"
	registryHostSetting    = "registry.host"
)

var registryScaledDeployments = []string{"harbor-core", "harbor-registry", "harbor-jobservice"}

type Facts struct {
	Settings         map[string]string
	EnabledAddons    map[string]bool
	CertManagerReady bool
}

func registryDefinition() Definition {
	return Definition{
		Name:           Registry,
		EnabledSetting: registryEnabledSetting,
		Check:          RegistryCheck,
		Vars:           registryVars,
		Adjust:         registryAdjust,
	}
}

func RegistryCheck(facts Facts) Verdict {
	var missing []string
	reason := ""
	note := func(why, text string) {
		if reason == "" {
			reason = why
		}
		missing = append(missing, text)
	}
	if facts.Settings[registryHostSetting] == "" {
		note("HostNotSet", "set the Setting registry.host")
	}
	if !facts.EnabledAddons[CNPG] {
		note("CNPGAddonOff", "turn on the addon cnpg")
	}
	if enabled, _ := strconv.ParseBool(facts.Settings["database.enabled"]); !enabled {
		note("DatabaseDisabled", "set the Setting database.enabled to true")
	}
	if !facts.CertManagerReady {
		note("CertManagerNotReady", "wait for cert-manager to be ready")
	}
	if len(missing) > 0 {
		return Verdict{State: VerdictUnknown, Reason: reason, Message: strings.Join(missing, "; ")}
	}
	return Verdict{State: VerdictPassed, Reason: "Passed", Message: "the registry has its host, its database platform and cert-manager"}
}

func registryVars(_, settings map[string]string) (map[string]string, error) {
	host := settings[registryHostSetting]
	if host == "" {
		return nil, fmt.Errorf("setting %s must be set", registryHostSetting)
	}
	return map[string]string{
		VarName(Registry, "host"):          host,
		VarName(Registry, "storage-class"): settings["registry.storage-class"],
		VarName(Registry, "bucket"):        settings["registry.bucket"],
		VarName(Registry, "http-proxy"):    settings["registry.http-proxy"],
	}, nil
}

func registryAdjust(objects []*unstructured.Unstructured, settings map[string]string) ([]*unstructured.Unstructured, error) {
	replicas, err := strconv.ParseInt(settings["registry.replicas"], 10, 64)
	if err != nil || replicas < 1 {
		return nil, fmt.Errorf("setting registry.replicas must be a positive whole number, got %q", settings["registry.replicas"])
	}
	adjusted := make([]*unstructured.Unstructured, 0, len(objects))
	for _, obj := range objects {
		if obj.GetKind() == "Namespace" {
			continue
		}
		copied := obj.DeepCopy()
		if copied.GetKind() == "Deployment" && isScaled(copied.GetName()) {
			if err := unstructured.SetNestedField(copied.Object, replicas, "spec", "replicas"); err != nil {
				return nil, err
			}
		}
		adjusted = append(adjusted, copied)
	}
	return adjusted, nil
}

func isScaled(name string) bool {
	for _, scaled := range registryScaledDeployments {
		if scaled == name {
			return true
		}
	}
	return false
}
