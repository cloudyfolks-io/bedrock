package database

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const digitUID = "865fc718-4387-45ac-aa66-45e11af2a021"

func TestAUidThatStartsWithADigitStillGivesValidObjectNamesAndKeepsTheBackupPathOnTheUid(t *testing.T) {
	db := v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Name: "orders", Namespace: "acme-web", UID: digitUID},
		Spec:       v1alpha1.DatabaseSpec{Engine: v1alpha1.DatabaseEnginePostgreSQL, Version: "17", StorageGi: 10, Backup: v1alpha1.DatabaseBackupSpec{Enabled: true}},
	}
	class := v1alpha1.DatabaseClass{Spec: v1alpha1.DatabaseClassSpec{CPU: resource.MustParse("500m"), Memory: resource.MustParse("1Gi")}}
	r := &Reconciler{}
	spec := r.specOf(&flow{db: &db, project: goldenProject(), cfg: Config{BackupBucket: "backups"}, catalog: Catalog{Class: class}})
	objects, err := renderFor(db.Spec.Engine, spec)
	must(t, err)
	names := []string{GateName(db.UID), VIPName(db.UID), ServerCertName(db.UID), ServerSecretName(db.UID), Suffixed(db.UID, SecretApp)}
	for _, obj := range objects {
		names = append(names, obj.GetName())
	}
	for _, name := range names {
		if problems := validation.IsDNS1035Label(name); len(problems) != 0 || len(name) > 63 {
			t.Errorf("%q is not a DNS-1035 label: %v", name, problems)
		}
	}
	var cluster, store map[string]any
	for _, obj := range objects {
		switch obj.GetKind() {
		case "Cluster":
			cluster = obj.Object
		case "ObjectStore":
			store = obj.Object
		}
	}
	if cluster == nil || store == nil {
		t.Fatalf("objects %v", names)
	}
	plugin := cluster["spec"].(map[string]any)["plugins"].([]any)[0].(map[string]any)["parameters"].(map[string]any)
	if plugin["serverName"] != digitUID {
		t.Fatalf("serverName %v, want %s", plugin["serverName"], digitUID)
	}
	path := store["spec"].(map[string]any)["configuration"].(map[string]any)["destinationPath"]
	if want := "s3://backups/" + digitUID + "/"; path != want || strings.Contains(path.(string), "db-") {
		t.Fatalf("destination %v, want %s", path, want)
	}
}
