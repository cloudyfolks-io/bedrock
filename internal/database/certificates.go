package database

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	DatabaseIssuer      = "bedrock-database-ca"
	certificateLifetime = 5 * 365 * 24 * time.Hour
	certificateRenewal  = 365 * 24 * time.Hour
	CombinedPEMKey      = "tls-combined.pem"
)

type CertSpec struct {
	Name       string
	Namespace  string
	SecretName string
	CommonName string
	DNSNames   []string
	IPs        []string
	Usages     []string
	Labels     map[string]string
	OwnerRefs  []metav1.OwnerReference
}

func anyList(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

func ServerCertUsages() []string {
	return []string{"digital signature", "key encipherment", "server auth"}
}

func ClientCertUsages() []string {
	return []string{"digital signature", "key encipherment", "client auth"}
}

func CertificateObject(s CertSpec) *unstructured.Unstructured {
	spec := map[string]any{
		"secretName":              s.SecretName,
		"commonName":              s.CommonName,
		"usages":                  anyList(s.Usages),
		"duration":                certificateLifetime.String(),
		"renewBefore":             certificateRenewal.String(),
		"privateKey":              map[string]any{"algorithm": "ECDSA", "size": int64(256), "rotationPolicy": "Always"},
		"issuerRef":               map[string]any{"group": "cert-manager.io", "kind": "ClusterIssuer", "name": DatabaseIssuer},
		"additionalOutputFormats": []any{map[string]any{"type": "CombinedPEM"}},
	}
	if len(s.DNSNames) > 0 {
		spec["dnsNames"] = anyList(s.DNSNames)
	}
	if len(s.IPs) > 0 {
		spec["ipAddresses"] = anyList(s.IPs)
	}
	metadata := map[string]any{"name": s.Name, "namespace": s.Namespace}
	if len(s.Labels) > 0 {
		labels := map[string]any{}
		for key, value := range s.Labels {
			labels[key] = value
		}
		metadata["labels"] = labels
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "Certificate", "metadata": metadata, "spec": spec,
	}}
	obj.SetOwnerReferences(s.OwnerRefs)
	return obj
}

func CertificateRevision(obj *unstructured.Unstructured) int64 {
	revision, _, _ := unstructured.NestedInt64(obj.Object, "status", "revision")
	return revision
}
