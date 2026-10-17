package operator

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	rookMonSecretName = "rook-ceph-mon"
	rookFSIDKey       = "fsid"
)

func adoptPhase(cluster v1alpha1.Cluster) string {
	if cluster.Spec.Storage.Adopt == nil {
		return ""
	}
	condition := meta.FindStatusCondition(cluster.Status.Conditions, v1alpha1.ConditionStorageAdopted)
	if condition == nil {
		return ""
	}
	return condition.Reason
}

func adoptedMonSecret(ctx context.Context, c client.Reader, cluster v1alpha1.Cluster) (*corev1.Secret, error) {
	if cluster.Spec.Storage.Adopt == nil {
		return nil, nil
	}
	return optionalSecret(ctx, c, client.ObjectKey{Namespace: storageNamespace, Name: rookMonSecretName})
}

func renderAdopted(in AddonInput, adopt v1alpha1.AdoptSpec) (Rendered, error) {
	hosts := osdHosts(in.Hosts)
	phase := adoptPhase(in.Cluster)
	if phase != v1alpha1.ReasonAdopted {
		if reason, message := adoptWait(in.CephMonSecret, adopt, hosts); reason != "" {
			return Rendered{SkipReason: reason, SkipMessage: message, Conditions: []metav1.Condition{adoptedCondition(reason, message)}}, nil
		}
	}
	if len(hosts) == 0 {
		return noDevices(), nil
	}
	image, replicas, err := storageSettings(in)
	if err != nil {
		return Rendered{}, err
	}
	cluster, err := adoptedCephCluster(image, hosts, adopt, replicas)
	if err != nil {
		return Rendered{}, err
	}
	switch phase {
	case v1alpha1.ReasonAdopted:
		objects := slices.Concat([]*unstructured.Unstructured{cluster}, storageObjects(replicas, hosts))
		return Rendered{Objects: objects, Probes: []Probe{cephClusterProbe(cephGate(deviceCount(hosts)))}}, nil
	case "", v1alpha1.ReasonWaitingForIdentity, v1alpha1.ReasonWaitingForHosts:
		quorum := adoptedCondition(v1alpha1.ReasonWaitingForQuorum, "the CephCluster is rendered and waits for every monitor to join the quorum")
		return adoptionPending(cluster, v1alpha1.ReasonWaitingForQuorum, quorum), nil
	default:
		return adoptionPending(cluster, phase), nil
	}
}

func adoptionPending(cluster *unstructured.Unstructured, phase string, conditions ...metav1.Condition) Rendered {
	gate := func(*unstructured.Unstructured) release.Readiness {
		return release.Readiness{Reason: phase, Message: fmt.Sprintf("adoption is at %s", phase)}
	}
	return Rendered{Objects: []*unstructured.Unstructured{cluster}, Probes: []Probe{cephClusterProbe(gate)}, Conditions: conditions}
}

func adoptedCondition(reason, message string) metav1.Condition {
	return metav1.Condition{Type: v1alpha1.ConditionStorageAdopted, Status: metav1.ConditionFalse, Reason: reason, Message: message}
}

func adoptWait(secret *corev1.Secret, adopt v1alpha1.AdoptSpec, hosts []v1alpha1.Host) (string, string) {
	if message := identityProblem(secret, adopt.FSID); message != "" {
		return v1alpha1.ReasonWaitingForIdentity, message
	}
	if missing := monitorsWithoutHost(adopt.Monitors, hosts); len(missing) > 0 {
		return v1alpha1.ReasonWaitingForHosts, "monitor hosts without the ceph-osd role and devices: " + strings.Join(missing, ", ")
	}
	return "", ""
}

func identityProblem(secret *corev1.Secret, fsid string) string {
	switch {
	case secret == nil:
		return fmt.Sprintf("Secret %s/%s does not exist", storageNamespace, rookMonSecretName)
	case string(secret.Data[rookFSIDKey]) != fsid:
		return fmt.Sprintf("Secret %s/%s holds another fsid than storage.adopt.fsid", storageNamespace, rookMonSecretName)
	}
	return ""
}

func monitorsWithoutHost(monitors []v1alpha1.AdoptMonitor, hosts []v1alpha1.Host) []string {
	var missing []string
	for _, monitor := range monitors {
		if !slices.ContainsFunc(hosts, func(host v1alpha1.Host) bool { return host.Name == monitor.Node }) {
			missing = append(missing, monitor.Node)
		}
	}
	return missing
}

func adoptedCephCluster(image string, hosts []v1alpha1.Host, adopt v1alpha1.AdoptSpec, replicas int) (*unstructured.Unstructured, error) {
	mons := int64(len(adopt.Monitors))
	spec := cephClusterSpec(image, hosts, mons, replicas)
	spec["network"] = map[string]any{"provider": "host"}
	spec["mon"] = map[string]any{"count": mons, "allowMultiplePerNode": false}
	spec["healthCheck"] = map[string]any{"muteHealthWarning": adoptedHealthWarnings(replicas)}
	if adopt.Cephx != nil && len(adopt.Cephx.Raw) > 0 {
		var cephx map[string]any
		if err := utiljson.Unmarshal(adopt.Cephx.Raw, &cephx); err != nil {
			return nil, fmt.Errorf("storage.adopt.cephx: %w", err)
		}
		spec["security"] = map[string]any{"cephx": cephx}
	}
	return object("ceph.rook.io/v1", "CephCluster", storageNamespace, storageNamespace, spec), nil
}

func adoptedHealthWarnings(replicas int) map[string]any {
	muted := mutedHealthWarnings(replicas)
	muted["AUTH_EMERGENCY_CIPHERS_SET"] = map[string]any{"policy": "mute"}
	return muted
}
