package container

import (
	"fmt"
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

const defaultCPUPercent = 80

type RenderInput struct {
	Container    v1alpha1.Container
	Labels       map[string]string
	NIC          network.NIC
	DNSServers   []string
	RuntimeClass string
}

func RenderDeployment(in RenderInput) (*unstructured.Unstructured, error) {
	spec := in.Container.Spec
	attachment := network.PodAttachment(in.NIC)
	deployment := appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            in.Container.Name,
			Namespace:       in.Container.Namespace,
			Labels:          maps.Clone(in.Labels),
			OwnerReferences: []metav1.OwnerReference{OwnerRef(in.Container)},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: replicasOf(spec),
			Selector: &metav1.LabelSelector{MatchLabels: SelectorLabels(in.Container.Name)},
			Strategy: strategyOf(spec),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      mergedStrings(in.Labels, attachment.Labels),
					Annotations: podAnnotations(in.Container, attachment),
				},
				Spec: podSpecOf(in),
			},
		},
	}
	body, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&deployment)
	if err != nil {
		return nil, fmt.Errorf("convert the deployment: %w", err)
	}
	obj := &unstructured.Unstructured{Object: body}
	removeGenerated(obj, "spec", "template", "metadata", "creationTimestamp")
	if err := writeProbes(obj, spec.Probes); err != nil {
		return nil, err
	}
	return obj, nil
}

func RenderAutoscaler(in RenderInput) (*unstructured.Unstructured, error) {
	scale := in.Container.Spec.Autoscale
	if scale == nil {
		return nil, nil
	}
	hpa := autoscalingv2.HorizontalPodAutoscaler{
		TypeMeta: metav1.TypeMeta{APIVersion: "autoscaling/v2", Kind: "HorizontalPodAutoscaler"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            in.Container.Name,
			Namespace:       in.Container.Namespace,
			Labels:          maps.Clone(in.Labels),
			OwnerReferences: []metav1.OwnerReference{OwnerRef(in.Container)},
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Name: in.Container.Name},
			MinReplicas:    ptr.To(scale.Min),
			MaxReplicas:    scale.Max,
			Metrics:        metricsOf(*scale),
		},
	}
	body, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&hpa)
	if err != nil {
		return nil, fmt.Errorf("convert the autoscaler: %w", err)
	}
	obj := &unstructured.Unstructured{Object: body}
	removeGenerated(obj)
	return obj, nil
}

func metricsOf(scale v1alpha1.ContainerAutoscale) []autoscalingv2.MetricSpec {
	var metrics []autoscalingv2.MetricSpec
	if scale.CPUPercent != nil {
		metrics = append(metrics, utilization(corev1.ResourceCPU, *scale.CPUPercent))
	}
	if scale.MemoryPercent != nil {
		metrics = append(metrics, utilization(corev1.ResourceMemory, *scale.MemoryPercent))
	}
	if len(metrics) == 0 {
		metrics = append(metrics, utilization(corev1.ResourceCPU, defaultCPUPercent))
	}
	return metrics
}

func utilization(name corev1.ResourceName, percent int32) autoscalingv2.MetricSpec {
	return autoscalingv2.MetricSpec{
		Type: autoscalingv2.ResourceMetricSourceType,
		Resource: &autoscalingv2.ResourceMetricSource{
			Name:   name,
			Target: autoscalingv2.MetricTarget{Type: autoscalingv2.UtilizationMetricType, AverageUtilization: ptr.To(percent)},
		},
	}
}

func removeGenerated(obj *unstructured.Unstructured, extra ...string) {
	unstructured.RemoveNestedField(obj.Object, "status")
	unstructured.RemoveNestedField(obj.Object, "metadata", "creationTimestamp")
	if len(extra) > 0 {
		unstructured.RemoveNestedField(obj.Object, extra...)
	}
}

func replicasOf(spec v1alpha1.ContainerSpec) *int32 {
	if spec.Autoscale != nil {
		return nil
	}
	if spec.Replicas == nil {
		return ptr.To(int32(1))
	}
	return ptr.To(*spec.Replicas)
}

func strategyOf(spec v1alpha1.ContainerSpec) appsv1.DeploymentStrategy {
	if IsPinned(spec) || len(spec.Volumes) > 0 || spec.Strategy == v1alpha1.ContainerStrategyRecreate {
		return appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType}
	}
	quarter := intstr.FromString("25%")
	return appsv1.DeploymentStrategy{
		Type:          appsv1.RollingUpdateDeploymentStrategyType,
		RollingUpdate: &appsv1.RollingUpdateDeployment{MaxSurge: &quarter, MaxUnavailable: &quarter},
	}
}

func mergedStrings(first, second map[string]string) map[string]string {
	out := maps.Clone(first)
	if out == nil {
		out = map[string]string{}
	}
	maps.Copy(out, second)
	return out
}

func podAnnotations(c v1alpha1.Container, attachment network.PodMetadata) map[string]string {
	annotations := maps.Clone(attachment.Annotations)
	if restarted, found := c.Annotations[v1alpha1.AnnotationRestartedAt]; found {
		annotations[v1alpha1.AnnotationRestartedAt] = restarted
	}
	return annotations
}

