package database

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"

	"github.com/cloudyfolks-io/bedrock/internal/backup/target"
)

func testSpec(t *testing.T) OffsiteSpec {
	t.Helper()
	tgt, err := target.Parse("s3://s3.example.com/bkt/pre/fix?region=eu-1")
	if err != nil {
		t.Fatal(err)
	}
	return OffsiteSpec{Image: "docker.io/rclone/rclone:1@sha256:abc", Schedule: "0 * * * *", Source: "bedrock-database-backups", Target: tgt}
}

func TestOffsiteCronJobSyncsToTargetPrefix(t *testing.T) {
	objects := OffsiteObjects(testSpec(t))
	cron, ok := objects[0].(*batchv1.CronJob)
	if !ok || len(objects) != 1 {
		t.Fatalf("objects %v", objects)
	}
	container := cron.Spec.JobTemplate.Spec.Template.Spec.Containers[0]
	want := []string{"copy", "SRC:bedrock-database-backups", "DST:bkt/pre/fix/databases", "--s3-no-check-bucket"}
	for i := range want {
		if container.Args[i] != want[i] {
			t.Fatalf("args %v, want %v", container.Args, want)
		}
	}
	if cron.Spec.Schedule != "0 * * * *" || cron.Namespace != PlatformNamespace || container.Image != testSpec(t).Image {
		t.Fatalf("cron %+v", cron.Spec)
	}
	for _, env := range container.Env {
		if env.Value != "" && env.Name == "RCLONE_CONFIG_DST_ACCESS_KEY_ID" {
			t.Fatal("credential in plain text")
		}
	}
}

func TestOffsiteSecretKeepsCAOnlyWhenGiven(t *testing.T) {
	if _, has := OffsiteSecret([]byte("a"), []byte("b"), nil).Data["ca.crt"]; has {
		t.Fatal("ca.crt without a CA")
	}
	if _, has := OffsiteSecret([]byte("a"), []byte("b"), []byte("pem")).Data["ca.crt"]; !has {
		t.Fatal("ca.crt missing")
	}
}

func TestRcloneImageFromRelease(t *testing.T) {
	images := []string{"docker.io/library/haproxy:3", "docker.io/rclone/rclone:1@sha256:abc"}
	if image, ok := rcloneImage(images); !ok || image != images[1] {
		t.Fatalf("image %q %v", image, ok)
	}
	if _, ok := rcloneImage(images[:1]); ok {
		t.Fatal("found image that is not in the release")
	}
}

func offsiteFixture(t *testing.T, enabled, targetValue string) (*OffsiteReconciler, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, batchv1.AddToScheme, v1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	setting := func(name, value string) *v1alpha1.Setting {
		return &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: v1alpha1.SettingSpec{Value: value}}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		setting("database.enabled", enabled), setting("backup.target", targetValue), setting("backup.credentials-secret", "creds"),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "bedrock-system"}, Data: map[string][]byte{"access-key-id": []byte("a"), "secret-access-key": []byte("b")}},
		&batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: OffsiteName, Namespace: PlatformNamespace}},
	).Build()
	return &OffsiteReconciler{Client: c, Live: c, Images: []string{"docker.io/rclone/rclone:1@sha256:abc"}}, c
}

func cronJobExists(t *testing.T, c client.Client) bool {
	t.Helper()
	err := c.Get(context.Background(), client.ObjectKey{Namespace: PlatformNamespace, Name: OffsiteName}, &batchv1.CronJob{})
	return err == nil
}

func TestOffsiteAbsentWithoutTargetOrDatabase(t *testing.T) {
	for name, values := range map[string][2]string{"no target": {"true", ""}, "database off": {"false", "s3://s3.example.com/bkt"}} {
		r, c := offsiteFixture(t, values[0], values[1])
		if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
			t.Fatal(name, err)
		}
		if cronJobExists(t, c) {
			t.Fatalf("%s: cron job still exists", name)
		}
	}
}

func TestOffsitePresentWithTargetAndDatabase(t *testing.T) {
	r, c := offsiteFixture(t, "true", "s3://s3.example.com/bkt/pre")
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	var cron batchv1.CronJob
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: PlatformNamespace, Name: OffsiteName}, &cron); err != nil || cron.Spec.Schedule != "0 * * * *" {
		t.Fatalf("cron %+v err %v", cron.Spec, err)
	}
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: PlatformNamespace, Name: OffsiteSecretName}, &secret); err != nil || string(secret.Data["access-key-id"]) != "a" {
		t.Fatalf("secret %v err %v", secret.Data, err)
	}
}
