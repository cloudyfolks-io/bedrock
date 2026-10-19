package objectstorage

import (
	"context"
	"slices"
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
	RulesConfigMapName = "objectstorage-name-rules"

	keyReservedNames    = "objectstorage.reserved-bucket-names"
	keyReservedPrefixes = "objectstorage.reserved-bucket-prefixes"
	keyCORSOrigins      = "objectstorage.cors-origins"
	keyMaxBuckets       = "objectstorage.max-buckets"
	keyPublicHost       = "objectstorage.public-host"
	keyPublicAliases    = "objectstorage.public-aliases"
	settingPrefix       = "objectstorage."
)

var platformBucketKeys = []string{"registry.bucket", "database.backup.bucket"}

var ruleKeys = []string{keyReservedNames, keyReservedPrefixes, keyCORSOrigins, keyMaxBuckets, keyPublicHost, keyPublicAliases}

func ParseRules(values map[string]string) (Rules, error) {
	effective := make(map[string]string, len(ruleKeys))
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
	reserved := splitList(effective[keyReservedNames])
	for _, key := range platformBucketKeys {
		if name := effectiveValue(values, key); name != "" && !slices.Contains(reserved, name) {
			reserved = append(reserved, name)
		}
	}
	maxBuckets, _ := strconv.Atoi(effective[keyMaxBuckets])
	origins := splitList(effective[keyCORSOrigins])
	for i, origin := range origins {
		origins[i] = strings.TrimSuffix(origin, "/")
	}
	return Rules{
		ReservedNames:    reserved,
		ReservedPrefixes: splitList(effective[keyReservedPrefixes]),
		CORSOrigins:      origins,
		MaxBuckets:       maxBuckets,
		PublicHost:       effective[keyPublicHost],
		PublicAliases:    splitList(effective[keyPublicAliases]),
	}, nil
}

func effectiveValue(values map[string]string, key string) string {
	if value := values[key]; value != "" {
		return value
	}
	def, _ := settings.Lookup(key)
	return def.Default
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func settingValues(ctx context.Context, c client.Reader) (map[string]string, error) {
	var list v1alpha1.SettingList
	if err := c.List(ctx, &list); err != nil {
		return nil, err
	}
	values := make(map[string]string, len(list.Items))
	for _, setting := range list.Items {
		values[setting.Name] = setting.Spec.Value
	}
	return values, nil
}

func LoadRules(ctx context.Context, c client.Reader) (Rules, error) {
	values, err := settingValues(ctx, c)
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
		logf.FromContext(ctx).Info("object storage settings are not valid, the name policy keeps its last rules", "error", err.Error())
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
	return ctrl.Result{RequeueAfter: steadyRequeue}, ssa.Apply(ctx, r.Client, config, v1alpha1.ObjectStorageFieldManager)
}

var singleRulesRequest = []reconcile.Request{{NamespacedName: types.NamespacedName{Name: RulesConfigMapName}}}

func toRulesRequest(context.Context, client.Object) []reconcile.Request {
	return singleRulesRequest
}

func objectStorageSetting(obj client.Object) bool {
	return strings.HasPrefix(obj.GetName(), settingPrefix) || slices.Contains(platformBucketKeys, obj.GetName())
}

func (r *RulesConfigReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ours := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetNamespace() == SystemNamespace && obj.GetName() == RulesConfigMapName
	})
	return ctrl.NewControllerManagedBy(mgr).Named("objectstorage-rules").
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(toRulesRequest), builder.WithPredicates(predicate.NewPredicateFuncs(objectStorageSetting))).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(toRulesRequest), builder.WithPredicates(ours)).
		Complete(r)
}

func (r *BucketReconciler) bucketsOfSetting(ctx context.Context, _ client.Object) []reconcile.Request {
	var buckets v1alpha1.ObjectStorageBucketList
	if err := r.Client.List(ctx, &buckets); err != nil {
		return nil
	}
	requests := make([]reconcile.Request, 0, len(buckets.Items))
	for i := range buckets.Items {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&buckets.Items[i])})
	}
	return requests
}
