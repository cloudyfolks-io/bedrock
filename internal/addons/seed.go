package addons

import (
	"context"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

func settingValues(ctx context.Context, c client.Client) (map[string]string, error) {
	var list v1alpha1.SettingList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
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
	return values, nil
}

func Seed(ctx context.Context, c client.Client) error {
	values, err := settingValues(ctx, c)
	if err != nil {
		return err
	}
	for _, def := range Known() {
		enabled, _ := strconv.ParseBool(values[def.EnabledSetting])
		addon := &v1alpha1.Addon{
			ObjectMeta: metav1.ObjectMeta{Name: def.Name, Labels: map[string]string{v1alpha1.LabelKind: "Addon", v1alpha1.LabelName: def.Name}},
			Spec:       v1alpha1.AddonSpec{Enabled: enabled},
		}
		if err := c.Create(ctx, addon, client.FieldOwner(v1alpha1.AddonsFieldManager)); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	}
	return nil
}

type seeder struct {
	client client.Client
}

func (s *seeder) Start(ctx context.Context) error {
	return Seed(ctx, s.client)
}

func (s *seeder) NeedLeaderElection() bool {
	return true
}

func addSeeder(mgr ctrl.Manager) error {
	return mgr.Add(&seeder{client: mgr.GetClient()})
}
