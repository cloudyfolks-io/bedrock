package compute

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	cloudConfigHeader = "#cloud-config"
	keysField         = "ssh_authorized_keys"
	maxUserDataBytes  = 65536
	secretUserData    = "userdata"
	secretNetworkData = "networkdata"
)

var (
	ErrUserDataNotCloudConfig = errors.New("user data must be a #cloud-config document when SSH keys are set")
	normalizedKeyPattern      = regexp.MustCompile(`^[a-z0-9@.-]+ [A-Za-z0-9+/=]+$`)
)

func UserData(userData string, keys []string) (string, error) {
	if len(keys) == 0 {
		return userData, nil
	}
	for _, key := range keys {
		if !normalizedKeyPattern.MatchString(key) {
			return "", errors.New("an SSH key is not in the normalized form")
		}
	}
	document, err := cloudConfigDocument(userData)
	if err != nil {
		return "", err
	}
	existing, err := existingKeys(document)
	if err != nil {
		return "", err
	}
	merged := []string{}
	for _, key := range append(existing, keys...) {
		if !slices.Contains(merged, key) {
			merged = append(merged, key)
		}
	}
	document[keysField] = merged
	out, err := yaml.Marshal(document)
	if err != nil {
		return "", err
	}
	text := cloudConfigHeader + "\n" + string(out)
	if len(text) > maxUserDataBytes {
		return "", fmt.Errorf("the user data with the SSH keys is larger than %d bytes", maxUserDataBytes)
	}
	return text, nil
}

func cloudConfigDocument(userData string) (map[string]any, error) {
	if strings.TrimSpace(userData) == "" {
		return map[string]any{}, nil
	}
	first, _, _ := strings.Cut(userData, "\n")
	if strings.TrimSpace(first) != cloudConfigHeader {
		return nil, ErrUserDataNotCloudConfig
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(userData), &document); err != nil {
		return nil, fmt.Errorf("the user data is not valid YAML: %w", err)
	}
	if document == nil {
		return map[string]any{}, nil
	}
	return document, nil
}

func existingKeys(document map[string]any) ([]string, error) {
	raw, found := document[keysField]
	if !found {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list", keysField)
	}
	keys := make([]string, 0, len(list))
	for _, item := range list {
		key, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s must hold text only", keysField)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func CloudInitSources(ci *v1alpha1.VMCloudInit, referenced *corev1.Secret) (string, string, error) {
	if ci == nil {
		return "", "", nil
	}
	if ci.SecretRef == "" {
		return ci.UserData, ci.NetworkData, nil
	}
	if ci.UserData != "" || ci.NetworkData != "" {
		return "", "", errors.New("secretRef cannot be used with inline cloud-init data")
	}
	if referenced == nil {
		return "", "", fmt.Errorf("the Secret %q does not exist", ci.SecretRef)
	}
	userData, found := referenced.Data[secretUserData]
	if !found {
		return "", "", fmt.Errorf("the Secret %q has no %q key", ci.SecretRef, secretUserData)
	}
	return string(userData), string(referenced.Data[secretNetworkData]), nil
}

func CloudInitSecret(vm v1alpha1.VirtualMachine, labels map[string]string, userData, networkData string) corev1.Secret {
	data := map[string][]byte{secretUserData: []byte(userData)}
	if networkData != "" {
		data[secretNetworkData] = []byte(networkData)
	}
	copied := make(map[string]string, len(labels))
	for key, value := range labels {
		copied[key] = value
	}
	return corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            CloudInitSecretName(vm.Name),
			Namespace:       vm.Namespace,
			Labels:          copied,
			OwnerReferences: []metav1.OwnerReference{OwnerRef(vm)},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}
