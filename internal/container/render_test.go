package container

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

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

func baseInput() RenderInput {
	return RenderInput{
		Container: v1alpha1.Container{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "t1-prod", UID: "uid-1"},
			Spec: v1alpha1.ContainerSpec{
				Image:   "registry.test/web:1",
				Network: v1alpha1.ContainerNetwork{SubnetRef: "default"},
			},
		},
		Labels:       v1alpha1.TenantLabels("Container", "web", "t1", "prod"),
		NIC:          network.NIC{Namespace: "t1-prod", Subnet: "default"},
		RuntimeClass: "kata",
	}
}

func withSpec(change func(*v1alpha1.ContainerSpec)) RenderInput {
	in := baseInput()
	change(&in.Container.Spec)
	return in
}

func mustDeployment(t *testing.T, in RenderInput) (appsv1.Deployment, *unstructured.Unstructured) {
	t.Helper()
	obj, err := RenderDeployment(in)
	if err != nil {
		t.Fatal(err)
	}
	var deployment appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructuredWithValidation(obj.Object, &deployment, true); err != nil {
		t.Fatal(err)
	}
	return deployment, obj
}

func appContainer(t *testing.T, deployment appsv1.Deployment) corev1.Container {
	t.Helper()
	containers := deployment.Spec.Template.Spec.Containers
	if len(containers) != 1 || containers[0].Name != "app" {
		t.Fatalf("containers %+v", containers)
	}
	return containers[0]
}

func TestRenderDeploymentIdentity(t *testing.T) {
	deployment, obj := mustDeployment(t, baseInput())
	if obj.GetAPIVersion() != "apps/v1" || obj.GetKind() != "Deployment" || deployment.Name != "web" || deployment.Namespace != "t1-prod" {
		t.Fatalf("identity %s %s %s/%s", obj.GetAPIVersion(), obj.GetKind(), deployment.Namespace, deployment.Name)
	}
	want := v1alpha1.TenantLabels("Container", "web", "t1", "prod")
	if !reflect.DeepEqual(deployment.Labels, want) {
		t.Fatalf("labels %v", deployment.Labels)
	}
	selector := map[string]string{v1alpha1.LabelKind: "Container", v1alpha1.LabelName: "web"}
	if !reflect.DeepEqual(deployment.Spec.Selector.MatchLabels, selector) {
		t.Fatalf("selector %v", deployment.Spec.Selector.MatchLabels)
	}
	for key, value := range want {
		if deployment.Spec.Template.Labels[key] != value {
			t.Fatalf("template label %s = %q", key, deployment.Spec.Template.Labels[key])
		}
	}
	owners := deployment.OwnerReferences
	if len(owners) != 1 || owners[0].Kind != "Container" || owners[0].Name != "web" || owners[0].UID != "uid-1" || owners[0].Controller == nil || !*owners[0].Controller {
		t.Fatalf("owners %+v", owners)
	}
}

func TestRenderDeploymentHasOneContainerNamedApp(t *testing.T) {
	in := withSpec(func(s *v1alpha1.ContainerSpec) {
		s.Command = []string{"/bin/web"}
		s.Args = []string{"--port", "8080"}
		s.Ports = []v1alpha1.ContainerPort{{Name: "http", Port: 8080, Protocol: "TCP"}, {Port: 53, Protocol: "UDP"}, {Port: 9000}}
	})
	deployment, _ := mustDeployment(t, in)
	app := appContainer(t, deployment)
	if app.Image != "registry.test/web:1" || !reflect.DeepEqual(app.Command, []string{"/bin/web"}) || !reflect.DeepEqual(app.Args, []string{"--port", "8080"}) {
		t.Fatalf("container %+v", app)
	}
	want := []corev1.ContainerPort{
		{Name: "http", ContainerPort: 8080, Protocol: corev1.ProtocolTCP},
		{ContainerPort: 53, Protocol: corev1.ProtocolUDP},
		{ContainerPort: 9000, Protocol: corev1.ProtocolTCP},
	}
	if !reflect.DeepEqual(app.Ports, want) {
		t.Fatalf("ports %+v", app.Ports)
	}
}

