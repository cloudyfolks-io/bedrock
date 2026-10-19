package database

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestADatabaseComesUpAndIsReady(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	w.bringUp()

	if w.phase() != "Ready" || w.reason() != ReasonReady {
		t.Fatalf("phase %s reason %s conditions %+v", w.phase(), w.reason(), w.db.Status.Conditions)
	}
	for _, name := range []string{v1alpha1.ConditionReady, ConditionEngineReady, ConditionGateReady, ConditionEndpointReady, ConditionBackupHealthy} {
		if meta.FindStatusCondition(w.db.Status.Conditions, name) == nil {
			t.Errorf("condition %s is missing", name)
		}
	}
	for _, name := range []string{v1alpha1.ConditionReady, ConditionEngineReady, ConditionGateReady, ConditionEndpointReady} {
		if !meta.IsStatusConditionTrue(w.db.Status.Conditions, name) {
			t.Errorf("condition %s is not true", name)
		}
	}
	if len(w.db.Status.Addresses) != 1 || w.db.Status.Addresses[0].IP != "10.100.0.2" || w.db.Status.Addresses[0].Port != 5432 || w.db.Status.CredentialsSecret != "orders-admin" || w.db.Status.ObservedGeneration != w.db.Generation {
		t.Fatalf("status %+v", w.db.Status)
	}
	if !controllerutil.ContainsFinalizer(w.db, Finalizer) || w.db.Annotations[annotationBootstrapped] != "true" || w.db.Annotations[annotationReadyOnce] != "true" {
		t.Fatalf("finalizer or annotations: %v %v", w.db.Finalizers, w.db.Annotations)
	}

	admin := getSecret(t, w.c, w.namespace, "orders-admin")
	if string(admin.Data["username"]) != "dbadmin" || string(admin.Data["host"]) != "10.100.0.2" || string(admin.Data["database"]) != "app" {
		t.Fatalf("admin secret %v", admin.Data)
	}
	if w.engine.count(`ALTER ROLE "dbadmin" WITH NOSUPERUSER CREATEDB CREATEROLE`) != 1 {
		t.Fatalf("the bootstrap did not run once: %v", w.engine.scripts)
	}

	var pods corev1.PodList
	must(t, w.c.List(ctx, &pods, client.InNamespace(w.namespace)))
	var proxies, engineInProject int
	for _, kind := range []string{"Cluster", "ObjectStore", "ScheduledBackup"} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(ClusterGVK.GroupVersion().WithKind(kind + "List"))
		if kind == "ObjectStore" {
			list.SetGroupVersionKind(ObjectStoreGVK.GroupVersion().WithKind("ObjectStoreList"))
		}
		must(t, w.c.List(ctx, list, client.InNamespace(w.namespace)))
		engineInProject += len(list.Items)
	}
	_ = proxies
	if engineInProject != 0 {
		t.Fatal("an engine object is in the project namespace")
	}
	if w.cluster().GetNamespace() != PlatformNamespace {
		t.Fatal("the cluster is not in the platform namespace")
	}
	select {
	case event := <-w.events.Events:
		if !strings.HasPrefix(event, "Normal Ready") {
			t.Fatalf("event %q", event)
		}
	default:
		t.Fatal("no Ready event")
	}
}

func TestEveryEngineObjectCarriesTheLabelsAndNoOwnerReference(t *testing.T) {
	w := newWorld(t, nil)
	w.bringUp()
	objects, err := EngineObjects(context.Background(), w.c, *w.db)
	must(t, err)
	kinds := map[string]bool{}
	for _, obj := range objects {
		kinds[obj.GetKind()] = true
		if obj.GetKind() == "Pod" || obj.GetKind() == "PersistentVolumeClaim" {
			continue
		}
		labels := obj.GetLabels()
		if labels[v1alpha1.LabelKind] != Kind || labels[v1alpha1.LabelName] != "orders" || labels[v1alpha1.LabelTenant] != "acme" || labels[v1alpha1.LabelProject] != "web" || len(obj.GetOwnerReferences()) != 0 {
			t.Errorf("%s %s: labels %v owners %v", obj.GetKind(), obj.GetName(), labels, obj.GetOwnerReferences())
		}
		if !strings.HasPrefix(obj.GetName(), string(w.db.UID)) {
			t.Errorf("%s %s is not named with the UID", obj.GetKind(), obj.GetName())
		}
	}
	for _, kind := range []string{"Cluster", "ObjectStore", "ScheduledBackup", "Service", "Secret", "Deployment", "Certificate", "NetworkPolicy", "ConfigMap"} {
		if !kinds[kind] {
			t.Errorf("no %s among %v", kind, kinds)
		}
	}
}

