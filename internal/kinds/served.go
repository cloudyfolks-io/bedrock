package kinds

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const pollInterval = 5 * time.Second

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
