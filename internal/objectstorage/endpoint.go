package objectstorage

import (
	"context"
	"strings"

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
	return []*unstructured.Unstructured{
		endpointObject("cert-manager.io/v1", "Certificate", map[string]any{
			"secretName": publicSecretName,
			"dnsNames":   certificateNames(rules, issuerFor(tlsMode)),
			"issuerRef":  map[string]any{"kind": "ClusterIssuer", "name": issuerFor(tlsMode)},
		}),
		endpointObject("traefik.io/v1alpha1", "IngressRoute", map[string]any{
			"entryPoints": []any{"websecure"},
			"routes": []any{map[string]any{
				"match":    routeMatch(rules),
				"kind":     "Rule",
				"services": []any{map[string]any{"name": ServiceName, "port": gatewayPort}},
			}},
			"tls": map[string]any{"secretName": publicSecretName},
		}),
	}
}

func certificateNames(rules Rules, issuer string) []any {
	hosts := publicHosts(rules)
	names := make([]any, 0, 2*len(hosts))
	for _, host := range hosts {
		names = append(names, host)
	}
	if len(rules.PublicAliases) > 0 && issuer == issuerCA {
		for _, host := range hosts {
			names = append(names, "*."+host)
		}
	}
	return names
}

func routeMatch(rules Rules) string {
	hosts := publicHosts(rules)
	matches := make([]string, 0, 2*len(hosts))
	for _, host := range hosts {
		matches = append(matches, "Host(`"+host+"`)")
	}
	if len(rules.PublicAliases) > 0 {
		for _, host := range hosts {
			matches = append(matches, "HostRegexp(`^[a-z0-9-]+\\."+strings.ReplaceAll(host, ".", "\\.")+"$`)")
		}
	}
	return "(" + strings.Join(matches, " || ") + ") && !PathPrefix(`/admin`)"
}

func endpointObject(apiVersion, kind string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      publicName,
			"namespace": StoreNamespace,
			"labels":    map[string]any{v1alpha1.LabelKind: kind, v1alpha1.LabelName: publicName},
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
	return nil
}

func (r *EndpointReconciler) remove(ctx context.Context) error {
	for _, apiKind := range [][2]string{{"cert-manager.io/v1", "Certificate"}, {"traefik.io/v1alpha1", "IngressRoute"}} {
		obj := endpointObject(apiKind[0], apiKind[1], nil)
		if err := client.IgnoreNotFound(r.Client.Delete(ctx, obj)); err != nil && !meta.IsNoMatchError(err) {
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
