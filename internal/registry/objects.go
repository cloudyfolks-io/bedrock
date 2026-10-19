package registry

import (
	"net/netip"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	harborCoreService    = "harbor-core"
	harborPortalService  = "harbor-portal"
	servicePort          = int64(80)
	rulePriority         = int64(20)
	portalPriority       = int64(10)
	coreAPIPaths         = "PathPrefix(`/v2/`) || PathPrefix(`/service/`) || PathPrefix(`/c/`)"
	adminAllowName       = "harbor-admin-allow"
	adminRouteName       = "harbor-admin"
	primaryRouteName     = "harbor-public"
	jobserviceEgressName = "harbor-jobservice-egress"
	operatorRoleName     = "bedrock-registry-operator"
)

func object(apiVersion, kind, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": Namespace,
			"labels":    map[string]any{v1alpha1.LabelKind: kind, v1alpha1.LabelName: name, managedByLabel: managedByValue},
		},
		"spec": spec,
	}}
}

func NamespaceObject() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]any{
			"name":   Namespace,
			"labels": map[string]any{v1alpha1.LabelKind: "PlatformNamespace", v1alpha1.LabelName: Namespace, managedByLabel: managedByValue},
		},
	}}
}

func TokenCertificate() *unstructured.Unstructured {
	return object("cert-manager.io/v1", "Certificate", tokenCertificateName, map[string]any{
		"secretName":  tokenCertificateName,
		"commonName":  "harbor-token-ca",
		"isCA":        true,
		"duration":    "87600h",
		"renewBefore": "720h",
		"privateKey":  map[string]any{"algorithm": "RSA", "size": int64(2048), "rotationPolicy": "Never"},
		"issuerRef":   map[string]any{"kind": "ClusterIssuer", "name": selfSignedIssuer},
	})
}

func DatabaseCluster(config Config) *unstructured.Unstructured {
	return object("postgresql.cnpg.io/v1", "Cluster", databaseClusterName, map[string]any{
		"instances": int64(config.DBInstances),
		"imageName": DatabaseImage,
		"bootstrap": map[string]any{"initdb": map[string]any{
			"database": databaseName,
			"owner":    databaseUser,
			"secret":   map[string]any{"name": databaseCredentialsSecret},
		}},
		"storage":    map[string]any{"size": databaseStorageSize, "storageClass": config.StorageClass},
		"monitoring": map[string]any{"enablePodMonitor": true},
		"plugins": []any{map[string]any{
			"name":          barmanPlugin,
			"isWALArchiver": true,
			"parameters":    map[string]any{"barmanObjectName": backupObjectStoreName},
		}},
	})
}

func BackupObjectStore(config Config, endpoint string) *unstructured.Unstructured {
	credentials := func(key string) map[string]any {
		return map[string]any{"name": backupIdentityName, "key": key}
	}
	return object("barmancloud.cnpg.io/v1", "ObjectStore", backupObjectStoreName, map[string]any{
		"retentionPolicy": backupRetention,
		"configuration": map[string]any{
			"destinationPath": "s3://" + config.BackupBucket + "/" + backupPrefix + "/",
			"endpointURL":     endpoint,
			"s3Credentials":   map[string]any{"accessKeyId": credentials("ACCESS_KEY_ID"), "secretAccessKey": credentials("ACCESS_SECRET_KEY")},
			"wal":             map[string]any{"compression": "gzip"},
		},
	})
}

func BackupSchedule() *unstructured.Unstructured {
	return object("postgresql.cnpg.io/v1", "ScheduledBackup", backupScheduleName, map[string]any{
		"schedule":             backupSchedule,
		"method":               "plugin",
		"cluster":              map[string]any{"name": databaseClusterName},
		"backupOwnerReference": "self",
		"pluginConfiguration":  map[string]any{"name": barmanPlugin},
	})
}

func DatabaseObjects(config Config, endpoint string) []*unstructured.Unstructured {
	return []*unstructured.Unstructured{DatabaseCluster(config), BackupObjectStore(config, endpoint), BackupSchedule()}
}

func slug(host string) string {
	return strings.ReplaceAll(host, ".", "-")
}

func issuerFor(tlsMode string) string {
	if tlsMode == tlsModeLetsEncrypt {
		return letsEncryptIssuer
	}
	return caIssuer
}

func hostNames(config Config) []any {
	hosts := config.PublicHosts()
	if config.AdminHost != "" && len(config.AdminAllow) > 0 {
		hosts = append(hosts, config.AdminHost)
	}
	names := make([]any, 0, len(hosts))
	for _, host := range hosts {
		names = append(names, host)
	}
	return names
}

func PublicCertificate(config Config) *unstructured.Unstructured {
	return object("cert-manager.io/v1", "Certificate", publicName, map[string]any{
		"secretName": publicSecretName,
		"dnsNames":   hostNames(config),
		"issuerRef":  map[string]any{"kind": "ClusterIssuer", "name": issuerFor(config.TLSMode)},
	})
}

func realmMiddleware(host string) *unstructured.Unstructured {
	return object("traefik.io/v1alpha1", "Middleware", "harbor-realm-"+slug(host), map[string]any{
		"headers": map[string]any{"customResponseHeaders": map[string]any{
			"WWW-Authenticate": `Bearer realm="https://` + host + `/service/token",service="harbor-registry"`,
		}},
	})
}