func TestTheClusterRendersFromTheClassVersionAndSettings(t *testing.T) {
	w := newWorld(t, func(spec *v1alpha1.DatabaseSpec) { spec.StorageGi = 20 })
	w.reconcile()
	spec := w.cluster().Object["spec"].(map[string]any)
	if spec["imageName"] != worldPostgresImage || spec["instances"] != int64(1) {
		t.Fatalf("cluster spec %v", spec)
	}
	storage := spec["storage"].(map[string]any)
	if storage["size"] != "20Gi" || storage["storageClass"] != "block" {
		t.Fatalf("storage %v", storage)
	}
	limits := spec["resources"].(map[string]any)["limits"].(map[string]any)
	if limits["cpu"] != "500m" || limits["memory"] != "1024Mi" {
		t.Fatalf("limits %v", limits)
	}
	stores := spec["plugins"].([]any)[0].(map[string]any)["parameters"].(map[string]any)
	if stores["barmanObjectName"] != string(w.db.UID) {
		t.Fatalf("plugin %v", stores)
	}
}

func TestAHighAvailabilityClassMakesThreeInstances(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	var class v1alpha1.DatabaseClass
	must(t, w.c.Get(ctx, client.ObjectKey{Name: w.class}, &class))
	class.Spec.HighAvailability = true
	must(t, w.c.Update(ctx, &class))
	w.reconcile()
	if instances, _, _ := unstructured.NestedInt64(w.cluster().Object, "spec", "instances"); instances != 3 {
		t.Fatalf("instances %d", instances)
	}
}

func TestADatabaseWithoutBackupMakesNoBackupObjects(t *testing.T) {
	w := newWorld(t, func(spec *v1alpha1.DatabaseSpec) { spec.Backup.Enabled = false })
	w.bringUp()
	for _, gvk := range []schemaKind{{ObjectStoreGVK.Group, "ObjectStore"}, {ScheduledBackupGVK.Group, "ScheduledBackup"}} {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(ClusterGVK.GroupVersion().WithKind(gvk.kind + "List"))
		list.SetAPIVersion(gvk.group + "/v1")
		must(t, w.c.List(context.Background(), list, client.InNamespace(PlatformNamespace), InstanceSelector(*w.db)))
		if len(list.Items) != 0 {
			t.Errorf("%s objects %d", gvk.kind, len(list.Items))
		}
	}
	if condition := meta.FindStatusCondition(w.db.Status.Conditions, ConditionBackupHealthy); condition == nil || condition.Reason != "BackupDisabled" || w.db.Status.Backup != nil {
		t.Fatalf("backup condition %+v status %+v", condition, w.db.Status.Backup)
	}
	if w.phase() != "Ready" {
		t.Fatalf("phase %s", w.phase())
	}
}

type schemaKind struct{ group, kind string }

