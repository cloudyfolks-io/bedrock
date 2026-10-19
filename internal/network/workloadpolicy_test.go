package network

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type workloadFixture struct {
	alice    client.Client
	operator client.Client
	cfg      *rest.Config
	admin    client.Client
	ns       string
}

var attachedTemplate = map[string]string{
	"ovn.kubernetes.io/logical_switch": "s",
	"v1.multus-cni.io/default-network": "elsewhere/net",
}

func podTemplate(annotations map[string]string) corev1.PodTemplateSpec {
	labels := map[string]string{"app": "x"}
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyAlways,
			Containers:    []corev1.Container{{Name: "main", Image: "registry.test/probe:1"}},
		},
	}
}

func jobTemplate(annotations map[string]string) corev1.PodTemplateSpec {
	template := podTemplate(annotations)
	template.Spec.RestartPolicy = corev1.RestartPolicyNever
	return template
}

func selector() *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{"app": "x"}}
}

func workloadObjects(namespace string, annotations map[string]string) map[string]client.Object {
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: namespace, Name: name} }
	return map[string]client.Object{
		"Deployment":  &appsv1.Deployment{ObjectMeta: meta("d"), Spec: appsv1.DeploymentSpec{Selector: selector(), Template: podTemplate(annotations)}},
		"ReplicaSet":  &appsv1.ReplicaSet{ObjectMeta: meta("r"), Spec: appsv1.ReplicaSetSpec{Selector: selector(), Template: podTemplate(annotations)}},
		"StatefulSet": &appsv1.StatefulSet{ObjectMeta: meta("s"), Spec: appsv1.StatefulSetSpec{ServiceName: "s", Selector: selector(), Template: podTemplate(annotations)}},
		"DaemonSet":   &appsv1.DaemonSet{ObjectMeta: meta("ds"), Spec: appsv1.DaemonSetSpec{Selector: selector(), Template: podTemplate(annotations)}},
		"Job":         &batchv1.Job{ObjectMeta: meta("j"), Spec: batchv1.JobSpec{Template: jobTemplate(annotations)}},
		"CronJob": &batchv1.CronJob{ObjectMeta: meta("c"), Spec: batchv1.CronJobSpec{
			Schedule:    "* * * * *",
			JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: jobTemplate(annotations)}},
		}},
	}
}

func namedCopy(object client.Object, name string) client.Object {
	copied := object.DeepCopyObject().(client.Object)
	copied.SetName(name)
	return copied
}

func newWorkloadFixture(t *testing.T) workloadFixture {
	t.Helper()
	c, cfg := testEnv(t)
	ns := newNamespace(t, unique(t, "wp"), "a")
	f := workloadFixture{
		alice:    impersonating(t, cfg, "bedrock:alice", "system:masters"),
		operator: impersonating(t, cfg, "system:serviceaccount:bedrock-system:bedrock-operator", "system:masters"),
		cfg:      cfg,
		admin:    c,
		ns:       ns,
	}
	probe := func() bool {
		deployment := workloadObjects(ns, attachedTemplate)["Deployment"]
		deployment.SetName(unique(t, "probe"))
		err := f.alice.Create(context.Background(), deployment)
		if err == nil {
			_ = f.admin.Delete(context.Background(), deployment)
		}
		return err != nil
	}
	installPolicy(t, c, "../../manifests/90-bedrock/network-workload-policy.yaml", probe)
	return f
}

func requireWorkloadDenied(t *testing.T, c client.Client, object client.Object) {
	t.Helper()
	err := c.Create(context.Background(), object)
	if err == nil {
		t.Fatalf("%T must be denied", object)
	}
	if !strings.Contains(err.Error(), platformOnly) {
		t.Fatalf("error %q must contain %q", err, platformOnly)
	}
}

func TestWorkloadPolicyDeniesNetworkSelectionByTenants(t *testing.T) {
	f := newWorkloadFixture(t)
	for kind, object := range workloadObjects(f.ns, attachedTemplate) {
		t.Run(kind, func(t *testing.T) {
			requireWorkloadDenied(t, f.alice, namedCopy(object, unique(t, "w")))
		})
	}
}

func TestWorkloadPolicyDeniesEveryAnnotationFamily(t *testing.T) {
	f := newWorkloadFixture(t)
	for _, key := range []string{
		"k8s.v1.cni.cncf.io/networks", "v1.multus-cni.io/default-network", "ovn.kubernetes.io/security_groups",
		"default.x.fabric.cloudyfolks.io/ip_address", "bedrock.cloudyfolks.io/restarted-at",
	} {
		deployment := workloadObjects(f.ns, map[string]string{key: "x"})["Deployment"]
		requireWorkloadDenied(t, f.alice, namedCopy(deployment, unique(t, "k")))
	}
}

