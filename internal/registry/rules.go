package registry

import (
	"context"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	SystemNamespace    = "bedrock-system"
	RulesConfigMapName = "registry-name-rules"
)

type Rules struct {
	Host             string
	ReservedNames    []string
	ReservedPrefixes []string
	DefaultQuotaGB   int64
}

var ruleKeys = []string{"registry.host", "registry.reserved-names", "registry.reserved-prefixes", "registry.default-quota-gb"}

func ParseRules(values map[string]string) (Rules, error) {
	effective := map[string]string{}
	for _, key := range ruleKeys {
		def, _ := settings.Lookup(key)
		value := values[key]
		if value == "" {
			value = def.Default
		}
		if err := def.Validate(value); err != nil {
			return Rules{}, err
		}
		effective[key] = value
	}
	quota, _ := strconv.ParseInt(effective["registry.default-quota-gb"], 10, 64)
	return Rules{
		Host:             effective["registry.host"],
		ReservedNames:    splitList(effective["registry.reserved-names"]),
		ReservedPrefixes: splitList(effective["registry.reserved-prefixes"]),
		DefaultQuotaGB:   quota,
	}, nil
}

func LoadRules(ctx context.Context, c client.Reader) (Rules, error) {
	values, err := settings.Effective(ctx, c)
	if err != nil {
		return Rules{}, err
	}
	return ParseRules(values)
}

type RulesConfigReconciler struct {
	Client client.Client
}

func (r *RulesConfigReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	rules, err := LoadRules(ctx, r.Client)
	if err != nil {
		logf.FromContext(ctx).Info("registry settings are not valid, the name policy keeps its last rules", "error", err.Error())
		return ctrl.Result{RequeueAfter: steadyRequeue}, nil
	}
	config := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      RulesConfigMapName,
			Namespace: SystemNamespace,
			Labels:    map[string]string{v1alpha1.LabelKind: "ConfigMap", v1alpha1.LabelName: RulesConfigMapName},
		},
		Data: map[string]string{
			"reservedNames":    strings.Join(rules.ReservedNames, ","),
			"reservedPrefixes": strings.Join(rules.ReservedPrefixes, ","),
		},
	}
	return ctrl.Result{RequeueAfter: steadyRequeue}, ssa.Apply(ctx, r.Client, config, v1alpha1.RegistryFieldManager)
}

var singleRulesRequest = []reconcile.Request{{NamespacedName: types.NamespacedName{Name: RulesConfigMapName}}}

func toRulesRequest(context.Context, client.Object) []reconcile.Request {
	return singleRulesRequest
}

func nameRuleSetting(obj client.Object) bool {
	switch obj.GetName() {
	case "registry.reserved-names", "registry.reserved-prefixes":
		return true
	}
	return false
}

func (r *RulesConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ours := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetNamespace() == SystemNamespace && obj.GetName() == RulesConfigMapName
	})
	return ctrl.NewControllerManagedBy(mgr).Named("registry-rules").
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(toRulesRequest), builder.WithPredicates(predicate.NewPredicateFuncs(nameRuleSetting))).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(toRulesRequest), builder.WithPredicates(ours)).
		Complete(r)
}
