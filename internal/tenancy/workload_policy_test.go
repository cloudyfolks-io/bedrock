package tenancy

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const pauseImage = "registry.k8s.io/pause:3.10"
const kubevirtHandler = "system:serviceaccount:kubevirt:kubevirt-kubevirtHandler"

func editorIn(namespace, user string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "editor-" + strings.ReplaceAll(user, ":", "-"), Namespace: namespace},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "bedrock-project-editor"},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: user}},
	}
}

func podSpecWith(change func(*corev1.PodSpec)) corev1.PodSpec {
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: pauseImage}}}
	change(&spec)
	return spec
}

func plainPod(name, namespace string, change func(*corev1.PodSpec)) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}, Spec: podSpecWith(change)}
}

func deploymentWith(name, namespace string, change func(*corev1.PodSpec)) *appsv1.Deployment {
	labels := map[string]string{"app": name}
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: labels},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: podSpecWith(change)},
		},
	}
}

func keep(*corev1.PodSpec) {}

type workloadWorld struct {
	admin, eddie, kubevirt client.Client
	cfg                    *rest.Config
}

func newWorkloadWorld(t *testing.T) workloadWorld {
	t.Helper()
	admin, cfg := startEnv(t)
	applyFile(t, admin, manifestPath("86-tenancy", "10-project-roles.yaml"))
	applyFile(t, admin, manifestPath("86-tenancy", "40-workload-policies.yaml"))
	mustCreate(t, admin,
		namespaceNamed("acme-shop", map[string]string{"bedrock.cloudyfolks.io/tenant": "acme"}),
		namespaceNamed("plain", nil),
		editorIn("acme-shop", "bedrock:eddie"), editorIn("plain", "bedrock:eddie"), editorIn("acme-shop", kubevirtHandler),
	)
	world := workloadWorld{cfg: cfg, admin: admin, eddie: actingAs(t, cfg, "bedrock:eddie"), kubevirt: actingAs(t, cfg, kubevirtHandler)}
	probe := func() error {
		return denyCreate(admin, world.eddie, plainPod("probe", "acme-shop", func(s *corev1.PodSpec) { s.HostNetwork = true }))()
	}
	if err := eventuallyForbidden(t, probe); err == nil {
		t.Fatal("the workload policy never denied")
	}
	return world
}

func requireDenied(t *testing.T, err error, message string) {
	t.Helper()
	if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), message) {
		t.Fatalf("err = %v, want forbidden with %q", err, message)
	}
}

func TestEditorPlainPodAndDeploymentPass(t *testing.T) {
	w := newWorkloadWorld(t)
	mustCreate(t, w.eddie, plainPod("plain-pod", "acme-shop", keep), deploymentWith("plain-deploy", "acme-shop", keep))
}

func TestEditorPodWithHostPathIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	for _, path := range []string{"/", "/var/run"} {
		pod := plainPod("hostpath", "acme-shop", func(s *corev1.PodSpec) {
			s.Volumes = []corev1.Volume{{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: path}}}}
		})
		requireDenied(t, w.eddie.Create(context.Background(), pod), "hostPath volumes are not allowed")
	}
}

func TestEditorPrivilegedPodIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	privileged := func(value bool) func(*corev1.PodSpec) {
		return func(s *corev1.PodSpec) {
			s.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr.To(value)}
		}
	}
	requireDenied(t, w.eddie.Create(context.Background(), plainPod("priv", "acme-shop", privileged(true))), "privileged containers are not allowed")
	mustCreate(t, w.eddie, plainPod("unpriv", "acme-shop", privileged(false)))
}

func TestEditorInitContainerPrivilegedIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	pod := plainPod("init-priv", "acme-shop", func(s *corev1.PodSpec) {
		s.InitContainers = []corev1.Container{{Name: "init", Image: pauseImage, SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true)}}}
	})
	requireDenied(t, w.eddie.Create(context.Background(), pod), "privileged containers are not allowed")
}

func TestEditorPodWithHostNamespacesIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	changes := map[string]func(*corev1.PodSpec){
		"hostNetwork": func(s *corev1.PodSpec) { s.HostNetwork = true },
		"hostPID":     func(s *corev1.PodSpec) { s.HostPID = true },
		"hostIPC":     func(s *corev1.PodSpec) { s.HostIPC = true },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			requireDenied(t, w.eddie.Create(context.Background(), plainPod(strings.ToLower(name), "acme-shop", change)), "host namespaces are not allowed")
		})
	}
}

func TestEditorPodWithHostPortIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	port := func(host int32) func(*corev1.PodSpec) {
		return func(s *corev1.PodSpec) {
			s.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: 80, HostPort: host}}
		}
	}
	requireDenied(t, w.eddie.Create(context.Background(), plainPod("hostport", "acme-shop", port(8080))), "host ports are not allowed")
	mustCreate(t, w.eddie, plainPod("noport", "acme-shop", port(0)))
}

func TestEditorPodWithNodeNameIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	pod := plainPod("pinned", "acme-shop", func(s *corev1.PodSpec) { s.NodeName = "node-1" })
	requireDenied(t, w.eddie.Create(context.Background(), pod), "spec.nodeName is not allowed")
}

func TestEditorDeploymentWithHostPathIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	deployment := deploymentWith("hostpath", "acme-shop", func(s *corev1.PodSpec) {
		s.Volumes = []corev1.Volume{{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}}}
	})
	requireDenied(t, w.eddie.Create(context.Background(), deployment), "hostPath volumes are not allowed")
}

func TestEditorDeploymentWithHostNetworkIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	deployment := deploymentWith("hostnet", "acme-shop", func(s *corev1.PodSpec) { s.HostNetwork = true })
	requireDenied(t, w.eddie.Create(context.Background(), deployment), "host namespaces are not allowed")
}