func TestRenderDeploymentTurnsOffTheServiceAccountTokenAndServiceLinks(t *testing.T) {
	deployment, _ := mustDeployment(t, baseInput())
	spec := deployment.Spec.Template.Spec
	if spec.AutomountServiceAccountToken == nil || *spec.AutomountServiceAccountToken {
		t.Fatalf("automount %v", spec.AutomountServiceAccountToken)
	}
	if spec.EnableServiceLinks == nil || *spec.EnableServiceLinks {
		t.Fatalf("service links %v", spec.EnableServiceLinks)
	}
}

func TestRenderDeploymentRuntimeClass(t *testing.T) {
	deployment, _ := mustDeployment(t, baseInput())
	if got := deployment.Spec.Template.Spec.RuntimeClassName; got == nil || *got != "kata" {
		t.Fatalf("runtime class %v", got)
	}
	in := baseInput()
	in.RuntimeClass = ""
	deployment, _ = mustDeployment(t, in)
	if deployment.Spec.Template.Spec.RuntimeClassName != nil {
		t.Fatalf("an empty input sets no runtime class: %v", *deployment.Spec.Template.Spec.RuntimeClassName)
	}
}

func TestRenderDeploymentDNS(t *testing.T) {
	in := baseInput()
	in.DNSServers = []string{"10.0.0.2", "10.0.0.3"}
	deployment, _ := mustDeployment(t, in)
	spec := deployment.Spec.Template.Spec
	if spec.DNSPolicy != corev1.DNSNone || spec.DNSConfig == nil || !reflect.DeepEqual(spec.DNSConfig.Nameservers, in.DNSServers) {
		t.Fatalf("dns %v %+v", spec.DNSPolicy, spec.DNSConfig)
	}
	deployment, _ = mustDeployment(t, baseInput())
	spec = deployment.Spec.Template.Spec
	if spec.DNSPolicy != "" || spec.DNSConfig != nil {
		t.Fatalf("no servers leave dns unset: %v %+v", spec.DNSPolicy, spec.DNSConfig)
	}
}

func TestRenderDeploymentCarriesThePodAttachment(t *testing.T) {
	in := baseInput()
	in.NIC = network.NIC{Namespace: "t1-prod", Subnet: "default", Address: "10.0.0.9", SecurityGroups: []string{"web"}}
	deployment, _ := mustDeployment(t, in)
	attachment := network.PodAttachment(in.NIC)
	template := deployment.Spec.Template
	for key, value := range attachment.Annotations {
		if template.Annotations[key] != value {
			t.Fatalf("annotation %s = %q, want %q", key, template.Annotations[key], value)
		}
	}
	for key, value := range attachment.Labels {
		if template.Labels[key] != value {
			t.Fatalf("label %s = %q, want %q", key, template.Labels[key], value)
		}
	}
	if template.Annotations["v1.multus-cni.io/default-network"] != "t1-prod/default" {
		t.Fatalf("annotations %v", template.Annotations)
	}
}

func TestRenderDeploymentStrategy(t *testing.T) {
	cases := []struct {
		name string
		in   RenderInput
		want appsv1.DeploymentStrategyType
	}{
		{"plain", baseInput(), appsv1.RollingUpdateDeploymentStrategyType},
		{"pinned", withSpec(func(s *v1alpha1.ContainerSpec) { s.Network.Address = "10.0.0.9" }), appsv1.RecreateDeploymentStrategyType},
		{"volumes", withSpec(func(s *v1alpha1.ContainerSpec) {
			s.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/data"}}
		}), appsv1.RecreateDeploymentStrategyType},
		{"chosen", withSpec(func(s *v1alpha1.ContainerSpec) { s.Strategy = v1alpha1.ContainerStrategyRecreate }), appsv1.RecreateDeploymentStrategyType},
		{"chosen-rolling", withSpec(func(s *v1alpha1.ContainerSpec) { s.Strategy = v1alpha1.ContainerStrategyRollingUpdate }), appsv1.RollingUpdateDeploymentStrategyType},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deployment, _ := mustDeployment(t, tc.in)
			strategy := deployment.Spec.Strategy
			if strategy.Type != tc.want {
				t.Fatalf("strategy %v, want %v", strategy.Type, tc.want)
			}
			if tc.want == appsv1.RecreateDeploymentStrategyType && strategy.RollingUpdate != nil {
				t.Fatalf("recreate carries rolling update %+v", strategy.RollingUpdate)
			}
			if tc.want == appsv1.RollingUpdateDeploymentStrategyType {
				quarter := intstr.FromString("25%")
				if strategy.RollingUpdate == nil || *strategy.RollingUpdate.MaxSurge != quarter || *strategy.RollingUpdate.MaxUnavailable != quarter {
					t.Fatalf("rolling update %+v", strategy.RollingUpdate)
				}
			}
		})
	}
}