func TestTheProxyAndTheGateAreWiredToTheVIP(t *testing.T) {
	w := newWorld(t, nil)
	w.bringUp()
	ctx := context.Background()
	var vip corev1.Service
	must(t, w.c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: VIPName(w.db.UID)}, &vip))
	address := vip.Annotations[annotationKubeVIP]
	if !strings.HasPrefix(address, "10.") || vip.Spec.Type != corev1.ServiceTypeLoadBalancer {
		t.Fatalf("vip service %+v", vip.Annotations)
	}
	var config corev1.ConfigMap
	must(t, w.c.Get(ctx, client.ObjectKey{Namespace: w.namespace, Name: EndpointName(*w.db)}, &config))
	if !strings.Contains(config.Data[ConfigFile], "server gate "+address+":5432") {
		t.Fatalf("proxy config %s", config.Data[ConfigFile])
	}
	var gate corev1.ConfigMap
	must(t, w.c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: GateName(w.db.UID)}, &gate))
	if !strings.Contains(gate.Data[ConfigFile], string(w.db.UID)) || !strings.Contains(gate.Data[ConfigFile], BackendHost(w.db.UID)) {
		t.Fatalf("gate config %s", gate.Data[ConfigFile])
	}
	var group v1alpha1.SecurityGroup
	must(t, w.c.Get(ctx, client.ObjectKey{Namespace: w.namespace, Name: SecurityGroupName(w.db.UID)}, &group))
	if len(group.Spec.Ingress) != 2 || group.Spec.Ingress[1].CIDR != address+"/32" {
		t.Fatalf("security group %+v", group.Spec.Ingress)
	}
}

func TestAnOperatorRestartInTheMiddleOfACreateKeepsTheAddresses(t *testing.T) {
	w := newWorld(t, nil)
	w.reconcile()
	ctx := context.Background()
	var before corev1.Service
	must(t, w.c.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: VIPName(w.db.UID)}, &before))
	address := w.db.Status.Addresses[0].IP

	w.r = w.reconciler()
	w.setClusterStatus("Setting up primary", 0)
	w.reconcile()
	w.r = w.reconciler()
	w.reconcile()
	var services corev1.ServiceList
	must(t, w.c.List(ctx, &services, client.InNamespace(PlatformNamespace), client.MatchingLabels{LabelInstance: string(w.db.UID)}))
	vips := 0
	for _, service := range services.Items {
		if service.Name == VIPName(w.db.UID) {
			vips++
			if service.Annotations[annotationKubeVIP] != before.Annotations[annotationKubeVIP] {
				t.Fatalf("the gate address moved from %s to %s", before.Annotations[annotationKubeVIP], service.Annotations[annotationKubeVIP])
			}
		}
	}
	if vips != 1 || w.db.Status.Addresses[0].IP != address {
		t.Fatalf("vips %d proxy address %v (was %s)", vips, w.db.Status.Addresses, address)
	}
}

func TestSuspendHibernatesTheClusterAndShowsSuspended(t *testing.T) {
	w := newWorld(t, nil)
	w.bringUp()
	ctx := context.Background()
	w.db.Spec.Suspended = true
	must(t, w.c.Update(ctx, w.db))
	w.reconcile()
	if got := w.cluster().GetAnnotations()["cnpg.io/hibernation"]; got != "on" || w.phase() != "Suspended" || w.reason() != ReasonSuspended {
		t.Fatalf("hibernation %q phase %s reason %s", got, w.phase(), w.reason())
	}
	w.db.Spec.Suspended = false
	must(t, w.c.Update(ctx, w.db))
	w.reconcile()
	if got := w.cluster().GetAnnotations()["cnpg.io/hibernation"]; got != "off" || w.phase() != "Ready" {
		t.Fatalf("after resume: hibernation %q phase %s", got, w.phase())
	}
}

func TestADatabaseThatWasReadyBecomesUnhealthyAndWarns(t *testing.T) {
	w := newWorld(t, nil)
	w.bringUp()
	for len(w.events.Events) > 0 {
		<-w.events.Events
	}
	w.setClusterStatus("Failing over", 0)
	w.reconcile()
	if w.phase() != "Unhealthy" || w.reason() != ReasonEngineWaiting || meta.IsStatusConditionTrue(w.db.Status.Conditions, ConditionEngineReady) {
		t.Fatalf("phase %s reason %s", w.phase(), w.reason())
	}
	select {
	case event := <-w.events.Events:
		if !strings.HasPrefix(event, "Warning Unhealthy") {
			t.Fatalf("event %q", event)
		}
	default:
		t.Fatal("no Unhealthy event")
	}
	w.setClusterStatus("Cluster in healthy state", 1)
	w.reconcile()
	if w.phase() != "Ready" {
		t.Fatalf("phase after the recovery %s", w.phase())
	}
}

