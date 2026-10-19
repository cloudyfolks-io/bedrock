package database

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/valkey"
)

func valkeySpec(uid string) engine.Spec {
	podLabels := map[string]string{"app.kubernetes.io/instance": uid, "app.kubernetes.io/component": "engine"}
	return engine.Spec{
		Name: uid, Namespace: PlatformNamespace, Image: "valkey/valkey:9.1.2@sha256:aaa", CPUMilli: 500, MemoryMi: 1024, StorageGi: 20,
		Instances: 1, Labels: map[string]string{"app.kubernetes.io/instance": uid}, PodLabels: podLabels,
		Images: map[string]string{valkey.ImageExporter: "exporter@sha256:bbb"},
	}
}

func createValkeyDeployment(t *testing.T, c client.Client, spec engine.Spec) {
	t.Helper()
	cfg, err := LoadConfig(context.Background(), c)
	must(t, err)
	must(t, EnsurePlatform(context.Background(), c, cfg))
	objs, err := valkey.Render(spec)
	must(t, err)
	for _, obj := range objs {
		if obj.GetKind() == "Deployment" {
			var deployment appsv1.Deployment
			must(t, runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &deployment))
			create(t, c, &deployment)
		}
	}
}

func platformDeployment(t *testing.T, c client.Client, name string) appsv1.Deployment {
	t.Helper()
	var deployment appsv1.Deployment
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: PlatformNamespace, Name: name}, &deployment))
	return deployment
}

func TestApplyPlanRestartsAndSuspendsAndResumes(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	uid := "valkey-apply-" + nextSequence()
	spec := valkeySpec(uid)
	createValkeyDeployment(t, c, spec)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	must(t, ApplyPlan(ctx, c, v1alpha1.DatabaseEngineValkey, spec, "acme-web", Result{Steps: []Step{StepRestart}}, now))
	if got := platformDeployment(t, c, uid).Spec.Template.Annotations["kubectl.kubernetes.io/restartedAt"]; got != "2026-10-19T12:00:00Z" {
		t.Fatalf("restartedAt %q", got)
	}
	must(t, ApplyPlan(ctx, c, v1alpha1.DatabaseEngineValkey, spec, "acme-web", Result{Steps: []Step{StepSuspend}}, now))
	if replicas := *platformDeployment(t, c, uid).Spec.Replicas; replicas != 0 {
		t.Fatalf("suspended replicas %d", replicas)
	}
	must(t, ApplyPlan(ctx, c, v1alpha1.DatabaseEngineValkey, spec, "acme-web", Result{Steps: []Step{StepResume}}, now))
	if replicas := *platformDeployment(t, c, uid).Spec.Replicas; replicas != 1 {
		t.Fatalf("resumed replicas %d", replicas)
	}
}

func TestApplyPlanKeepsTheRunningImageUntilARefresh(t *testing.T) {
	c := startEnv(t)
	ctx := context.Background()
	uid := "valkey-image-" + nextSequence()
	spec := valkeySpec(uid)
	createValkeyDeployment(t, c, spec)
	running := spec.Image
	catalog := "valkey/valkey:9.1.3@sha256:ccc"

	spec.Image = ImageToApply(running, "9.1", "9.1", catalog, false)
	must(t, ApplyPlan(ctx, c, v1alpha1.DatabaseEngineValkey, spec, "acme-web", Result{Steps: []Step{StepResize}}, time.Now()))
	if got := platformDeployment(t, c, uid).Spec.Template.Spec.Containers[0].Image; got != running {
		t.Fatalf("a catalog change rolled the database: %s", got)
	}

	spec.Image = ImageToApply(running, "9.1", "9.1", catalog, true)
	must(t, ApplyPlan(ctx, c, v1alpha1.DatabaseEngineValkey, spec, "acme-web", Result{Steps: []Step{StepRefreshImage, StepRestart}}, time.Now()))
	if got := platformDeployment(t, c, uid).Spec.Template.Spec.Containers[0].Image; got != catalog {
		t.Fatalf("the refresh did not apply the catalog image: %s", got)
	}
}

func TestApplyPlanCreatesOnePreUpgradeBackup(t *testing.T) {
	scheme := runtime.NewScheme()
	must(t, corev1.AddToScheme(scheme))
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	spec := engineSpecFor(nil)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	result := Result{Hold: &Refusal{Reason: ReasonBackupRequired, Message: "m"}, RequestBackup: true}
	must(t, ApplyPlan(context.Background(), c, v1alpha1.DatabaseEnginePostgreSQL, spec, "ns", result, now))
	must(t, ApplyPlan(context.Background(), c, v1alpha1.DatabaseEnginePostgreSQL, spec, "ns", result, now))
	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion("postgresql.cnpg.io/v1")
	list.SetKind("BackupList")
	must(t, c.List(context.Background(), list))
	if len(list.Items) != 1 || !strings.HasPrefix(list.Items[0].GetName(), "uid-1-pre-") {
		t.Fatalf("backups %v", list.Items)
	}
}

func TestApplyPlanWithoutBackupAndWithoutStepsChangesNothing(t *testing.T) {
	scheme := runtime.NewScheme()
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	if err := ApplyPlan(context.Background(), c, v1alpha1.DatabaseEngineValkey, valkeySpec("uid-9"), "ns", Result{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := ApplyPlan(context.Background(), c, v1alpha1.DatabaseEngineValkey, valkeySpec("uid-9"), "ns", Result{RequestBackup: true}, time.Now()); err == nil {
		t.Fatal("valkey got a backup")
	}
}