func TestRenderDeploymentPinnedRefusesRollingUpdateEvenWhenChosen(t *testing.T) {
	in := withSpec(func(s *v1alpha1.ContainerSpec) {
		s.Network.Address = "10.0.0.9"
		s.Strategy = v1alpha1.ContainerStrategyRollingUpdate
	})
	deployment, _ := mustDeployment(t, in)
	if deployment.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Fatalf("a pinned container always uses Recreate: %v", deployment.Spec.Strategy.Type)
	}
}

func TestRenderDeploymentReplicas(t *testing.T) {
	cases := []struct {
		name string
		in   RenderInput
		want *int32
	}{
		{"default", baseInput(), ptr.To(int32(1))},
		{"three", withSpec(func(s *v1alpha1.ContainerSpec) { s.Replicas = ptr.To(int32(3)) }), ptr.To(int32(3))},
		{"stopped", withSpec(func(s *v1alpha1.ContainerSpec) { s.Replicas = ptr.To(int32(0)) }), ptr.To(int32(0))},
		{"autoscaled", withSpec(func(s *v1alpha1.ContainerSpec) { s.Autoscale = &v1alpha1.ContainerAutoscale{Min: 1, Max: 4} }), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deployment, obj := mustDeployment(t, tc.in)
			if !reflect.DeepEqual(deployment.Spec.Replicas, tc.want) {
				t.Fatalf("replicas %v, want %v", deployment.Spec.Replicas, tc.want)
			}
			_, present, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", "replicas")
			if present != (tc.want != nil) {
				t.Fatalf("replicas present %v", present)
			}
		})
	}
}

func TestRenderDeploymentCopiesTheRestartAnnotation(t *testing.T) {
	in := baseInput()
	in.Container.Annotations = map[string]string{v1alpha1.AnnotationRestartedAt: "2026-10-19T10:00:00Z", "other": "x"}
	deployment, _ := mustDeployment(t, in)
	template := deployment.Spec.Template.Annotations
	if template[v1alpha1.AnnotationRestartedAt] != "2026-10-19T10:00:00Z" {
		t.Fatalf("annotations %v", template)
	}
	if _, found := template["other"]; found {
		t.Fatalf("only the restart annotation is copied: %v", template)
	}
	deployment, _ = mustDeployment(t, baseInput())
	if _, found := deployment.Spec.Template.Annotations[v1alpha1.AnnotationRestartedAt]; found {
		t.Fatal("no restart annotation, no copy")
	}
}

