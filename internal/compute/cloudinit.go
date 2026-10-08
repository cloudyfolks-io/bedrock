package compute

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	goyaml "sigs.k8s.io/yaml/goyaml.v3"

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
	ErrUserDataSetsKeys       = errors.New("user data must not set ssh_authorized_keys when SSH keys are listed")
	ErrUserDataNotMapping     = errors.New("the #cloud-config document must be a block mapping of one document")
	normalizedKeyPattern      = regexp.MustCompile(`^[a-z0-9@.-]+ [A-Za-z0-9+/=]+$`)
	yamlLinePattern           = regexp.MustCompile(`line (\d+)`)
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
	body := cloudConfigHeader + "\n"
	if strings.TrimSpace(userData) != "" {
		if err := checkCloudConfig(userData); err != nil {
			return "", err
		}
		body = userData
		if !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
	}
	text := body + keysBlock(keys)
	if len(text) > maxUserDataBytes {
		return "", fmt.Errorf("the user data with the SSH keys is larger than %d bytes", maxUserDataBytes)
	}
	return text, nil
}

func keysBlock(keys []string) string {
	var block strings.Builder
	block.WriteString(keysField + ":\n")
	seen := map[string]bool{}
	for _, key := range keys {
		if !seen[key] {
			seen[key] = true
			block.WriteString("- " + key + "\n")
		}
	}
	return block.String()
}

func checkCloudConfig(userData string) error {
	first, _, _ := strings.Cut(userData, "\n")
	if strings.TrimSpace(first) != cloudConfigHeader {
		return ErrUserDataNotCloudConfig
	}
	decoder := goyaml.NewDecoder(strings.NewReader(userData))
	var root goyaml.Node
	if err := decoder.Decode(&root); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return invalidYAML(err)
	}
	var second goyaml.Node
	if err := decoder.Decode(&second); !errors.Is(err, io.EOF) {
		return ErrUserDataNotMapping
	}
	return checkRoot(root)
}

func checkRoot(root goyaml.Node) error {
	if len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	if doc.Kind == goyaml.ScalarNode && doc.Tag == "!!null" {
		return nil
	}
	if doc.Kind != goyaml.MappingNode || doc.Style&goyaml.FlowStyle != 0 {
		return ErrUserDataNotMapping
	}
	for index := 0; index+1 < len(doc.Content); index += 2 {
		if doc.Content[index].Value == keysField {
			return ErrUserDataSetsKeys
		}
	}
	return nil
}

func invalidYAML(err error) error {
	if match := yamlLinePattern.FindStringSubmatch(err.Error()); match != nil {
		return fmt.Errorf("the user data is not valid YAML near line %s", match[1])
	}
	return errors.New("the user data is not valid YAML")
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