func TestEditorDeploymentUpdateCannotAddHostPath(t *testing.T) {
	w := newWorkloadWorld(t)
	deployment := deploymentWith("grow", "acme-shop", keep)
	mustCreate(t, w.eddie, deployment)
	deployment.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/"}}}}
	requireDenied(t, w.eddie.Update(context.Background(), deployment), "hostPath volumes are not allowed")
}

func TestPolicyIgnoresNamespacesWithoutTheTenantLabel(t *testing.T) {
	w := newWorkloadWorld(t)
	mustCreate(t, w.eddie, plainPod("priv", "plain", func(s *corev1.PodSpec) {
		s.Containers[0].SecurityContext = &corev1.SecurityContext{Privileged: ptr.To(true)}
	}))
}

func TestPolicyIgnoresNonBedrockIdentities(t *testing.T) {
	w := newWorkloadWorld(t)
	mustCreate(t, w.kubevirt, plainPod("launcher", "acme-shop", func(s *corev1.PodSpec) {
		s.Volumes = []corev1.Volume{{Name: "host", VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/var/run"}}}}
	}))
}

func withCapabilities(add ...corev1.Capability) func(*corev1.PodSpec) {
	return func(s *corev1.PodSpec) {
		s.Containers[0].SecurityContext = &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: add}}
	}
}

func withUnmaskedProc(s *corev1.PodSpec) {
	unmasked := corev1.UnmaskedProcMount
	s.HostUsers = ptr.To(false)
	s.Containers[0].SecurityContext = &corev1.SecurityContext{ProcMount: &unmasked}
}

func TestEditorPodWithExtraCapabilitiesIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	for _, name := range []string{"SYS_ADMIN", "NET_ADMIN", "SYS_MODULE", "SYS_PTRACE", "ALL"} {
		pod := plainPod("cap-"+strings.ToLower(strings.ReplaceAll(name, "_", "-")), "acme-shop", withCapabilities(corev1.Capability(name)))
		requireDenied(t, w.eddie.Create(context.Background(), pod), "added capabilities other than NET_BIND_SERVICE")
	}
	mustCreate(t, w.eddie, plainPod("bind", "acme-shop", withCapabilities("NET_BIND_SERVICE")))
}

func TestEditorInitContainerWithExtraCapabilitiesIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	pod := plainPod("init-cap", "acme-shop", func(s *corev1.PodSpec) {
		s.InitContainers = []corev1.Container{{Name: "init", Image: pauseImage, SecurityContext: &corev1.SecurityContext{Capabilities: &corev1.Capabilities{Add: []corev1.Capability{"NET_ADMIN"}}}}}
	})
	requireDenied(t, w.eddie.Create(context.Background(), pod), "added capabilities other than NET_BIND_SERVICE")
}

func TestEditorPodWithUnmaskedProcMountIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	requireDenied(t, w.eddie.Create(context.Background(), plainPod("unmasked", "acme-shop", withUnmaskedProc)), "unmasked procMount")
}

func TestEditorDeploymentTemplateWithExtraCapabilitiesIsDenied(t *testing.T) {
	w := newWorkloadWorld(t)
	requireDenied(t, w.eddie.Create(context.Background(), deploymentWith("cap", "acme-shop", withCapabilities("SYS_ADMIN"))), "added capabilities other than NET_BIND_SERVICE")
	requireDenied(t, w.eddie.Create(context.Background(), deploymentWith("proc", "acme-shop", withUnmaskedProc)), "unmasked procMount")
}

func TestEditorDeploymentUpdateCannotAddCapabilities(t *testing.T) {
	w := newWorkloadWorld(t)
	deployment := deploymentWith("grow-cap", "acme-shop", keep)
	mustCreate(t, w.eddie, deployment)
	withCapabilities("SYS_ADMIN")(&deployment.Spec.Template.Spec)
	requireDenied(t, w.eddie.Update(context.Background(), deployment), "added capabilities other than NET_BIND_SERVICE")
	withUnmaskedProc(&deployment.Spec.Template.Spec)
	requireDenied(t, w.eddie.Update(context.Background(), deployment), "unmasked procMount")
}

func TestOtherWorkloadKindsAreHardenedToo(t *testing.T) {
	w := newWorkloadWorld(t)
	mustCreate(t, w.admin, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "bedrock-root"},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects:   []rbacv1.Subject{{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:root"}},
	})
	root := actingAs(t, w.cfg, "bedrock:root")
	labels := map[string]string{"app": "x"}
	template := func(change func(*corev1.PodSpec)) corev1.PodTemplateSpec {
		return corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: podSpecWith(change)}
	}
	hostile := withCapabilities("SYS_ADMIN")
	objects := []client.Object{
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "sts", Namespace: "acme-shop"}, Spec: appsv1.StatefulSetSpec{ServiceName: "x", Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template(hostile)}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "ds", Namespace: "acme-shop"}, Spec: appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template(hostile)}},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "rs", Namespace: "acme-shop"}, Spec: appsv1.ReplicaSetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: template(hostile)}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "acme-shop"}, Spec: batchv1.JobSpec{Template: withNever(template(hostile))}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "cron", Namespace: "acme-shop"}, Spec: batchv1.CronJobSpec{Schedule: "* * * * *", JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: withNever(template(hostile))}}}},
	}
	for _, obj := range objects {
		requireDenied(t, root.Create(context.Background(), obj), "added capabilities other than NET_BIND_SERVICE")
	}
}

func withNever(template corev1.PodTemplateSpec) corev1.PodTemplateSpec {
	template.Spec.RestartPolicy = corev1.RestartPolicyNever
	return template
}
