package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	rookGroup             = "rook"
	rookNamespace         = "rook-ceph"
	rookMonSecretName     = "rook-ceph-mon"
	rookMonEndpointsName  = "rook-ceph-mon-endpoints"
	rookSecretType        = "kubernetes.io/rook"
	rookDisasterFinalizer = "ceph.rook.io/disaster-protection"
	rookFSIDKey           = "fsid"
	rookMonPort           = "6789"
)

type rookKeys struct {
	FSID         string `json:"fsid"`
	MonSecret    string `json:"mon-secret"`
	CephUsername string `json:"ceph-username"`
	CephSecret   string `json:"ceph-secret"`
}

type rookMonNode struct {
	Name     string
	Hostname string
	Address  string
}

type rookMonMapping struct {
	Node map[string]rookMonNode `json:"node"`
}

func loadRookMonSecret(adopt v1alpha1.AdoptSpec) (*corev1.Secret, error) {
	keys, err := readRookKeys(adopt.KeyFile, adopt.FSID)
	if err != nil {
		return nil, err
	}
	return rookMonSecret(keys), nil
}

func readRookKeys(path, fsid string) (rookKeys, error) {
	raw, err := readOwnerOnlyFile(path)
	if err != nil {
		return rookKeys{}, keyFileError(path, err)
	}
	var keys rookKeys
	if err := yaml.Unmarshal(raw, &keys); err != nil {
		return rookKeys{}, keyFileError(path, errors.New("not a YAML mapping of strings"))
	}
	if name := missingRookKey(keys); name != "" {
		return rookKeys{}, keyFileError(path, errors.New("missing "+name))
	}
	if keys.FSID != fsid {
		return rookKeys{}, keyFileError(path, errors.New("fsid differs from storage.adopt.fsid"))
	}
	return keys, nil
}

func keyFileError(path string, reason error) error {
	return fmt.Errorf("adopt key file %s: %w", path, reason)
}

func readOwnerOnlyFile(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, osReason(err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if perm := info.Mode().Perm(); perm != 0o600 && perm != 0o400 {
		return nil, fmt.Errorf("mode is %04o, want 0600 or 0400", perm)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, osReason(err)
	}
	return raw, nil
}

func osReason(err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return errors.New("cannot be read")
}

func missingRookKey(keys rookKeys) string {
	fields := []struct{ name, value string }{
		{rookFSIDKey, keys.FSID},
		{"mon-secret", keys.MonSecret},
		{"ceph-username", keys.CephUsername},
		{"ceph-secret", keys.CephSecret},
	}
	for _, field := range fields {
		if field.value == "" {
			return field.name
		}
	}
	return ""
}

func rookMonSecret(keys rookKeys) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: rookNamespace, Name: rookMonSecretName, Finalizers: []string{rookDisasterFinalizer}},
		Type:       rookSecretType,
		Data: map[string][]byte{
			rookFSIDKey:     []byte(keys.FSID),
			"mon-secret":    []byte(keys.MonSecret),
			"ceph-username": []byte(keys.CephUsername),
			"ceph-secret":   []byte(keys.CephSecret),
		},
	}
}

func rookMonEndpoints(monitors []v1alpha1.AdoptMonitor) (*corev1.ConfigMap, error) {
	endpoints := make([]string, 0, len(monitors))
	mapping := rookMonMapping{Node: map[string]rookMonNode{}}
	maxIndex := 0
	for _, monitor := range monitors {
		endpoints = append(endpoints, monitor.ID+"="+monitor.Address+":"+rookMonPort)
		mapping.Node[monitor.ID] = rookMonNode{Name: monitor.Node, Hostname: monitor.Node, Address: monitor.Address}
		maxIndex = max(maxIndex, monitorIndex(monitor.ID))
	}
	rawMapping, err := json.Marshal(mapping)
	if err != nil {
		return nil, err
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: rookNamespace, Name: rookMonEndpointsName, Finalizers: []string{rookDisasterFinalizer}},
		Data: map[string]string{
			"data":     strings.Join(endpoints, ","),
			"mapping":  string(rawMapping),
			"maxMonId": strconv.Itoa(maxIndex),
		},
	}, nil
}

func monitorIndex(id string) int {
	index := 0
	for _, letter := range id {
		index = index*26 + int(letter-'a') + 1
	}
	return index - 1
}

func rookIdentityHook(c client.Client, adopt *v1alpha1.AdoptSpec, preloaded *corev1.Secret) func(context.Context, release.Group) error {
	if adopt == nil {
		return release.SkipHook
	}
	return func(ctx context.Context, group release.Group) error {
		if group.Name != rookGroup {
			return nil
		}
		return ensureRookIdentity(ctx, c, *adopt, preloaded)
	}
}

func ensureRookIdentity(ctx context.Context, c client.Client, adopt v1alpha1.AdoptSpec, preloaded *corev1.Secret) error {
	adopted, err := rookSecretAdopted(ctx, c, adopt.FSID)
	if err != nil {
		return err
	}
	if !adopted {
		secret, err := rookSecretToWrite(adopt, preloaded)
		if err != nil {
			return err
		}
		if err := client.IgnoreAlreadyExists(c.Create(ctx, secret.DeepCopy(), client.FieldOwner(initFieldOwner))); err != nil {
			return err
		}
	}
	endpoints, err := rookMonEndpoints(adopt.Monitors)
	if err != nil {
		return err
	}
	return client.IgnoreAlreadyExists(c.Create(ctx, endpoints, client.FieldOwner(initFieldOwner)))
}

func rookSecretToWrite(adopt v1alpha1.AdoptSpec, preloaded *corev1.Secret) (*corev1.Secret, error) {
	if preloaded != nil {
		return preloaded, nil
	}
	return loadRookMonSecret(adopt)
}

func rookSecretAdopted(ctx context.Context, c client.Client, fsid string) (bool, error) {
	var existing corev1.Secret
	err := c.Get(ctx, client.ObjectKey{Namespace: rookNamespace, Name: rookMonSecretName}, &existing)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if string(existing.Data[rookFSIDKey]) != fsid {
		return false, fmt.Errorf("secret %s/%s holds another fsid than storage.adopt.fsid", rookNamespace, rookMonSecretName)
	}
	return true, nil
}
