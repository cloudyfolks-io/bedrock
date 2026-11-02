package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/k0s"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type kubeletValues map[string]string

func (k *kubeletValues) String() string { return fmt.Sprint(map[string]string(*k)) }

func (k *kubeletValues) Set(pair string) error {
	key, value, found := strings.Cut(pair, "=")
	if !found || !slices.Contains(k0s.ReservationKeys, key) {
		return fmt.Errorf("kubelet setting %q must be one of %s as key=value", pair, strings.Join(k0s.ReservationKeys, ", "))
	}
	def, _ := settings.Lookup(key)
	if err := def.Validate(value); err != nil {
		return fmt.Errorf("kubelet setting %s: %w", key, err)
	}
	if *k == nil {
		*k = kubeletValues{}
	}
	(*k)[key] = value
	return nil
}

func kubeletSettingObjects(values map[string]string) []v1alpha1.Setting {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make([]v1alpha1.Setting, 0, len(keys))
	for _, key := range keys {
		out = append(out, v1alpha1.Setting{
			ObjectMeta: metav1.ObjectMeta{Name: key, Labels: map[string]string{v1alpha1.LabelKind: "Setting", v1alpha1.LabelName: key}},
			Spec:       v1alpha1.SettingSpec{Value: values[key]},
		})
	}
	return out
}
