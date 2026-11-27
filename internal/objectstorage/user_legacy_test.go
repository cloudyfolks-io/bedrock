package objectstorage

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestUserWithoutAPermissionIsLegacy(t *testing.T) {
	r := userReconcilerAt("http://unused.invalid")
	legacy, err := r.isLegacy(context.Background(), &v1alpha1.ObjectStorageUser{Spec: v1alpha1.ObjectStorageUserSpec{}})
	if err != nil || !legacy {
		t.Fatalf("legacy %v err %v", legacy, err)
	}
}

func TestUserWithAPermissionAndNoRookUserIsNotLegacy(t *testing.T) {
	fresh(t)
	r := userReconcilerAt("http://unused.invalid")
	user := &v1alpha1.ObjectStorageUser{Spec: v1alpha1.ObjectStorageUserSpec{Permission: "read"}}
	user.Name, user.Namespace = "app", "acme-prod"
	legacy, err := r.isLegacy(context.Background(), user)
	if err != nil || legacy {
		t.Fatalf("legacy %v err %v", legacy, err)
	}
}

func TestLegacyUserMessageNamesTheFix(t *testing.T) {
	for _, message := range []string{legacyUserMessage, legacyBucketMessage} {
		if !strings.Contains(message, "delete") || !strings.Contains(message, "create it again") {
			t.Fatalf("message %q", message)
		}
	}
}
