package objectstorage

import (
	"context"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	ServiceName       = "rook-ceph-rgw-" + StoreName
	ServiceHost       = ServiceName + "." + StoreNamespace + ".svc"
	publicName        = "objectstorage-public"
	rateLimitName     = "objectstorage-public-rate-limit"
	wildcardName      = "objectstorage-public-wildcard"
	wildcardPriority  = int64(1)
	publicSecretName  = "objectstorage-public-tls"
	issuerCA          = "bedrock-ca"
	issuerLetsEncrypt = "letsencrypt"
	tlsModeKey        = "platform.tls-mode"
	tlsModeLetsEnc    = "LetsEncrypt"
	gatewayPort       = int64(80)
)

func publicHosts(rules Rules) []string {
	if rules.PublicHost == "" {
		return nil
	}
	return append([]string{rules.PublicHost}, rules.PublicAliases...)
}

func HostingDNSNames(rules Rules) []string {
	hosts := publicHosts(rules)
	if hosts == nil {
		return nil
	}
	return append(hosts, ServiceHost)
}

func PublicEndpoint(rules Rules, internal string) string {
	if rules.PublicHost == "" {
		return internal
	}
	return "https://" + rules.PublicHost
}

func issuerFor(tlsMode string) string {
	if tlsMode == tlsModeLetsEnc {
		return issuerLetsEncrypt
	}
	return issuerCA
}

func EndpointObjects(rules Rules, tlsMode string) []*unstructured.Unstructured {
	hosts := publicHosts(rules)
	if hosts == nil {
		return nil
	}
	secretName := publicSecretName
	if rules.TLSSecret != "" {
		secretName = rules.TLSSecret
	}
	var limit []*unstructured.Unstructured
	var middlewares []any
	if rules.RateLimitAverage > 0 {
		limit = []*unstructured.Unstructured{rateLimitMiddleware(rules)}
		middlewares = []any{map[string]any{"name": rateLimitName}}
	}
	route := endpointObject("traefik.io/v1alpha1", "IngressRoute", publicName, publicRouteSpec(routeMatch(rules), secretName, nil, middlewares))
	wildcard := endpointObject("traefik.io/v1alpha1", "IngressRoute", wildcardName, publicRouteSpec(wildcardMatch(rules), secretName, wildcardPriority, middlewares))
	if rules.TLSSecret != "" {
		return append(limit, route, wildcard)
	}
	certificate := endpointObject("cert-manager.io/v1", "Certificate", publicName, map[string]any{
		"secretName": publicSecretName,
		"dnsNames":   certificateNames(rules, issuerFor(tlsMode)),
		"issuerRef":  map[string]any{"kind": "ClusterIssuer", "name": issuerFor(tlsMode)},
	})
	return append(limit, certificate, route, wildcard)
}

func rateLimitMiddleware(rules Rules) *unstructured.Unstructured {
	return endpointObject("traefik.io/v1alpha1", "Middleware", rateLimitName, map[string]any{
		"rateLimit": map[string]any{"average": int64(rules.RateLimitAverage), "burst": int64(rules.RateLimitBurst)},
	})
}

func publicRouteSpec(match, secretName string, priority any, middlewares []any) map[string]any {
	rule := map[string]any{
		"match":    match,
		"kind":     "Rule",
		"services": []any{map[string]any{"name": ServiceName, "port": gatewayPort}},
	}
	if middlewares != nil {
		rule["middlewares"] = middlewares
	}
	if priority != nil {
		rule["priority"] = priority
	}
	return map[string]any{
		"entryPoints": []any{"websecure"},
		"routes":      []any{rule},
		"tls":         map[string]any{"secretName": secretName},
	}
}

func certificateNames(rules Rules, issuer string) []any {
	hosts := publicHosts(rules)
	names := make([]any, 0, 2*len(hosts))
	for _, host := range hosts {
		names = append(names, host)
	}
	if issuer == issuerCA {
		for _, host := range hosts {
			names = append(names, "*."+host)
		}
	}
	return names
}

const adminGuard = "!(Path(`/admin`) || PathPrefix(`/admin/`))"

func routeMatch(rules Rules) string {
	hosts := publicHosts(rules)
	matches := make([]string, 0, 2*len(hosts))
	for _, host := range hosts {
		matches = append(matches, "(Host(`"+host+"`) && "+adminGuard+")")
	}
	return strings.Join(matches, " || ")
}