func TestRenderDeploymentResources(t *testing.T) {
	deployment, _ := mustDeployment(t, baseInput())
	app := appContainer(t, deployment)
	got := map[string]string{
		"requests.cpu":    app.Resources.Requests.Cpu().String(),
		"requests.memory": app.Resources.Requests.Memory().String(),
		"limits.cpu":      app.Resources.Limits.Cpu().String(),
		"limits.memory":   app.Resources.Limits.Memory().String(),
	}
	want := map[string]string{"requests.cpu": "100m", "requests.memory": "128Mi", "limits.cpu": "500m", "limits.memory": "512Mi"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resources %v", got)
	}
	custom := withSpec(func(s *v1alpha1.ContainerSpec) {
		s.Resources.Requests.CPU = ptr.To(resource.MustParse("1"))
		s.Resources.Limits.CPU = ptr.To(resource.MustParse("2"))
		s.Resources.Limits.Memory = ptr.To(resource.MustParse("2Gi"))
	})
	deployment, _ = mustDeployment(t, custom)
	app = appContainer(t, deployment)
	if app.Resources.Requests.Cpu().String() != "1" || app.Resources.Limits.Cpu().String() != "2" || app.Resources.Limits.Memory().String() != "2Gi" || app.Resources.Requests.Memory().String() != "128Mi" {
		t.Fatalf("resources %+v", app.Resources)
	}
}

func TestRenderDeploymentFSGroup(t *testing.T) {
	deployment, _ := mustDeployment(t, withSpec(func(s *v1alpha1.ContainerSpec) { s.FSGroup = ptr.To(int64(1000)) }))
	context := deployment.Spec.Template.Spec.SecurityContext
	if context == nil || context.FSGroup == nil || *context.FSGroup != 1000 || context.FSGroupChangePolicy == nil || *context.FSGroupChangePolicy != corev1.FSGroupChangeOnRootMismatch {
		t.Fatalf("security context %+v", context)
	}
	deployment, _ = mustDeployment(t, baseInput())
	if deployment.Spec.Template.Spec.SecurityContext != nil && deployment.Spec.Template.Spec.SecurityContext.FSGroup != nil {
		t.Fatal("no fsGroup, no security context group")
	}
}

func TestRenderDeploymentEnvironment(t *testing.T) {
	in := withSpec(func(s *v1alpha1.ContainerSpec) {
		s.Env = []v1alpha1.ContainerEnvVar{
			{Name: "MODE", Value: "prod"},
			{Name: "TOKEN", ValueFrom: &v1alpha1.ContainerEnvSource{SecretKeyRef: &v1alpha1.ContainerSecretKeyRef{Name: "creds", Key: "token"}}},
		}
	})
	deployment, _ := mustDeployment(t, in)
	want := []corev1.EnvVar{
		{Name: "MODE", Value: "prod"},
		{Name: "TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "creds"}, Key: "token"}}},
	}
	if got := appContainer(t, deployment).Env; !reflect.DeepEqual(got, want) {
		t.Fatalf("env %+v", got)
	}
}

func execProbe(command ...string) *corev1.Probe {
	return &corev1.Probe{
		ProbeHandler:        corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: command}},
		InitialDelaySeconds: 0, PeriodSeconds: 10, TimeoutSeconds: 1, SuccessThreshold: 1, FailureThreshold: 3,
	}
}

func TestRenderDeploymentExecProbesWriteKubernetesDefaultsInFull(t *testing.T) {
	in := withSpec(func(s *v1alpha1.ContainerSpec) {
		s.Probes = &v1alpha1.ContainerProbes{
			Liveness:  &v1alpha1.ContainerProbe{Exec: &v1alpha1.ContainerExec{Command: []string{"live"}}},
			Readiness: &v1alpha1.ContainerProbe{Exec: &v1alpha1.ContainerExec{Command: []string{"ready"}}, PeriodSeconds: ptr.To(int32(5)), SuccessThreshold: ptr.To(int32(2))},
		}
	})
	deployment, obj := mustDeployment(t, in)
	app := appContainer(t, deployment)
	if !reflect.DeepEqual(app.LivenessProbe, execProbe("live")) {
		t.Fatalf("liveness %+v", app.LivenessProbe)
	}
	readiness := execProbe("ready")
	readiness.PeriodSeconds, readiness.SuccessThreshold = 5, 2
	if !reflect.DeepEqual(app.ReadinessProbe, readiness) {
		t.Fatalf("readiness %+v", app.ReadinessProbe)
	}
	raw, _ := json.Marshal(obj.Object)
	for _, key := range []string{`"initialDelaySeconds":0`, `"timeoutSeconds":1`, `"failureThreshold":3`, `"periodSeconds":10`} {
		if !strings.Contains(string(raw), key) {
			t.Fatalf("the rendered probe must hold %s in full: %s", key, raw)
		}
	}
	deployment, _ = mustDeployment(t, baseInput())
	app = appContainer(t, deployment)
	if app.LivenessProbe != nil || app.ReadinessProbe != nil {
		t.Fatal("no probes, no probe")
	}
}

