package backup

import (
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/yaml"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/backup/target"
)

var fixtureScope = Scope{Namespace: "acme-prod", Tenant: "acme", Project: "prod"}

func TestVeleroNameIsUniquePerNamespace(t *testing.T) {
	a := VeleroName("vmb", "acme-prod", "daily")
	b := VeleroName("vmb", "acme-test", "daily")
	if a == b {
		t.Fatalf("names collide: %s", a)
	}
	if a != VeleroName("vmb", "acme-prod", "daily") {
		t.Fatal("name is not stable")
	}
	if !strings.HasPrefix(a, "vmb-daily-") {
		t.Fatalf("name %s", a)
	}
	long := VeleroName("vmb", "acme-prod", strings.Repeat("a", 63))
	if len(long) > 40 || len(validation.IsDNS1123Subdomain(long)) > 0 {
		t.Fatalf("long name %s", long)
	}
	for _, name := range []string{strings.Repeat("a", 23) + "-bcd", strings.Repeat("a", 23) + ".bcd"} {
		got := VeleroName("vmb", "acme-prod", name)
		if strings.Contains(got, "--") || strings.Contains(got, "-.") || strings.Contains(got, ".-") || len(validation.IsDNS1123Subdomain(got)) > 0 {
			t.Fatalf("separator problem in %s", got)
		}
	}
}

func TestScopeOf(t *testing.T) {
	labeled := corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "acme-prod", Labels: map[string]string{v1alpha1.LabelTenant: "acme", v1alpha1.LabelProject: "prod"}}}
	got, ok := ScopeOf(labeled)
	if !ok || got != fixtureScope {
		t.Fatalf("got %+v %v", got, ok)
	}
	if _, ok := ScopeOf(corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system"}}); ok {
		t.Fatal("namespace without labels must not be a project")
	}
}

func TestStorageLocation(t *testing.T) {
	tgt := target.Target{Scheme: "http", Endpoint: "seaweedfs.backup-e2e.svc:8333", Bucket: "bedrock-backups", Region: "us-east-1"}
	want := map[string]any{
		"apiVersion": "velero.io/v1",
		"kind":       "BackupStorageLocation",
		"metadata": map[string]any{
			"name":      "default",
			"namespace": "velero",
			"labels":    map[string]any{"bedrock.cloudyfolks.io/kind": "BackupStorageLocation", "bedrock.cloudyfolks.io/name": "default"},
		},
		"spec": map[string]any{
			"provider":      "aws",
			"default":       true,
			"accessMode":    "ReadWrite",
			"objectStorage": map[string]any{"bucket": "bedrock-backups"},
			"credential":    map[string]any{"name": "bedrock-backup-credentials", "key": "cloud"},
			"config": map[string]any{
				"region":            "us-east-1",
				"s3Url":             "http://seaweedfs.backup-e2e.svc:8333",
				"s3ForcePathStyle":  "true",
				"checksumAlgorithm": "",
			},
		},
	}
	if got := StorageLocation(tgt, nil).Object; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	tgt.Prefix = "bedrock/prod"
	ca := []byte("-----BEGIN CERTIFICATE-----\nabc\n")
	object := StorageLocation(tgt, ca).Object["spec"].(map[string]any)["objectStorage"].(map[string]any)
	if object["prefix"] != "bedrock/prod" || object["caCert"] != base64.StdEncoding.EncodeToString(ca) {
		t.Fatalf("objectStorage %#v", object)
	}
}

func TestCredentialsSecret(t *testing.T) {
	s := CredentialsSecret("AK", "SK")
	if s.Name != "bedrock-backup-credentials" || s.Namespace != "velero" || string(s.Data["cloud"]) != "[default]\naws_access_key_id=AK\naws_secret_access_key=SK\n" || len(s.StringData) != 0 {
		t.Fatalf("secret %+v", s)
	}
	if s.Kind != "Secret" || s.APIVersion != "v1" || s.Labels[v1alpha1.LabelKind] != "Secret" {
		t.Fatalf("type meta %+v labels %v", s.TypeMeta, s.Labels)
	}
}