func podSpecOf(in RenderInput) corev1.PodSpec {
	spec := in.Container.Spec
	pod := corev1.PodSpec{
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		Containers:                   []corev1.Container{appContainerOf(spec)},
		Volumes:                      volumesOf(spec.Volumes),
		ImagePullSecrets:             pullSecretsOf(spec.ImagePullSecrets),
	}
	if in.RuntimeClass != "" {
		pod.RuntimeClassName = ptr.To(in.RuntimeClass)
	}
	if len(in.DNSServers) > 0 {
		pod.DNSPolicy = corev1.DNSNone
		pod.DNSConfig = &corev1.PodDNSConfig{Nameservers: append([]string(nil), in.DNSServers...)}
	}
	if spec.FSGroup != nil {
		pod.SecurityContext = &corev1.PodSecurityContext{FSGroup: ptr.To(*spec.FSGroup), FSGroupChangePolicy: ptr.To(corev1.FSGroupChangeOnRootMismatch)}
	}
	return pod
}

func appContainerOf(spec v1alpha1.ContainerSpec) corev1.Container {
	return corev1.Container{
		Name:         appContainerName,
		Image:        spec.Image,
		Command:      append([]string(nil), spec.Command...),
		Args:         append([]string(nil), spec.Args...),
		Env:          envOf(spec.Env),
		Ports:        portsOf(spec.Ports),
		Resources:    resourcesOf(spec.Resources),
		VolumeMounts: mountsOf(spec.Volumes),
	}
}

func envOf(env []v1alpha1.ContainerEnvVar) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(env))
	for _, variable := range env {
		entry := corev1.EnvVar{Name: variable.Name, Value: variable.Value}
		if variable.ValueFrom != nil && variable.ValueFrom.SecretKeyRef != nil {
			ref := variable.ValueFrom.SecretKeyRef
			entry.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: ref.Name}, Key: ref.Key}}
		}
		out = append(out, entry)
	}
	return out
}

func portsOf(ports []v1alpha1.ContainerPort) []corev1.ContainerPort {
	out := make([]corev1.ContainerPort, 0, len(ports))
	for _, port := range ports {
		protocol := corev1.Protocol(port.Protocol)
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		out = append(out, corev1.ContainerPort{Name: port.Name, ContainerPort: port.Port, Protocol: protocol})
	}
	return out
}

func resourcesOf(resources v1alpha1.ContainerResources) corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    quantityOr(resources.Requests.CPU, "100m"),
			corev1.ResourceMemory: quantityOr(resources.Requests.Memory, "128Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    quantityOr(resources.Limits.CPU, "500m"),
			corev1.ResourceMemory: quantityOr(resources.Limits.Memory, "512Mi"),
		},
	}
}

func quantityOr(value *resource.Quantity, fallback string) resource.Quantity {
	if value == nil {
		return resource.MustParse(fallback)
	}
	return value.DeepCopy()
}

func volumeName(index int) string {
	return fmt.Sprintf("volume-%d", index)
}

func volumesOf(volumes []v1alpha1.ContainerVolume) []corev1.Volume {
	out := make([]corev1.Volume, 0, len(volumes))
	for index, volume := range volumes {
		out = append(out, corev1.Volume{
			Name:         volumeName(index),
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: volume.VolumeRef}},
		})
	}
	return out
}

func mountsOf(volumes []v1alpha1.ContainerVolume) []corev1.VolumeMount {
	out := make([]corev1.VolumeMount, 0, len(volumes))
	for index, volume := range volumes {
		out = append(out, corev1.VolumeMount{Name: volumeName(index), MountPath: volume.MountPath})
	}
	return out
}

func pullSecretsOf(names []string) []corev1.LocalObjectReference {
	out := make([]corev1.LocalObjectReference, 0, len(names))
	for _, name := range names {
		out = append(out, corev1.LocalObjectReference{Name: name})
	}
	return out
}

func writeProbes(obj *unstructured.Unstructured, probes *v1alpha1.ContainerProbes) error {
	if probes == nil {
		return nil
	}
	containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if err != nil || !found || len(containers) != 1 {
		return fmt.Errorf("the rendered deployment has no container: %v", err)
	}
	app := containers[0].(map[string]any)
	if probes.Liveness != nil {
		app["livenessProbe"] = probeOf(*probes.Liveness)
	}
	if probes.Readiness != nil {
		app["readinessProbe"] = probeOf(*probes.Readiness)
	}
	return unstructured.SetNestedSlice(obj.Object, []any{app}, "spec", "template", "spec", "containers")
}

func probeOf(probe v1alpha1.ContainerProbe) map[string]any {
	var words []string
	if probe.Exec != nil {
		words = probe.Exec.Command
	}
	command := make([]any, len(words))
	for i, word := range words {
		command[i] = word
	}
	return map[string]any{
		"exec":                map[string]any{"command": command},
		"initialDelaySeconds": int64(valueOr(probe.InitialDelaySeconds, 0)),
		"periodSeconds":       int64(valueOr(probe.PeriodSeconds, 10)),
		"timeoutSeconds":      int64(valueOr(probe.TimeoutSeconds, 1)),
		"successThreshold":    int64(valueOr(probe.SuccessThreshold, 1)),
		"failureThreshold":    int64(valueOr(probe.FailureThreshold, 3)),
	}
}

func valueOr(value *int32, fallback int32) int32 {
	if value == nil {
		return fallback
	}
	return *value
}