func TestANewDatabaseThatStaysNotReadyFailsAfterTheProvisionTimeout(t *testing.T) {
	w := newWorld(t, nil)
	w.reconcile()
	if w.phase() != "Provisioning" {
		t.Fatalf("phase %s", w.phase())
	}
	w.clock = w.clock.Add(31 * time.Minute)
	w.reconcile()
	if w.phase() != "Failed" || !strings.Contains(meta.FindStatusCondition(w.db.Status.Conditions, v1alpha1.ConditionReady).Message, "30m0s") {
		t.Fatalf("phase %s %+v", w.phase(), meta.FindStatusCondition(w.db.Status.Conditions, v1alpha1.ConditionReady))
	}
	select {
	case event := <-w.events.Events:
		if !strings.HasPrefix(event, "Warning Failed") {
			t.Fatalf("event %q", event)
		}
	default:
		t.Fatal("no Failed event")
	}
	w.setClusterStatus("Cluster in healthy state", 1)
	w.setSecurityGroupReady()
	w.reconcile()
	w.setDeploymentReady(PlatformNamespace, GateName(w.db.UID), 1)
	w.setDeploymentReady(w.namespace, EndpointName(*w.db), 1)
	w.reconcile()
	if w.phase() != "Ready" {
		t.Fatalf("a failed database must recover: %s", w.phase())
	}
}

func TestARestoredDatabaseShowsRestoringAndResetsTheAdminPassword(t *testing.T) {
	w := newWorld(t, func(spec *v1alpha1.DatabaseSpec) {
		spec.Restore = &v1alpha1.DatabaseRestore{SourceUID: "33333333-4444-5555-6666-777777777777"}
	})
	w.s3.put("33333333-4444-5555-6666-777777777777/base/1", 10)
	w.reconcile()
	if w.phase() != "Restoring" {
		t.Fatalf("phase %s reason %s", w.phase(), w.reason())
	}
	if raw, _, _ := unstructured.NestedMap(w.cluster().Object, "spec", "bootstrap"); raw["recovery"] == nil {
		t.Fatalf("bootstrap %v", raw)
	}
	w.setClusterStatus("Cluster in healthy state", 1)
	w.setSecurityGroupReady()
	w.reconcile()
	w.setDeploymentReady(PlatformNamespace, GateName(w.db.UID), 1)
	w.setDeploymentReady(w.namespace, EndpointName(*w.db), 1)
	w.reconcile()
	if w.phase() != "Ready" || w.engine.count(`ALTER ROLE "dbadmin" PASSWORD 'SCRAM-SHA-256$`) != 1 {
		t.Fatalf("phase %s scripts %v", w.phase(), w.engine.scripts)
	}
}

func TestAdminResetFollowsTheDatabaseAnnotation(t *testing.T) {
	w := newWorld(t, nil)
	w.bringUp()
	before := string(getSecret(t, w.c, w.namespace, "orders-admin").Data["password"])
	w.db.Annotations[v1alpha1.AnnotationResetPassword] = "now"
	must(t, w.c.Update(context.Background(), w.db))
	w.reconcile()
	after := string(getSecret(t, w.c, w.namespace, "orders-admin").Data["password"])
	if after == before || w.engine.count(`ALTER ROLE "dbadmin" PASSWORD`) != 1 {
		t.Fatalf("password changed %v scripts %v", after != before, w.engine.scripts)
	}
}