func TestRepositorySecret(t *testing.T) {
	s := RepositorySecret("RP")
	if s.Name != "velero-repo-credentials" || s.Namespace != "velero" || string(s.Data["repository-password"]) != "RP" {
		t.Fatalf("secret %+v", s)
	}
}

func TestSnapshotClass(t *testing.T) {
	c := SnapshotClass()
	if c.GetKind() != "VolumeSnapshotClass" || c.GetAPIVersion() != "snapshot.storage.k8s.io/v1" || c.GetName() != "velero-block" {
		t.Fatalf("class %v", c.Object)
	}
	if c.Object["driver"] != "rook-ceph.rbd.csi.ceph.com" || c.Object["deletionPolicy"] != "Delete" {
		t.Fatalf("class %v", c.Object)
	}
	if c.GetLabels()["velero.io/csi-volumesnapshot-class"] != "true" {
		t.Fatalf("labels %v", c.GetLabels())
	}
	if _, found := c.GetAnnotations()["snapshot.storage.kubernetes.io/is-default-class"]; found {
		t.Fatal("class must not be default")
	}
	wantParameters := map[string]any{
		"clusterID": "rook-ceph",
		"csi.storage.k8s.io/snapshotter-secret-name":      "rook-csi-rbd-provisioner",
		"csi.storage.k8s.io/snapshotter-secret-namespace": "rook-ceph",
	}
	if !reflect.DeepEqual(c.Object["parameters"], wantParameters) {
		t.Fatalf("parameters %v", c.Object["parameters"])
	}
}

func fixtureSelectors() []any {
	return []any{
		map[string]any{"matchLabels": map[string]any{"bedrock.cloudyfolks.io/backup-vm": "web"}},
		map[string]any{"matchLabels": map[string]any{
			"bedrock.cloudyfolks.io/kind":    "VirtualMachine",
			"bedrock.cloudyfolks.io/name":    "web",
			"bedrock.cloudyfolks.io/tenant":  "acme",
			"bedrock.cloudyfolks.io/project": "prod",
		}},
	}
}

func TestBackupSpecSelectsTheVMAndItsBedrockObjects(t *testing.T) {
	want := map[string]any{
		"includedNamespaces":       []any{"acme-prod"},
		"orLabelSelectors":         fixtureSelectors(),
		"storageLocation":          "default",
		"snapshotVolumes":          true,
		"snapshotMoveData":         true,
		"defaultVolumesToFsBackup": false,
		"ttl":                      "8760h0m0s",
		"csiSnapshotTimeout":       "10m0s",
		"itemOperationTimeout":     "4h0m0s",
	}
	if got := BackupSpec(fixtureScope, "web", ScheduleTTL); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
}

func vmbLabels() map[string]any {
	return map[string]any{
		"bedrock.cloudyfolks.io/kind":    "VirtualMachineBackup",
		"bedrock.cloudyfolks.io/name":    "daily",
		"bedrock.cloudyfolks.io/tenant":  "acme",
		"bedrock.cloudyfolks.io/project": "prod",
	}
}

func TestScheduleObject(t *testing.T) {
	s := Schedule(fixtureScope, "daily", "web", "0 2 * * *", ScheduleTTL)
	if s.GetKind() != "Schedule" || s.GetAPIVersion() != "velero.io/v1" || s.GetName() != VeleroName("vmb", "acme-prod", "daily") || s.GetNamespace() != "velero" {
		t.Fatalf("schedule %v", s.Object)
	}
	if !reflect.DeepEqual(s.Object["metadata"].(map[string]any)["labels"], vmbLabels()) {
		t.Fatalf("labels %v", s.GetLabels())
	}
	spec := s.Object["spec"].(map[string]any)
	if spec["schedule"] != "0 2 * * *" || spec["useOwnerReferencesInBackup"] != false {
		t.Fatalf("spec %v", spec)
	}
	template := BackupSpec(fixtureScope, "web", ScheduleTTL)
	template["metadata"] = map[string]any{"labels": vmbLabels()}
	if !reflect.DeepEqual(spec["template"], template) {
		t.Fatalf("template %#v", spec["template"])
	}
}

