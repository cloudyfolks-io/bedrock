package objectstorage

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestParseRulesUsesTheDefaults(t *testing.T) {
	rules, err := ParseRules(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	want := Rules{ReservedNames: []string{"admin", "bedrock-registry", "bedrock-database-backups"}, MaxBuckets: 100}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules %+v", rules)
	}
}

func TestParseRulesReadsEveryValue(t *testing.T) {
	rules, err := ParseRules(map[string]string{
		"objectstorage.reserved-bucket-names":    "admin,billing",
		"objectstorage.reserved-bucket-prefixes": "git-,e2e-",
		"objectstorage.cors-origins":             "https://panel.test/,http://localhost:3000",
		"objectstorage.max-buckets":              "7",
		"objectstorage.public-host":              "s3.example.test",
		"objectstorage.public-aliases":           "s3.a.test,s3.b.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := Rules{
		ReservedNames:    []string{"admin", "billing", "bedrock-registry", "bedrock-database-backups"},
		ReservedPrefixes: []string{"git-", "e2e-"},
		CORSOrigins:      []string{"https://panel.test", "http://localhost:3000"},
		MaxBuckets:       7,
		PublicHost:       "s3.example.test",
		PublicAliases:    []string{"s3.a.test", "s3.b.test"},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules %+v", rules)
	}
}

func TestParseRulesReservesThePlatformBuckets(t *testing.T) {
	rules, err := ParseRules(map[string]string{"registry.bucket": "image-blobs", "database.backup.bucket": "db-dumps"})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"image-blobs", "db-dumps", "admin"} {
		if nameProblem(name, rules, false) != reasonNameReserved {
			t.Errorf("%s is not reserved", name)
		}
	}
	if nameProblem("bedrock-registry", rules, false) != "" {
		t.Error("the old default is still reserved after the Setting changed")
	}
}

func TestParseRulesRefusesABadValue(t *testing.T) {
	for key, value := range map[string]string{
		"objectstorage.max-buckets":           "0",
		"objectstorage.cors-origins":          "panel.test",
		"objectstorage.reserved-bucket-names": "Admin",
		"objectstorage.public-host":           "bad host",
	} {
		if _, err := ParseRules(map[string]string{key: value}); err == nil {
			t.Fatalf("%s=%q was accepted", key, value)
		}
	}
}

func putSetting(t *testing.T, key, value string) {
	t.Helper()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}, Spec: v1alpha1.SettingSpec{Value: value}}
	if err := testClient.Create(context.Background(), setting); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testClient.Delete(context.Background(), setting) })
}

func TestLoadRulesReadsTheSettings(t *testing.T) {
	putSetting(t, "objectstorage.max-buckets", "12")
	putSetting(t, "objectstorage.reserved-bucket-prefixes", "git-")
	rules, err := LoadRules(context.Background(), testClient)
	if err != nil {
		t.Fatal(err)
	}
	if rules.MaxBuckets != 12 || len(rules.ReservedPrefixes) != 1 || rules.ReservedPrefixes[0] != "git-" || rules.ReservedNames[0] != "admin" {
		t.Fatalf("rules %+v", rules)
	}
}

func TestRulesConfigMapFollowsTheSettings(t *testing.T) {
	putSetting(t, "objectstorage.reserved-bucket-names", "admin,billing")
	putSetting(t, "objectstorage.reserved-bucket-prefixes", "git-,e2e-")
	r := &RulesConfigReconciler{Client: testClient}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	var config corev1.ConfigMap
	key := client.ObjectKey{Namespace: SystemNamespace, Name: RulesConfigMapName}
	if err := testClient.Get(context.Background(), key, &config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testClient.Delete(context.Background(), &config) })
	want := map[string]string{"reservedNames": "admin,billing,bedrock-registry,bedrock-database-backups", "reservedPrefixes": "git-,e2e-"}
	if !reflect.DeepEqual(config.Data, want) {
		t.Fatalf("data %v", config.Data)
	}
	if config.Labels["bedrock.cloudyfolks.io/kind"] != "ConfigMap" {
		t.Fatalf("labels %v", config.Labels)
	}
}

func TestRulesConfigMapKeepsTheLastGoodValueForABadSetting(t *testing.T) {
	putSetting(t, "objectstorage.reserved-bucket-prefixes", "git-")
	r := &RulesConfigReconciler{Client: testClient}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	var setting v1alpha1.Setting
	if err := testClient.Get(context.Background(), client.ObjectKey{Name: "objectstorage.reserved-bucket-prefixes"}, &setting); err != nil {
		t.Fatal(err)
	}
	setting.Spec.Value = "Bad_Prefix"
	if err := testClient.Update(context.Background(), &setting); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{}); err != nil {
		t.Fatal(err)
	}
	var config corev1.ConfigMap
	if err := testClient.Get(context.Background(), client.ObjectKey{Namespace: SystemNamespace, Name: RulesConfigMapName}, &config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testClient.Delete(context.Background(), &config) })
	if config.Data["reservedPrefixes"] != "git-" {
		t.Fatalf("data %v", config.Data)
	}
}
