package database

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const annotationConfigHash = "bedrock.cloudyfolks.io/config-hash"

func GateName(uid types.UID) string       { return Suffixed(uid, "gate") }
func ServerCertName(uid types.UID) string { return Suffixed(uid, "server") }
func ServerSecretName(uid types.UID) string {
	return Suffixed(uid, "server-tls")
}
func VIPName(uid types.UID) string { return Suffixed(uid, "vip") }

func BackendHost(uid types.UID) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", Suffixed(uid, "db"), PlatformNamespace)
}

func toUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	body, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	out := &unstructured.Unstructured{Object: body}
	unstructured.RemoveNestedField(out.Object, "status")
	unstructured.RemoveNestedField(out.Object, "metadata", "creationTimestamp")
	return out, nil
}

func configHash(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		digest.Write([]byte(part))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))[:16]
}

type GateInput struct {
	Database v1alpha1.Database
	Project  Project
	Image    string
	Port     int
	Revision int64
}

func gateSelector(uid types.UID) map[string]string {
	return map[string]string{LabelInstance: Name(uid), LabelComponent: ComponentGate}
}

func GateObjects(in GateInput) ([]*unstructured.Unstructured, error) {
	db := in.Database
	config, err := GateConfig(GateSpec{ListenPort: in.Port, BackendHost: BackendHost(db.UID), BackendPort: in.Port, ClientCN: Name(db.UID)})
	if err != nil {
		return nil, err
	}
	labels, _ := OwnedMetadata(db, in.Project)
	configMap, err := toUnstructured(&corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: GateName(db.UID), Namespace: PlatformNamespace},
		Data:       map[string]string{ConfigFile: config},
	})
	if err != nil {
		return nil, err
	}
	certificate := CertificateObject(CertSpec{
		Name: ServerCertName(db.UID), Namespace: PlatformNamespace, SecretName: ServerSecretName(db.UID), CommonName: Name(db.UID),
		DNSNames: []string{ServerName(Name(db.UID)), "localhost"}, IPs: []string{"127.0.0.1"}, Usages: ServerCertUsages(),
	})
	podLabels := map[string]string{LabelComponent: ComponentGate}
	for key, value := range labels {
		podLabels[key] = value
	}
	deployment, err := deploymentOf(proxyDeployment{
		Name: GateName(db.UID), Namespace: PlatformNamespace, Labels: labels, PodLabels: podLabels, Selector: gateSelector(db.UID),
		PodAnnotations: map[string]string{annotationConfigHash: configHash(config, strconv.FormatInt(in.Revision, 10))},
		Image:          in.Image, Port: in.Port, Suspended: db.Spec.Suspended,
		Volume: projectedCerts(ServerSecretName(db.UID), CombinedPEMKey, CombinedPEMFile, GateName(db.UID)),
	})
	if err != nil {
		return nil, err
	}
	policy, err := EnginePolicyObject(db)
	if err != nil {
		return nil, err
	}
	objects := []*unstructured.Unstructured{configMap, certificate, deployment, policy}
	for i := range objects {
		objects[i] = Adopt(objects[i], db, in.Project)
	}
	return objects, nil
}

func EnginePolicyObject(db v1alpha1.Database) (*unstructured.Unstructured, error) {
	uid := Name(db.UID)
	return toUnstructured(&networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: uid, Namespace: PlatformNamespace},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{LabelInstance: uid, LabelComponent: ComponentEngine}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{
				PodSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{LabelInstance: uid},
					MatchExpressions: []metav1.LabelSelectorRequirement{{
						Key: LabelComponent, Operator: metav1.LabelSelectorOpIn, Values: []string{ComponentGate, ComponentEngine},
					}},
				},
			}}}},
		},
	})
}