func TestBackupStatusAndHealthComeFromTheBackupObjects(t *testing.T) {
	w := newWorld(t, nil)
	w.bringUp()
	ctx := context.Background()
	w.s3.put(string(w.db.UID)+"/base/1", 700)
	backup := backupObject("first-"+nextSequence(), string(w.db.UID), "completed", w.clock.Add(-time.Hour).UTC().Format(time.RFC3339), w.clock.Add(-50*time.Minute).UTC().Format(time.RFC3339))
	must(t, w.c.Create(ctx, backup))
	backup.Object["status"] = map[string]any{"phase": "completed", "startedAt": w.clock.Add(-time.Hour).UTC().Format(time.RFC3339), "stoppedAt": w.clock.Add(-50 * time.Minute).UTC().Format(time.RFC3339)}
	must(t, w.c.Status().Update(ctx, backup))
	w.clock = w.clock.Add(11 * time.Minute)
	w.reconcile()
	status := w.db.Status.Backup
	if status == nil || len(status.Items) != 1 || status.LastAt == nil || status.SizeBytes != 700 {
		t.Fatalf("backup status %+v", status)
	}
	if condition := meta.FindStatusCondition(w.db.Status.Conditions, ConditionBackupHealthy); condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != "BackupCurrent" {
		t.Fatalf("condition %+v", condition)
	}
}

func TestAVersionChangeOfARunningDatabaseIsRefusedAndNothingIsRendered(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	w.bringUp()
	if w.db.Annotations[annotationAppliedVersion] != "17" {
		t.Fatalf("annotations %v", w.db.Annotations)
	}
	w.db.Annotations[annotationAppliedVersion] = "16"
	must(t, w.c.Update(ctx, w.db))
	image, _, _ := unstructured.NestedString(w.cluster().Object, "spec", "imageName")
	w.reconcile()
	if w.reason() != ReasonUpgradeNotSupported || w.phase() != "Unhealthy" {
		t.Fatalf("phase %s reason %s", w.phase(), w.reason())
	}
	if after, _, _ := unstructured.NestedString(w.cluster().Object, "spec", "imageName"); after != image {
		t.Fatal("the image changed")
	}
}

func TestAClassChangeThatFlipsHighAvailabilityIsRefused(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	w.bringUp()
	ha := &v1alpha1.DatabaseClass{ObjectMeta: metav1.ObjectMeta{Name: w.class + "-ha"}, Spec: v1alpha1.DatabaseClassSpec{CPU: resource.MustParse("500m"), Memory: resource.MustParse("1Gi"), MinStorageGi: 5, MaxStorageGi: 100, HighAvailability: true, Enabled: true}}
	create(t, w.c, ha)
	w.db.Spec.Class = ha.Name
	must(t, w.c.Update(ctx, w.db))
	w.reconcile()
	if w.reason() != ReasonClassChangeNotSupported {
		t.Fatalf("reason %s", w.reason())
	}
	if instances, _, _ := unstructured.NestedInt64(w.cluster().Object, "spec", "instances"); instances != 1 {
		t.Fatalf("instances %d", instances)
	}
	same := &v1alpha1.DatabaseClass{ObjectMeta: metav1.ObjectMeta{Name: w.class + "-big"}, Spec: v1alpha1.DatabaseClassSpec{CPU: resource.MustParse("1"), Memory: resource.MustParse("2Gi"), MinStorageGi: 5, MaxStorageGi: 100, Enabled: true}}
	create(t, w.c, same)
	w.db.Spec.Class = same.Name
	must(t, w.c.Update(ctx, w.db))
	w.reconcile()
	if w.reason() == ReasonClassChangeNotSupported {
		t.Fatal("a class with the same highAvailability was refused")
	}
}

func TestStorageGrowthStaysInsideTheClassRange(t *testing.T) {
	w := newWorld(t, nil)
	ctx := context.Background()
	w.bringUp()
	size := func() string {
		value, _, _ := unstructured.NestedString(w.cluster().Object, "spec", "storage", "size")
		return value
	}
	w.db.Spec.StorageGi = 50
	must(t, w.c.Update(ctx, w.db))
	w.reconcile()
	if size() != "50Gi" {
		t.Fatalf("growth inside the range was not applied: %s", size())
	}
	w.db.Spec.StorageGi = 101
	must(t, w.c.Update(ctx, w.db))
	w.reconcile()
	if w.reason() != ReasonStorageOutOfRange || size() != "50Gi" {
		t.Fatalf("reason %s size %s", w.reason(), size())
	}
}