func wildcardMatch(rules Rules) string {
	hosts := publicHosts(rules)
	matches := make([]string, 0, len(hosts))
	for _, host := range hosts {
		matches = append(matches, "HostRegexp(`^[a-z0-9-]+\\."+strings.ReplaceAll(host, ".", "\\.")+"$`)")
	}
	return strings.Join(matches, " || ")
}

func endpointObject(apiVersion, kind, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": StoreNamespace,
			"labels":    map[string]any{v1alpha1.LabelKind: kind, v1alpha1.LabelName: name},
		},
		"spec": spec,
	}}
}

type EndpointReconciler struct {
	Client client.Client
}

func (r *EndpointReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	values, err := settingValues(ctx, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	rules, err := ParseRules(values)
	if err != nil {
		logf.FromContext(ctx).Info("object storage settings are not valid, the public endpoint is unchanged", "error", err.Error())
		return ctrl.Result{RequeueAfter: steadyRequeue}, nil
	}
	if err := r.converge(ctx, rules, values[tlsModeKey]); err != nil {
		if meta.IsNoMatchError(err) {
			logf.FromContext(ctx).Info("the public endpoint needs cert-manager and Traefik", "error", err.Error())
			return ctrl.Result{RequeueAfter: storeRequeue}, nil
		}
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: steadyRequeue}, nil
}

func (r *EndpointReconciler) converge(ctx context.Context, rules Rules, tlsMode string) error {
	desired := EndpointObjects(rules, tlsMode)
	if len(desired) == 0 {
		return r.remove(ctx)
	}
	for _, obj := range desired {
		if err := ssa.Apply(ctx, r.Client, obj, v1alpha1.ObjectStorageFieldManager); err != nil {
			return err
		}
	}
	if rules.RateLimitAverage == 0 {
		if err := r.removeOwned(ctx, "traefik.io/v1alpha1", "Middleware", rateLimitName); err != nil {
			return err
		}
	}
	if rules.TLSSecret != "" {
		if err := r.removeOwned(ctx, "cert-manager.io/v1", "Certificate", publicName); err != nil {
			return err
		}
		return r.warnOnMissingSecret(ctx, rules.TLSSecret)
	}
	return nil
}

func (r *EndpointReconciler) warnOnMissingSecret(ctx context.Context, name string) error {
	err := r.Client.Get(ctx, client.ObjectKey{Namespace: StoreNamespace, Name: name}, &corev1.Secret{})
	if apierrors.IsNotFound(err) {
		logf.FromContext(ctx).Info("objectstorage.tls-secret names a Secret that does not exist, Traefik serves its default certificate", "secret", name, "namespace", StoreNamespace)
		return nil
	}
	return err
}

func (r *EndpointReconciler) removeOwned(ctx context.Context, apiVersion, kind, name string) error {
	obj := endpointObject(apiVersion, kind, name, nil)
	live := &unstructured.Unstructured{}
	live.SetAPIVersion(apiVersion)
	live.SetKind(kind)
	err := r.Client.Get(ctx, client.ObjectKeyFromObject(obj), live)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if live.GetLabels()[v1alpha1.LabelName] != name {
		return nil
	}
	return client.IgnoreNotFound(r.Client.Delete(ctx, live))
}

func (r *EndpointReconciler) remove(ctx context.Context) error {
	for _, target := range [][3]string{{"cert-manager.io/v1", "Certificate", publicName}, {"traefik.io/v1alpha1", "IngressRoute", publicName}, {"traefik.io/v1alpha1", "IngressRoute", wildcardName}, {"traefik.io/v1alpha1", "Middleware", rateLimitName}} {
		if err := r.removeOwned(ctx, target[0], target[1], target[2]); err != nil {
			return err
		}
	}
	return nil
}

func publicEndpointSetting(obj client.Object) bool {
	return objectStorageSetting(obj) || obj.GetName() == tlsModeKey
}

func (r *EndpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("objectstorage-endpoint").
		Watches(&v1alpha1.Setting{}, handler.EnqueueRequestsFromMapFunc(toRulesRequest), builder.WithPredicates(predicate.NewPredicateFuncs(publicEndpointSetting))).
		Complete(r)
}