func allowMiddleware(cidrs []string) *unstructured.Unstructured {
	ranges := make([]any, 0, len(cidrs))
	for _, cidr := range cidrs {
		ranges = append(ranges, cidr)
	}
	return object("traefik.io/v1alpha1", "Middleware", adminAllowName, map[string]any{
		"ipAllowList": map[string]any{"sourceRange": ranges},
	})
}

func rule(match string, priority int64, middlewares []string, service string) map[string]any {
	refs := make([]any, 0, len(middlewares))
	for _, name := range middlewares {
		refs = append(refs, map[string]any{"name": name})
	}
	out := map[string]any{
		"match":    match,
		"kind":     "Rule",
		"priority": priority,
		"services": []any{map[string]any{"name": service, "port": servicePort}},
	}
	if len(refs) > 0 {
		out["middlewares"] = refs
	}
	return out
}

func route(name string, rules ...map[string]any) *unstructured.Unstructured {
	items := make([]any, 0, len(rules))
	for _, item := range rules {
		items = append(items, item)
	}
	return object("traefik.io/v1alpha1", "IngressRoute", name, map[string]any{
		"entryPoints": []any{"websecure"},
		"routes":      items,
		"tls":         map[string]any{"secretName": publicSecretName},
	})
}

func publicRoute(name, host string, middlewares []string) *unstructured.Unstructured {
	return route(name,
		rule("Host(`"+host+"`) && ("+coreAPIPaths+")", rulePriority, middlewares, harborCoreService),
		rule("Host(`"+host+"`) && PathPrefix(`/api/`)", rulePriority, nil, harborCoreService),
	)
}

func adminRoute(config Config) *unstructured.Unstructured {
	allow := []string{adminAllowName}
	realm := []string{adminAllowName, "harbor-realm-" + slug(config.AdminHost)}
	host := config.AdminHost
	return route(adminRouteName,
		rule("Host(`"+host+"`) && ("+coreAPIPaths+")", rulePriority, realm, harborCoreService),
		rule("Host(`"+host+"`) && PathPrefix(`/api/`)", rulePriority, allow, harborCoreService),
		rule("Host(`"+host+"`)", portalPriority, allow, harborPortalService),
	)
}

func RouteObjects(config Config) []*unstructured.Unstructured {
	objects := []*unstructured.Unstructured{PublicCertificate(config), publicRoute(primaryRouteName, config.Host, nil)}
	for _, alias := range config.Aliases {
		middleware := realmMiddleware(alias)
		objects = append(objects, middleware, publicRoute("harbor-public-"+slug(alias), alias, []string{middleware.GetName()}))
	}
	if config.AdminHost != "" && len(config.AdminAllow) > 0 {
		objects = append(objects, allowMiddleware(config.AdminAllow), realmMiddleware(config.AdminHost), adminRoute(config))
	}
	return objects
}

func JobserviceEgressPolicy(config Config, nodeAddresses []string) *unstructured.Unstructured {
	except := make([]any, 0, len(config.BlockedCIDRs)+len(nodeAddresses))
	for _, cidr := range config.BlockedCIDRs {
		except = append(except, cidr)
	}
	for _, address := range nodeAddresses {
		if parsed, err := netip.ParseAddr(address); err == nil && parsed.Is4() {
			except = append(except, parsed.String()+"/32")
		}
	}
	anywhere := map[string]any{"cidr": "0.0.0.0/0"}
	if len(except) > 0 {
		anywhere["except"] = except
	}
	policy := object("networking.k8s.io/v1", "NetworkPolicy", jobserviceEgressName, map[string]any{
		"podSelector": map[string]any{"matchLabels": map[string]any{"app": "harbor", "component": "jobservice"}},
		"policyTypes": []any{"Egress"},
		"egress": []any{
			map[string]any{
				"to": []any{map[string]any{
					"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "kube-system"}},
					"podSelector":       map[string]any{"matchLabels": map[string]any{"k8s-app": "kube-dns"}},
				}},
				"ports": []any{map[string]any{"protocol": "UDP", "port": int64(53)}, map[string]any{"protocol": "TCP", "port": int64(53)}},
			},
			map[string]any{"to": []any{map[string]any{"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": Namespace}}}}},
			map[string]any{"to": []any{map[string]any{"ipBlock": anywhere}}},
		},
	})
	return policy
}

func OperatorRole() *unstructured.Unstructured {
	verbs := []any{"create", "delete", "get", "list", "patch", "update", "watch"}
	role := object("rbac.authorization.k8s.io/v1", "Role", operatorRoleName, nil)
	delete(role.Object, "spec")
	role.Object["rules"] = []any{
		map[string]any{"apiGroups": []any{"postgresql.cnpg.io"}, "resources": []any{"clusters", "scheduledbackups"}, "verbs": verbs},
		map[string]any{"apiGroups": []any{"barmancloud.cnpg.io"}, "resources": []any{"objectstores"}, "verbs": verbs},
		map[string]any{"apiGroups": []any{""}, "resources": []any{"secrets"}, "verbs": []any{"delete"}},
	}
	return role
}

func OperatorRoleBinding() *unstructured.Unstructured {
	binding := object("rbac.authorization.k8s.io/v1", "RoleBinding", operatorRoleName+"-binding", nil)
	delete(binding.Object, "spec")
	binding.Object["roleRef"] = map[string]any{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": operatorRoleName}
	binding.Object["subjects"] = []any{map[string]any{"kind": "ServiceAccount", "name": "bedrock-operator", "namespace": "bedrock-system"}}
	return binding
}
