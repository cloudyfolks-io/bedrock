package operator

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const pinnedImage = "ghcr.io/cloudnative-pg/postgresql:17.9-minimal-trixie@sha256:3c9f2df53e15f3fff0aee630beffe73936590333b7fd80f2239bf59b22a9852e"

func databaseVersion(name, engine, major, image string) *v1alpha1.DatabaseVersion {
	return &v1alpha1.DatabaseVersion{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.DatabaseVersionSpec{Engine: engine, Major: major, Image: image, Port: 5432, Enabled: true},
	}
}

func databaseClass(name string, minGi, maxGi int32) *v1alpha1.DatabaseClass {
	return &v1alpha1.DatabaseClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.DatabaseClassSpec{
			CPU: resource.MustParse("500m"), Memory: resource.MustParse("1Gi"),
			MinStorageGi: minGi, MaxStorageGi: maxGi, Enabled: true,
		},
	}
}

func TestDatabaseVersionValidation(t *testing.T) {
	c, _ := StartTestEnv(t)

	good := databaseVersion("postgresql-17", "postgresql", "17", pinnedImage)
	good.Spec.EOLDate = "2029-11-08"
	good.Spec.UpgradeTo = []string{"18"}
	good.Spec.Companions = map[string]string{"exporter": "quay.io/example/exporter@sha256:" + sha64}
	createValid(t, c, good)

	createInvalid(t, c, databaseVersion("postgresql-16", "postgresql", "16", "ghcr.io/cloudnative-pg/postgresql:16.15"), "pinned by digest")
	createInvalid(t, c, databaseVersion("postgresql-15", "postgresql", "16", pinnedImage), "<engine>-<major>")
	createInvalid(t, c, databaseVersion("oracle-1", "oracle", "1", pinnedImage), "spec.engine")

	selfUpgrade := databaseVersion("postgresql-19", "postgresql", "19", pinnedImage)
	selfUpgrade.Spec.UpgradeTo = []string{"19"}
	createInvalid(t, c, selfUpgrade, "upgradeTo must not list the major")

	badCompanion := databaseVersion("mysql-8.4", "mysql", "8.4", pinnedImage)
	badCompanion.Spec.Companions = map[string]string{"toolkit": "quay.io/example/toolkit:1"}
	createInvalid(t, c, badCompanion, "every companion image")

	badDate := databaseVersion("postgresql-20", "postgresql", "20", pinnedImage)
	badDate.Spec.EOLDate = "next year"
	createInvalid(t, c, badDate, "spec.eolDate")

	badPort := databaseVersion("postgresql-21", "postgresql", "21", pinnedImage)
	badPort.Spec.Port = 0
	createInvalid(t, c, badPort, "spec.port")
}

func TestDatabaseVersionDefaultsToEnabledAndKeepsFalse(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	raw := databaseVersion("postgresql-17", "postgresql", "17", pinnedImage)
	raw.Spec.Enabled = false
	createValid(t, c, raw)
	var stored v1alpha1.DatabaseVersion
	if err := c.Get(ctx, client.ObjectKeyFromObject(raw), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Spec.Enabled {
		t.Fatal("enabled=false was turned into true")
	}
}

func TestDatabaseClassValidation(t *testing.T) {
	c, _ := StartTestEnv(t)

	ha := databaseClass("small-ha", 10, 100)
	ha.Spec.HighAvailability = true
	createValid(t, c, ha)
	createValid(t, c, databaseClass("equal", 20, 20))

	createInvalid(t, c, databaseClass("inverted", 100, 10), "maxStorageGi must not be below minStorageGi")
	createInvalid(t, c, databaseClass("zero", 0, 10), "spec.minStorageGi")
	createInvalid(t, c, databaseClass("Bad_Name", 1, 10), "the name must be a DNS label")
}

const sha64 = "0000000000000000000000000000000000000000000000000000000000000000"
