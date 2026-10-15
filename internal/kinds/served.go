package kinds

import (
	"cmp"
	"context"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const pollInterval = 5 * time.Second

func OfGroup(scheme *runtime.Scheme, group string) []schema.GroupVersionKind {
	known := scheme.AllKnownTypes()
	var resources []schema.GroupVersionKind
	for gvk := range known {
		if _, hasList := known[gvk.GroupVersion().WithKind(gvk.Kind+"List")]; gvk.Group == group && hasList {
			resources = append(resources, gvk)
		}
	}
	slices.SortFunc(resources, func(a, b schema.GroupVersionKind) int { return cmp.Compare(a.String(), b.String()) })
	return resources
}

func FirstMissing(mapper meta.RESTMapper, required []schema.GroupVersionKind) (schema.GroupVersionKind, bool) {
	for _, gvk := range required {
		if _, err := mapper.RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
			return gvk, true
		}
	}
	return schema.GroupVersionKind{}, false
}

func SetupWhenServed(mgr ctrl.Manager, area string, required []schema.GroupVersionKind, setup func(ctrl.Manager) error) error {
	missing, isMissing := FirstMissing(mgr.GetRESTMapper(), required)
	if !isMissing {
		return setup(mgr)
	}
	log := mgr.GetLogger().WithName(area)
	log.Info("the controllers wait until the required kinds are served", "kind", missing.String())
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		served := func(context.Context) (bool, error) {
			_, isMissing := FirstMissing(mgr.GetRESTMapper(), required)
			return !isMissing, nil
		}
		if err := wait.PollUntilContextCancel(ctx, pollInterval, false, served); err != nil {
			return nil
		}
		log.Info("the required kinds are served; registering the controllers")
		return setup(mgr)
	}))
}