func TestWorkloadPolicyAllowsOperatorAndWorkloadControllers(t *testing.T) {
	f := newWorkloadFixture(t)
	objects := workloadObjects(f.ns, attachedTemplate)
	if err := f.operator.Create(context.Background(), namedCopy(objects["Deployment"], unique(t, "op"))); err != nil {
		t.Fatalf("the operator must create a Deployment: %v", err)
	}
	deploymentController := impersonating(t, f.cfg, "system:serviceaccount:kube-system:deployment-controller", "system:masters")
	if err := deploymentController.Create(context.Background(), namedCopy(objects["ReplicaSet"], unique(t, "rs"))); err != nil {
		t.Fatalf("the deployment controller must create a ReplicaSet: %v", err)
	}
	cronJobController := impersonating(t, f.cfg, "system:serviceaccount:kube-system:cronjob-controller", "system:masters")
	if err := cronJobController.Create(context.Background(), namedCopy(objects["Job"], unique(t, "job"))); err != nil {
		t.Fatalf("the cronjob controller must create a Job: %v", err)
	}
}

func TestWorkloadPolicyAllowsPlainTenantWorkloads(t *testing.T) {
	f := newWorkloadFixture(t)
	plain := map[string]string{"prometheus.io/scrape": "true", "kubernetes.io/change-cause": "x"}
	for kind, object := range workloadObjects(f.ns, plain) {
		t.Run(kind, func(t *testing.T) {
			if err := f.alice.Create(context.Background(), namedCopy(object, unique(t, "p"))); err != nil {
				t.Fatalf("%s without network annotations must work: %v", kind, err)
			}
		})
	}
	for kind, object := range workloadObjects(f.ns, nil) {
		t.Run(kind+"-bare", func(t *testing.T) {
			if err := f.alice.Create(context.Background(), namedCopy(object, unique(t, "b"))); err != nil {
				t.Fatalf("%s with no annotations must work: %v", kind, err)
			}
		})
	}
}

func TestWorkloadPolicyDeniesChangesByTenantsOnUpdate(t *testing.T) {
	f := newWorkloadFixture(t)
	ctx := context.Background()
	name := unique(t, "held")
	deployment := namedCopy(workloadObjects(f.ns, attachedTemplate)["Deployment"], name).(*appsv1.Deployment)
	if err := f.operator.Create(ctx, deployment); err != nil {
		t.Fatal(err)
	}
	get := func() appsv1.Deployment {
		var current appsv1.Deployment
		if err := f.alice.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: name}, &current); err != nil {
			t.Fatal(err)
		}
		return current
	}
	scaled := get()
	scaled.Labels = map[string]string{"relabeled": "yes"}
	if err := f.alice.Update(ctx, &scaled); err != nil {
		t.Fatalf("an unchanged annotation must be ignored: %v", err)
	}
	changed := get()
	changed.Spec.Template.Annotations["ovn.kubernetes.io/logical_switch"] = "other"
	if err := f.alice.Update(ctx, &changed); err == nil || !strings.Contains(err.Error(), platformOnly) {
		t.Fatalf("a changed selection must be denied: %v", err)
	}
	removed := get()
	delete(removed.Spec.Template.Annotations, "ovn.kubernetes.io/logical_switch")
	if err := f.alice.Update(ctx, &removed); err == nil || !strings.Contains(err.Error(), platformOnly) {
		t.Fatalf("a removed selection must be denied: %v", err)
	}
	added := get()
	added.Spec.Template.Annotations["k8s.v1.cni.cncf.io/networks"] = "pp-b/default"
	if err := f.alice.Update(ctx, &added); err == nil || !strings.Contains(err.Error(), platformOnly) {
		t.Fatalf("an added selection must be denied: %v", err)
	}
	platform := get()
	platform.Spec.Template.Annotations["ovn.kubernetes.io/logical_switch"] = "other"
	if err := f.operator.Update(ctx, &platform); err != nil {
		t.Fatalf("the operator may change the selection: %v", err)
	}
}

func TestWorkloadPolicyIgnoresNamespacesWithoutTheTenantLabel(t *testing.T) {
	f := newWorkloadFixture(t)
	bare := unique(t, "bare")
	if err := f.admin.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: bare}}); err != nil {
		t.Fatal(err)
	}
	deployment := workloadObjects(bare, attachedTemplate)["Deployment"]
	if err := f.alice.Create(context.Background(), deployment); err != nil {
		t.Fatalf("a namespace without the tenant label is out of scope: %v", err)
	}
}
