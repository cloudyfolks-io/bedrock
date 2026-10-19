package database

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func requireRefusal(t *testing.T, w *world, reason string) {
	t.Helper()
	waiting := w.phase() == "Provisioning" || w.phase() == "Restoring" && w.db.Spec.Restore != nil
	if !waiting || w.reason() != reason {
		t.Fatalf("phase %s reason %s, want a waiting phase and %s", w.phase(), w.reason(), reason)
	}
	if w.engineObjects() != 0 {
		t.Fatalf("%d engine objects were made although the create is refused", w.engineObjects())
	}
	var proxies appsv1.DeploymentList
	must(t, w.c.List(context.Background(), &proxies, client.InNamespace(w.namespace)))
	if len(proxies.Items) != 0 {
		t.Fatal("a proxy was made although the create is refused")
	}
}

func TestAFullPlatformRefusesTheCreateAndMakesNothing(t *testing.T) {
	w := newWorld(t, nil)
	setQuotaStatus(t, w.c, "10", "9", "400Gi", "1Gi")
	w.reconcile()
	requireRefusal(t, w, ReasonCapacityExceeded)
	if exists(w.c, secretKey(PlatformNamespace, Suffixed(w.db.UID, SecretApp))) {
		t.Fatal("the engine secrets were made before the capacity check")
	}
	setQuotaStatus(t, w.c, "100", "0", "400Gi", "0")
	w.reconcile()
	if w.reason() == ReasonCapacityExceeded || w.engineObjects() == 0 {
		t.Fatalf("the create did not go on after the room came back: %s", w.reason())
	}
}

func secretKey(namespace, name string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
}

func TestAnUnknownCapacityRefusesTheCreate(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	setQuotaStatus(t, w.c, "1", "0", "1Gi", "0")
	var quota corev1.ResourceQuota
	must(t, w.c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: QuotaName}, &quota))
	quota.Status.Hard, quota.Status.Used = nil, nil
	must(t, w.c.Status().Update(ctx, &quota))
	w.reconcile()
	requireRefusal(t, w, ReasonCapacityUnknown)
}

func TestAFullPlatformDoesNotTouchADatabaseThatAlreadyExists(t *testing.T) {
	w := newWorld(t, nil)
	w.bringUp()
	setQuotaStatus(t, w.c, "10", "10", "400Gi", "400Gi")
	w.reconcile()
	if w.phase() != "Ready" {
		t.Fatalf("phase %s reason %s", w.phase(), w.reason())
	}
}

func TestCreateRefusals(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		edit   func(*v1alpha1.DatabaseSpec)
		before func(*testing.T, *world)
	}{
		{"unknown version", ReasonVersionNotAvailable, func(s *v1alpha1.DatabaseSpec) { s.Version = "99" }, nil},
		{"unknown class", ReasonClassNotAvailable, func(s *v1alpha1.DatabaseSpec) { s.Class = "nope-class" }, nil},
		{"storage above the class", ReasonStorageOutOfRange, func(s *v1alpha1.DatabaseSpec) { s.StorageGi = 1000 }, nil},
		{"storage below the class", ReasonStorageOutOfRange, func(s *v1alpha1.DatabaseSpec) { s.StorageGi = 1 }, nil},
		{"unknown subnet", ReasonSubnetNotFound, func(s *v1alpha1.DatabaseSpec) { s.SubnetRef = "nope" }, nil},
		{"no VIP range", ReasonVIPRangeMissing, nil, func(t *testing.T, w *world) { setSetting(t, w.c, "database.vip-range", "") }},
		{"no DNS servers", ReasonDNSMissing, nil, func(t *testing.T, w *world) { setSetting(t, w.c, "network.dns-servers", "") }},
		{"no HAProxy image", ReasonEndpointImageMissing, nil, func(t *testing.T, w *world) { w.r.Images = []string{"quay.io/x/y:1"} }},
		{"missing RuntimeClass", ReasonRuntimeUnavailable, nil, func(t *testing.T, w *world) { setSetting(t, w.c, "container.runtime-class", "kata-missing") }},
		{"restore source without backups", ReasonRestoreSourceEmpty, func(s *v1alpha1.DatabaseSpec) {
			s.Restore = &v1alpha1.DatabaseRestore{SourceUID: "44444444-5555-6666-7777-888888888888"}
		}, nil},
		{"backup bucket name taken", ReasonBackupStoreFailed, nil, func(t *testing.T, w *world) {
			setSetting(t, w.c, "database.backup.bucket", "taken-by-someone")
			w.s3.createAs = "BucketAlreadyExists"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t, c.edit)
			if c.before != nil {
				c.before(t, w)
			}
			w.reconcile()
			requireRefusal(t, w, c.reason)
		})
	}
}

