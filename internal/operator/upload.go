package operator

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	uploadNamespace        = "cdi"
	uploadProxyName        = "cdi-uploadproxy"
	uploadProxyCAConfigMap = "cdi-uploadproxy-signer-bundle"
	uploadProxyCAKey       = "ca-bundle.crt"
	uploadCAName           = "bedrock-upload-ca"
	uploadTransportName    = "bedrock-upload"
	uploadCORSName         = "bedrock-upload-cors"
	uploadRouteName        = "bedrock-upload"
	uploadPathPrefix       = "/v1beta1/upload"
)

var uploadAddon = Addon{Name: "upload", Condition: v1alpha1.ConditionUploadReady, Render: RenderUpload}

func RenderUpload(in AddonInput) (Rendered, error) {
	host := in.Settings["platform.host"]
	if host == "" {
		return platformHostMissing(), nil
	}
	if len(in.UploadProxyCA) == 0 {
		return Rendered{SkipReason: "WaitingForUploadProxyCA", SkipMessage: "configmap " + uploadNamespace + "/" + uploadProxyCAConfigMap + " not found"}, nil
	}
	objects := []*unstructured.Unstructured{
		uploadCAConfigMap(in.UploadProxyCA),
		uploadServersTransport(),
		uploadCORSMiddleware(host),
		uploadIngressRoute(host),
	}
	probe := Probe{GVK: authnDeploymentGVK, Key: client.ObjectKey{Namespace: uploadNamespace, Name: uploadProxyName}, Gate: release.DefaultGate}
	return Rendered{Objects: objects, Probes: []Probe{probe}}, nil
}

func uploadCAConfigMap(ca []byte) *unstructured.Unstructured {
	return uploadObject("v1", "ConfigMap", uploadCAName, map[string]any{"data": map[string]any{authnPlatformCAKey: string(ca)}})
}

func uploadServersTransport() *unstructured.Unstructured {
	return uploadObject(traefikAPIVersion, "ServersTransport", uploadTransportName, map[string]any{"spec": map[string]any{
		"serverName": uploadProxyName + "." + uploadNamespace + ".svc",
		"rootCAs":    []any{map[string]any{"configMap": uploadCAName}},
	}})
}

func uploadCORSMiddleware(host string) *unstructured.Unstructured {
	return uploadObject(traefikAPIVersion, "Middleware", uploadCORSName, map[string]any{"spec": map[string]any{
		"headers": map[string]any{
			"accessControlAllowMethods":    []any{"POST", "OPTIONS"},
			"accessControlAllowHeaders":    []any{"Authorization", "Content-Type"},
			"accessControlAllowOriginList": []any{"https://console." + host},
			"accessControlMaxAge":          int64(600),
			"addVaryHeader":                true,
		},
	}})
}

func uploadIngressRoute(host string) *unstructured.Unstructured {
	service := map[string]any{"name": uploadProxyName, "port": int64(443), "scheme": "https", "serversTransport": uploadTransportName}
	route := map[string]any{
		"match":       "Host(`upload." + host + "`) && PathPrefix(`" + uploadPathPrefix + "`)",
		"kind":        "Rule",
		"middlewares": []any{map[string]any{"name": uploadCORSName}},
		"services":    []any{service},
	}
	return uploadObject(traefikAPIVersion, "IngressRoute", uploadRouteName, map[string]any{"spec": map[string]any{
		"entryPoints": []any{"websecure"},
		"routes":      []any{route},
		"tls":         map[string]any{},
	}})
}

func uploadObject(apiVersion, kind, name string, fields map[string]any) *unstructured.Unstructured {
	content := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": uploadNamespace,
			"labels":    map[string]any{v1alpha1.LabelKind: kind, v1alpha1.LabelName: name},
		},
	}
	for key, value := range fields {
		content[key] = value
	}
	return &unstructured.Unstructured{Object: content}
}