func TestRenderDeploymentPullSecretsAndVolumes(t *testing.T) {
	in := withSpec(func(s *v1alpha1.ContainerSpec) {
		s.ImagePullSecrets = []string{"registry-credentials"}
		s.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/data"}, {VolumeRef: "cache.v2", MountPath: "/cache"}}
	})
	deployment, _ := mustDeployment(t, in)
	spec := deployment.Spec.Template.Spec
	if !reflect.DeepEqual(spec.ImagePullSecrets, []corev1.LocalObjectReference{{Name: "registry-credentials"}}) {
		t.Fatalf("pull secrets %+v", spec.ImagePullSecrets)
	}
	if len(spec.Volumes) != 2 || spec.Volumes[0].PersistentVolumeClaim == nil || spec.Volumes[0].PersistentVolumeClaim.ClaimName != "data" || spec.Volumes[1].PersistentVolumeClaim.ClaimName != "cache.v2" {
		t.Fatalf("volumes %+v", spec.Volumes)
	}
	mounts := appContainer(t, deployment).VolumeMounts
	if len(mounts) != 2 || mounts[0].MountPath != "/data" || mounts[1].MountPath != "/cache" || mounts[0].Name != spec.Volumes[0].Name || mounts[1].Name != spec.Volumes[1].Name {
		t.Fatalf("mounts %+v", mounts)
	}
	if spec.Volumes[0].Name == spec.Volumes[1].Name {
		t.Fatal("volume names must differ")
	}
}

var forbiddenPodKeys = []string{"hostNetwork", "hostPID", "hostIPC", "hostPath", "privileged", "nodeName", "hostPort", "procMount", "hostUsers", "shareProcessNamespace"}

func collectKeys(value any, found map[string]bool) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			found[key] = true
			collectKeys(nested, found)
		}
	case []any:
		for _, nested := range typed {
			collectKeys(nested, found)
		}
	}
}

func fullInput() RenderInput {
	in := withSpec(func(s *v1alpha1.ContainerSpec) {
		s.Command = []string{"/bin/web"}
		s.Args = []string{"a"}
		s.Env = []v1alpha1.ContainerEnvVar{{Name: "A", Value: "1"}}
		s.Ports = []v1alpha1.ContainerPort{{Port: 80}}
		s.FSGroup = ptr.To(int64(10))
		s.Probes = &v1alpha1.ContainerProbes{Liveness: &v1alpha1.ContainerProbe{Exec: &v1alpha1.ContainerExec{Command: []string{"x"}}}}
		s.Volumes = []v1alpha1.ContainerVolume{{VolumeRef: "data", MountPath: "/data"}}
		s.ImagePullSecrets = []string{"pull"}
		s.Network.Address = "10.0.0.9"
	})
	in.DNSServers = []string{"10.0.0.2"}
	in.NIC = network.NIC{Namespace: "t1-prod", Subnet: "default", Address: "10.0.0.9", SecurityGroups: []string{"web"}}
	return in
}

func TestRenderedPodNeverCarriesAHostField(t *testing.T) {
	_, obj := mustDeployment(t, fullInput())
	found := map[string]bool{}
	collectKeys(obj.Object, found)
	for _, key := range forbiddenPodKeys {
		if found[key] {
			t.Errorf("the pod must not carry %s", key)
		}
	}
	if found["capabilities"] {
		t.Error("the pod must not add capabilities")
	}
}

func TestRenderDeploymentDoesNotChangeItsInput(t *testing.T) {
	in := fullInput()
	in.Container.Annotations = map[string]string{v1alpha1.AnnotationRestartedAt: "now"}
	before, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	first, err := RenderDeployment(in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderDeployment(in)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("render changed its input")
	}
	if !reflect.DeepEqual(first.Object, second.Object) {
		t.Fatal("render is not deterministic")
	}
}

