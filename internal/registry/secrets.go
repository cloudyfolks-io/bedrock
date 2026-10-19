package registry

import (
	"context"
	"errors"
	"io"

	"golang.org/x/crypto/bcrypt"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	alphabet       = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	acceptedBelow  = 248
	secretLength   = 32
	keyLength      = 16
	maxPasswordTry = 64
)

type secretSpec struct {
	name   string
	kind   corev1.SecretType
	values func(io.Reader) (map[string]string, error)
}

func secretSpecs() []secretSpec {
	return []secretSpec{
		{adminSecret, corev1.SecretTypeOpaque, generated(map[string]int{"HARBOR_ADMIN_PASSWORD": secretLength}, true)},
		{coreKeysSecret, corev1.SecretTypeOpaque, generated(map[string]int{"secret": secretLength, "CSRF_KEY": secretLength, "secretKey": keyLength}, false)},
		{jobserviceKeysSecret, corev1.SecretTypeOpaque, generated(map[string]int{"JOBSERVICE_SECRET": secretLength}, false)},
		{registryKeysSecret, corev1.SecretTypeOpaque, generated(map[string]int{"REGISTRY_HTTP_SECRET": secretLength}, false)},
		{registryCredentialsSecret, corev1.SecretTypeOpaque, registryCredentials},
		{databaseCredentialsSecret, corev1.SecretTypeBasicAuth, databaseCredentials},
	}
}

func generated(lengths map[string]int, strong bool) func(io.Reader) (map[string]string, error) {
	return func(random io.Reader) (map[string]string, error) {
		values := map[string]string{}
		for key, length := range lengths {
			value, err := randomString(random, length, strong)
			if err != nil {
				return nil, err
			}
			values[key] = value
		}
		return values, nil
	}
}

func registryCredentials(random io.Reader) (map[string]string, error) {
	password, err := randomString(random, secretLength, false)
	if err != nil {
		return nil, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	return map[string]string{"REGISTRY_PASSWD": password, "REGISTRY_HTPASSWD": registryUsername + ":" + string(hash)}, nil
}

func databaseCredentials(random io.Reader) (map[string]string, error) {
	password, err := randomString(random, secretLength, false)
	if err != nil {
		return nil, err
	}
	return map[string]string{"username": databaseUser, "password": password}, nil
}

func randomString(random io.Reader, length int, strong bool) (string, error) {
	for range maxPasswordTry {
		value, err := draw(random, length)
		if err != nil {
			return "", err
		}
		if !strong || hasEveryClass(value) {
			return value, nil
		}
	}
	return "", errors.New("no password with every character class after many draws")
}

func draw(random io.Reader, length int) (string, error) {
	out := make([]byte, 0, length)
	buffer := make([]byte, length)
	for len(out) < length {
		if _, err := io.ReadFull(random, buffer); err != nil {
			return "", err
		}
		for _, b := range buffer {
			if b < acceptedBelow && len(out) < length {
				out = append(out, alphabet[int(b)%len(alphabet)])
			}
		}
	}
	return string(out), nil
}

func hasEveryClass(value string) bool {
	var lower, upper, digit bool
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		default:
			digit = true
		}
	}
	return lower && upper && digit
}

func EnsureSecrets(ctx context.Context, c client.Client, random io.Reader) error {
	for _, spec := range secretSpecs() {
		if err := ensureSecret(ctx, c, spec, random); err != nil {
			return err
		}
	}
	return nil
}

func ensureSecret(ctx context.Context, c client.Client, spec secretSpec, random io.Reader) error {
	key := client.ObjectKey{Namespace: Namespace, Name: spec.name}
	err := c.Get(ctx, key, &corev1.Secret{})
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	values, err := spec.values(random)
	if err != nil {
		return err
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.name,
			Namespace: Namespace,
			Labels:    map[string]string{v1alpha1.LabelKind: "RegistrySecret", v1alpha1.LabelName: spec.name, managedByLabel: managedByValue},
		},
		Type: spec.kind,
		Data: bytesOf(values),
	}
	if err := c.Create(ctx, secret, client.FieldOwner(v1alpha1.RegistryFieldManager)); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func bytesOf(values map[string]string) map[string][]byte {
	data := make(map[string][]byte, len(values))
	for key, value := range values {
		data[key] = []byte(value)
	}
	return data
}
