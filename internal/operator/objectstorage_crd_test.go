package operator

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func TestObjectStorageKindsValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	user := &v1alpha1.ObjectStorageUser{
		ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "default"},
		Spec:       v1alpha1.ObjectStorageUserSpec{DisplayName: "App", Quota: v1alpha1.ObjectStorageUserQuota{Bytes: ptr.To(resource.MustParse("100Gi")), Buckets: ptr.To(int32(5))}},
	}
	createValid(t, c, user)
	user.Status.CredentialsSecret = "app-credentials"
	if err := c.Status().Update(ctx, user); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	createInvalid(t, c, &v1alpha1.ObjectStorageUser{ObjectMeta: metav1.ObjectMeta{Name: "bad-quota", Namespace: "default"}, Spec: v1alpha1.ObjectStorageUserSpec{Quota: v1alpha1.ObjectStorageUserQuota{Buckets: ptr.To(int32(-1))}}}, "spec.quota.buckets")

	bucket := &v1alpha1.ObjectStorageBucket{
		ObjectMeta: metav1.ObjectMeta{Name: "assets", Namespace: "default"},
		Spec:       v1alpha1.ObjectStorageBucketSpec{UserRef: "app", Versioning: true, Quota: v1alpha1.ObjectStorageBucketQuota{Objects: ptr.To(int64(1000))}},
	}
	createValid(t, c, bucket)
	bucket.Status.Endpoint = "https://s3.example.test"
	bucket.Status.BucketName = "default-assets"
	if err := c.Status().Update(ctx, bucket); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	createInvalid(t, c, &v1alpha1.ObjectStorageBucket{ObjectMeta: metav1.ObjectMeta{Name: "no-user", Namespace: "default"}}, "spec.userRef")
}
