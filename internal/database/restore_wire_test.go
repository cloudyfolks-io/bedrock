package database

import (
	"encoding/json"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestDatabaseRestoreTargetWire(t *testing.T) {
	c := startEnv(t)
	namespace := projectNamespace(t, c)
	for _, raw := range []string{
		"0001-01-01T00:00:00Z",
		"2026-11-22T10:00:00Z",
		"2026-11-22T13:30:00.1+03:30",
		"2026-11-22T10:00:00.000001Z",
		"2026-11-22T10:00:00.123456Z",
		"2026-11-22T10:00:00.1234560000Z",
	} {
		t.Run(raw, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339Nano, raw)
			must(t, err)
			db := targetOf(namespace, nil)
			db.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Database"))
			db.Name = "restore-" + nextSequence()
			value, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&db)
			must(t, err)
			must(t, unstructured.SetNestedField(value, raw, "spec", "restore", "targetTime"))
			input := &unstructured.Unstructured{Object: value}
			create(t, c, input)
			key := client.ObjectKeyFromObject(input)
			var saved v1alpha1.Database
			must(t, c.Get(t.Context(), key, &saved))
			point := RestoreSpecOf(saved)
			if point.TargetTime == nil || !point.TargetTime.Equal(at) {
				t.Fatalf("stored restore target time = %v, want %v", point.TargetTime, at)
			}
			refused := CheckEngineRules(saved.Spec, v1alpha1.DatabaseClassSpec{})
			if refused != nil {
				t.Fatal(refused)
			}
			metav1.SetMetaDataAnnotation(&saved.ObjectMeta, "example.org/verified", "true")
			must(t, c.Update(t.Context(), &saved))
			out := &unstructured.Unstructured{}
			out.SetGroupVersionKind(input.GroupVersionKind())
			must(t, c.Get(t.Context(), key, out))
			wire, found, err := unstructured.NestedString(out.Object, "spec", "restore", "targetTime")
			if err != nil || !found || wire != at.UTC().Format(time.RFC3339Nano) {
				t.Fatalf("updated restore target time = %q, want %s", wire, at.UTC().Format(time.RFC3339Nano))
			}
		})
	}
	for _, raw := range []string{"2026-11-22T10:00:00.1234567Z", "2026-11-22T10:00:00.1234560001Z", "not-a-time"} {
		t.Run(raw, func(t *testing.T) {
			db := targetOf(namespace, nil)
			db.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind("Database"))
			value, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&db)
			must(t, err)
			must(t, unstructured.SetNestedField(value, raw, "spec", "restore", "targetTime"))
			err = c.Create(t.Context(), &unstructured.Unstructured{Object: value})
			if !apierrors.IsInvalid(err) {
				t.Fatalf("expected admission to reject an invalid target time, got %v", err)
			}
		})
	}
}

func TestPreviouslyStoredInexactRestoreTime(t *testing.T) {
	for _, raw := range []string{"2026-11-22T10:00:00.1234567Z", "2026-11-22T10:00:00.1234560001Z"} {
		values := map[string]any{"sourceUID": "retained", "targetTime": raw}
		var restore v1alpha1.DatabaseRestore
		must(t, runtime.DefaultUnstructuredConverter.FromUnstructured(values, &restore))
		refused := CheckEngineRules(v1alpha1.DatabaseSpec{Engine: v1alpha1.DatabaseEnginePostgreSQL, Restore: &restore}, v1alpha1.DatabaseClassSpec{})
		if refused == nil || refused.Reason != ReasonRestoreTimeUnsupported {
			t.Fatalf("the engine accepted previously stored target time %s", raw)
		}
		roundTrip, err := runtime.DefaultUnstructuredConverter.ToUnstructured(restore.DeepCopy())
		must(t, err)
		if roundTrip["targetTime"] != raw {
			t.Fatalf("old target time changed from %q to %q", raw, roundTrip["targetTime"])
		}
		var list v1alpha1.DatabaseList
		payload := []byte(`{"items":[{"spec":{"restore":{"sourceUID":"old","targetTime":"` + raw + `"}}},{"spec":{"restore":{"sourceUID":"healthy","targetTime":"2026-11-22T10:00:00Z"}}}]}`)
		must(t, json.Unmarshal(payload, &list))
		if len(list.Items) != 2 || !list.Items[0].Spec.Restore.HasInexactTargetTime() || list.Items[1].Spec.Restore.HasInexactTargetTime() {
			t.Fatal("an inexact stored target prevented correct decoding of other databases")
		}
	}
}

func TestDatabaseRecoveryWindowWire(t *testing.T) {
	c := startEnv(t)
	db := newDatabase(t, c, projectNamespace(t, c), "window-"+nextSequence(), nil)
	from := metav1.NewTime(mustTime("2026-11-22T10:00:00.123456Z"))
	to := metav1.NewTime(mustTime("2026-11-22T10:00:01.000001Z"))
	db.Status.Backup = &v1alpha1.DatabaseBackupStatus{PITRFrom: &from, PITRTo: &to, SizeBytes: 123}
	must(t, c.Status().Update(t.Context(), db))
	var saved v1alpha1.Database
	must(t, c.Get(t.Context(), client.ObjectKeyFromObject(db), &saved))
	status := saved.Status.Backup
	if status == nil || !status.PITRFrom.Equal(&from) || !status.PITRTo.Equal(&to) || status.SizeBytes != 123 {
		t.Fatalf("recovery window changed in the status subresource: %+v", status)
	}
}