func TestADisabledVersionOrClassRefusesANewDatabaseOnly(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	var version v1alpha1.DatabaseVersion
	must(t, w.c.Get(ctx, client.ObjectKey{Name: "postgresql-17"}, &version))
	version.Spec.Enabled = false
	must(t, w.c.Update(ctx, &version))
	t.Cleanup(func() {
		version.Spec.Enabled = true
		_ = w.c.Update(ctx, &version)
	})
	w.reconcile()
	requireRefusal(t, w, ReasonVersionNotAvailable)

	version.Spec.Enabled = true
	must(t, w.c.Update(ctx, &version))
	w.bringUp()
	version.Spec.Enabled = false
	must(t, w.c.Update(ctx, &version))
	w.reconcile()
	if w.phase() != "Ready" {
		t.Fatalf("a disabled version stopped a running database: %s %s", w.phase(), w.reason())
	}

	var class v1alpha1.DatabaseClass
	must(t, w.c.Get(ctx, client.ObjectKey{Name: w.class}, &class))
	class.Spec.Enabled = false
	must(t, w.c.Update(ctx, &class))
	w.reconcile()
	if w.phase() != "Ready" {
		t.Fatalf("a disabled class stopped a running database: %s %s", w.phase(), w.reason())
	}
}

func TestASubnetThatIsNotReadyHoldsTheCreate(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	var subnet v1alpha1.Subnet
	must(t, w.c.Get(ctx, client.ObjectKey{Namespace: w.namespace, Name: "default"}, &subnet))
	subnet.Status.Conditions = []metav1.Condition{{Type: v1alpha1.ConditionReady, Status: metav1.ConditionFalse, Reason: "Test", LastTransitionTime: metav1.Now()}}
	must(t, w.c.Status().Update(ctx, &subnet))
	w.reconcile()
	requireRefusal(t, w, ReasonSubnetNotReady)
}

func TestABackupStoreThatIsNotReadyHoldsTheCreate(t *testing.T) {
	w := newWorld(t, nil)
	deleteBackupIdentity(t, w.c)
	w.reconcile()
	requireRefusal(t, w, ReasonBackupStorePending)
}

func TestAnObjectOfAnotherOwnerWithTheNameOfTheProxyIsLeftAlone(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	w.reconcile()
	w.setSecurityGroupReady()
	foreign := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: EndpointName(*w.db), Namespace: w.namespace},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "foreign"}},
			Template: podTemplate("foreign"),
		},
	}
	must(t, w.c.Create(ctx, foreign))
	w.reconcile()
	if w.reason() != ReasonNameConflict {
		t.Fatalf("reason %s", w.reason())
	}
	var stored appsv1.Deployment
	must(t, w.c.Get(ctx, client.ObjectKeyFromObject(foreign), &stored))
	if len(stored.OwnerReferences) != 0 || stored.Spec.Template.Labels["app"] != "foreign" {
		t.Fatalf("the foreign deployment was changed: %+v", stored.Spec.Template.Labels)
	}
}

func podTemplate(app string) corev1.PodTemplateSpec {
	return corev1.PodTemplateSpec{
		ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": app}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "busybox"}}},
	}
}