func TestBackupObject(t *testing.T) {
	b := Backup(fixtureScope, "daily", "web", OneShotTTL)
	if b.GetKind() != "Backup" || b.GetAPIVersion() != "velero.io/v1" || b.GetName() != VeleroName("vmb", "acme-prod", "daily") || b.GetNamespace() != "velero" {
		t.Fatalf("backup %v", b.Object)
	}
	if !reflect.DeepEqual(b.Object["metadata"].(map[string]any)["labels"], vmbLabels()) {
		t.Fatalf("labels %v", b.GetLabels())
	}
	want := BackupSpec(fixtureScope, "web", OneShotTTL)
	if !reflect.DeepEqual(b.Object["spec"], want) || want["ttl"] != "720h0m0s" {
		t.Fatalf("spec %#v", b.Object["spec"])
	}
}

func TestRestoreObject(t *testing.T) {
	r := Restore(fixtureScope, "back", "vmb-daily-x-20261008020000")
	name := VeleroName("vmr", "acme-prod", "back")
	if r.GetKind() != "Restore" || r.GetName() != name || r.GetNamespace() != "velero" {
		t.Fatalf("restore %v", r.Object)
	}
	wantLabels := v1alpha1.TenantLabels("VirtualMachineRestore", "back", "acme", "prod")
	if !reflect.DeepEqual(r.GetLabels(), wantLabels) {
		t.Fatalf("labels %v", r.GetLabels())
	}
	want := map[string]any{
		"backupName":             "vmb-daily-x-20261008020000",
		"includedNamespaces":     []any{"acme-prod"},
		"restorePVs":             true,
		"existingResourcePolicy": "none",
		"itemOperationTimeout":   "4h0m0s",
		"resourceModifier":       map[string]any{"kind": "ConfigMap", "name": name + "-modifiers"},
	}
	if !reflect.DeepEqual(r.Object["spec"], want) {
		t.Fatalf("spec %#v", r.Object["spec"])
	}
}

func TestRestoreModifiers(t *testing.T) {
	cm := RestoreModifiers(fixtureScope, "back")
	if cm.Namespace != "velero" || cm.Name != VeleroName("vmr", "acme-prod", "back")+"-modifiers" {
		t.Fatalf("configmap %s/%s", cm.Namespace, cm.Name)
	}
	var parsed struct {
		Version string `json:"version"`
		Rules   []struct {
			Conditions struct {
				GroupResource string   `json:"groupResource"`
				Namespaces    []string `json:"namespaces"`
			} `json:"conditions"`
			MergePatches []struct {
				PatchData string `json:"patchData"`
			} `json:"mergePatches"`
		} `json:"resourceModifierRules"`
	}
	if err := yaml.Unmarshal([]byte(cm.Data["resource-modifiers.yaml"]), &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Version != "v1" || len(parsed.Rules) != 2 {
		t.Fatalf("parsed %+v", parsed)
	}
	wantPatch := `{"metadata":{"annotations":{"bedrock.cloudyfolks.io/restore-hold":"back"}}}`
	for i, group := range []string{"virtualmachines.bedrock.cloudyfolks.io", "volumes.bedrock.cloudyfolks.io"} {
		rule := parsed.Rules[i]
		if rule.Conditions.GroupResource != group || !reflect.DeepEqual(rule.Conditions.Namespaces, []string{"acme-prod"}) {
			t.Fatalf("rule %d %+v", i, rule.Conditions)
		}
		if len(rule.MergePatches) != 1 || rule.MergePatches[0].PatchData != wantPatch {
			t.Fatalf("rule %d patches %+v", i, rule.MergePatches)
		}
	}
}

func TestDeleteBackupRequestObject(t *testing.T) {
	d := DeleteBackupRequest(fixtureScope, "vmb-daily-x-20261008020000")
	if d.GetKind() != "DeleteBackupRequest" || d.GetName() != VeleroName("del", "acme-prod", "vmb-daily-x-20261008020000") || d.GetNamespace() != "velero" {
		t.Fatalf("request %v", d.Object)
	}
	if spec := d.Object["spec"].(map[string]any); spec["backupName"] != "vmb-daily-x-20261008020000" {
		t.Fatalf("spec %v", spec)
	}
}