func mustAutoscaler(t *testing.T, in RenderInput) *autoscalingv2.HorizontalPodAutoscaler {
	t.Helper()
	obj, err := RenderAutoscaler(in)
	if err != nil {
		t.Fatal(err)
	}
	if obj == nil {
		return nil
	}
	var hpa autoscalingv2.HorizontalPodAutoscaler
	if err := runtime.DefaultUnstructuredConverter.FromUnstructuredWithValidation(obj.Object, &hpa, true); err != nil {
		t.Fatal(err)
	}
	return &hpa
}

func TestRenderAutoscalerIsAbsentWithoutAutoscale(t *testing.T) {
	if hpa := mustAutoscaler(t, baseInput()); hpa != nil {
		t.Fatalf("autoscaler %+v", hpa)
	}
}

func TestRenderAutoscalerDefaultsToEightyPercentCPU(t *testing.T) {
	hpa := mustAutoscaler(t, withSpec(func(s *v1alpha1.ContainerSpec) { s.Autoscale = &v1alpha1.ContainerAutoscale{Min: 2, Max: 6} }))
	if hpa.Name != "web" || hpa.Namespace != "t1-prod" || !reflect.DeepEqual(hpa.Labels, v1alpha1.TenantLabels("Container", "web", "t1", "prod")) {
		t.Fatalf("meta %+v", hpa.ObjectMeta)
	}
	if hpa.Spec.ScaleTargetRef.Kind != "Deployment" || hpa.Spec.ScaleTargetRef.Name != "web" || hpa.Spec.ScaleTargetRef.APIVersion != "apps/v1" {
		t.Fatalf("target %+v", hpa.Spec.ScaleTargetRef)
	}
	if hpa.Spec.MinReplicas == nil || *hpa.Spec.MinReplicas != 2 || hpa.Spec.MaxReplicas != 6 {
		t.Fatalf("bounds %v %v", hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas)
	}
	if len(hpa.Spec.Metrics) != 1 || hpa.Spec.Metrics[0].Resource == nil || hpa.Spec.Metrics[0].Resource.Name != corev1.ResourceCPU || *hpa.Spec.Metrics[0].Resource.Target.AverageUtilization != 80 {
		t.Fatalf("metrics %+v", hpa.Spec.Metrics)
	}
	owners := hpa.OwnerReferences
	if len(owners) != 1 || owners[0].Kind != "Container" || owners[0].Controller == nil || !*owners[0].Controller {
		t.Fatalf("owners %+v", owners)
	}
}

func TestRenderAutoscalerUsesBothTargets(t *testing.T) {
	hpa := mustAutoscaler(t, withSpec(func(s *v1alpha1.ContainerSpec) {
		s.Autoscale = &v1alpha1.ContainerAutoscale{Min: 1, Max: 3, CPUPercent: ptr.To(int32(60)), MemoryPercent: ptr.To(int32(70))}
	}))
	got := map[corev1.ResourceName]int32{}
	for _, metric := range hpa.Spec.Metrics {
		got[metric.Resource.Name] = *metric.Resource.Target.AverageUtilization
	}
	if !reflect.DeepEqual(got, map[corev1.ResourceName]int32{corev1.ResourceCPU: 60, corev1.ResourceMemory: 70}) {
		t.Fatalf("metrics %v", got)
	}
	memoryOnly := mustAutoscaler(t, withSpec(func(s *v1alpha1.ContainerSpec) {
		s.Autoscale = &v1alpha1.ContainerAutoscale{Min: 1, Max: 3, MemoryPercent: ptr.To(int32(70))}
	}))
	if len(memoryOnly.Spec.Metrics) != 1 || memoryOnly.Spec.Metrics[0].Resource.Name != corev1.ResourceMemory {
		t.Fatalf("a memory target alone has no cpu default: %+v", memoryOnly.Spec.Metrics)
	}
}
