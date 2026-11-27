package database

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func specOf(engine string, edit func(*v1alpha1.DatabaseSpec)) v1alpha1.DatabaseSpec {
	spec := v1alpha1.DatabaseSpec{Engine: engine, Version: "1", Class: "small", StorageGi: 10, Backup: v1alpha1.DatabaseBackupSpec{Enabled: engine != v1alpha1.DatabaseEngineValkey}}
	if edit != nil {
		edit(&spec)
	}
	return spec
}

func TestEngineRulesForValkey(t *testing.T) {
	single := v1alpha1.DatabaseClassSpec{}
	highAvailability := v1alpha1.DatabaseClassSpec{HighAvailability: true}
	cases := []struct {
		name   string
		spec   v1alpha1.DatabaseSpec
		class  v1alpha1.DatabaseClassSpec
		reason string
	}{
		{"single instance", specOf("valkey", nil), single, ""},
		{"high availability class", specOf("valkey", nil), highAvailability, ReasonInstancesUnsupported},
		{"backup", specOf("valkey", func(s *v1alpha1.DatabaseSpec) { s.Backup.Enabled = true }), single, ReasonBackupUnsupported},
		{"restore", specOf("valkey", func(s *v1alpha1.DatabaseSpec) { s.Restore = &v1alpha1.DatabaseRestore{SourceUID: "abc"} }), single, ReasonRestoreUnsupported},
		{"mysql high availability", specOf("mysql", nil), highAvailability, ""},
		{"mysql restore", specOf("mysql", func(s *v1alpha1.DatabaseSpec) { s.Restore = &v1alpha1.DatabaseRestore{SourceUID: "abc"} }), single, ""},
		{"mysql target time", specOf("mysql", func(s *v1alpha1.DatabaseSpec) {
			at := metav1.NewTime(time.Now().Add(-time.Hour))
			s.Restore = &v1alpha1.DatabaseRestore{SourceUID: "abc", TargetTime: &at}
		}), single, ReasonRestoreTimeUnsupported},
		{"postgresql target time", specOf("postgresql", func(s *v1alpha1.DatabaseSpec) {
			at := metav1.NewTime(time.Now().Add(-time.Hour))
			s.Restore = &v1alpha1.DatabaseRestore{SourceUID: "abc", TargetTime: &at}
		}), single, ""},
	}
	for _, c := range cases {
		refusal := CheckEngineRules(c.spec, c.class)
		switch {
		case c.reason == "" && refusal != nil:
			t.Errorf("%s: refused with %s", c.name, refusal.Reason)
		case c.reason != "" && (refusal == nil || refusal.Reason != c.reason || refusal.Message == ""):
			t.Errorf("%s: refusal %+v, want %s", c.name, refusal, c.reason)
		}
	}
}

func TestSchemaIsRefusedForValkeyOnly(t *testing.T) {
	if refusal := SchemaRefusal("valkey"); refusal == nil || refusal.Reason != ReasonUnsupportedForEngine || refusal.Message == "" {
		t.Fatalf("valkey: %+v", refusal)
	}
	for _, engine := range []string{"postgresql", "mysql"} {
		if refusal := SchemaRefusal(engine); refusal != nil {
			t.Errorf("%s: %+v", engine, refusal)
		}
	}
}

func TestOperationReasonsAreListedOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, reason := range append(Reasons(), OperationReasons()...) {
		if reason == "" || seen[reason] {
			t.Errorf("reason %q is empty or listed twice", reason)
		}
		seen[reason] = true
	}
	for _, want := range []string{ReasonInstancesUnsupported, ReasonBackupUnsupported, ReasonRestoreUnsupported, ReasonRestoreTimeUnsupported, ReasonUnsupportedForEngine} {
		if !seen[want] {
			t.Errorf("reason %s is not listed", want)
		}
	}
}

func TestValkeyOnAHighAvailabilityClassShowsInstancesUnsupported(t *testing.T) {
	c := startEnv(t)
	setSetting(t, c, "database.enabled", "true")
	class := &v1alpha1.DatabaseClass{ObjectMeta: metav1.ObjectMeta{Name: "ha-" + nextSequence()}, Spec: v1alpha1.DatabaseClassSpec{
		CPU: mustQuantity("1"), Memory: mustQuantity("1Gi"), MinStorageGi: 1, MaxStorageGi: 100, HighAvailability: true, Enabled: true,
	}}
	create(t, c, class)
	t.Cleanup(func() { _ = c.Delete(context.Background(), class) })
	db := newDatabase(t, c, projectNamespace(t, c), "cache", func(spec *v1alpha1.DatabaseSpec) {
		spec.Engine = v1alpha1.DatabaseEngineValkey
		spec.Backup.Enabled = false
		spec.Class = class.Name
	})
	reconcileOnce(t, c, db)
	if reason := readyReason(t, c, db); reason != ReasonInstancesUnsupported {
		t.Fatalf("reason %s", reason)
	}
	if hasFinalizer(t, c, db) {
		t.Fatal("a refused database got a finalizer")
	}
}
