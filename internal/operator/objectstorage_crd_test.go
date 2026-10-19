package operator

import (
	"context"
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func objectUser(name, permission string, buckets ...string) *v1alpha1.ObjectStorageUser {
	return &v1alpha1.ObjectStorageUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.ObjectStorageUserSpec{Permission: permission, Buckets: buckets},
	}
}

func TestObjectStorageUserValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	user := objectUser("app", "readWrite", "assets", "logs")
	user.Spec.DisplayName = "App"
	user.Spec.Suspended = true
	createValid(t, c, user)
	user.Status.CredentialsSecret = "app-s3"
	user.Status.ARN = "arn:aws:iam::RGW00000000000000001:user/default.app"
	if err := c.Status().Update(ctx, user); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	createValid(t, c, objectUser("reader", "read"))

	createInvalid(t, c, objectUser("no-permission", ""), "spec.permission")
	createInvalid(t, c, objectUser("bad-permission", "admin"), "spec.permission")
	createInvalid(t, c, objectUser("bad-bucket", "read", "Not_A_Label"), "spec.buckets")

	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("bucket-%d", i)
	}
	createInvalid(t, c, objectUser("too-many", "read", tooMany...), "spec.buckets")
}

func objectBucket(name, bucketName string) *v1alpha1.ObjectStorageBucket {
	return &v1alpha1.ObjectStorageBucket{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1alpha1.ObjectStorageBucketSpec{BucketName: bucketName},
	}
}

func TestObjectStorageBucketValidation(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	bucket := objectBucket("assets", "acme-assets")
	bucket.Spec.Versioning = true
	bucket.Spec.ForceDestroy = true
	bucket.Spec.CORSOrigins = []string{"https://app.example.test"}
	bucket.Spec.Quota = v1alpha1.ObjectStorageBucketQuota{Bytes: ptr.To(resource.MustParse("1Gi")), Objects: ptr.To(int64(1000))}
	createValid(t, c, bucket)
	bucket.Status.Endpoint = "https://s3.example.test"
	bucket.Status.BucketName = "acme-assets"
	bucket.Status.Usage = &v1alpha1.ObjectStorageBucketUsage{Bytes: 10, Objects: 2, SampledAt: metav1.Now()}
	if err := c.Status().Update(ctx, bucket); err != nil {
		t.Fatalf("status must be writable through the status subresource: %v", err)
	}
	createValid(t, c, objectBucket("generated", ""))

	for _, bad := range []string{"ab", "Upper-case", "-leading", "trailing-", "under_score"} {
		createInvalid(t, c, objectBucket("bad-"+fmt.Sprint(len(bad))+bad[:1], bad), "spec.bucketName")
	}
}

func TestObjectStorageBucketNameIsImmutable(t *testing.T) {
	c, _ := StartTestEnv(t)
	ctx := context.Background()

	named := objectBucket("named", "acme-named")
	createValid(t, c, named)
	named.Spec.BucketName = "acme-other"
	updateInvalid(t, c, named, "bucketName is immutable")

	generated := objectBucket("generated", "")
	createValid(t, c, generated)
	generated.Spec.BucketName = "acme-late"
	updateInvalid(t, c, generated, "bucketName is immutable")

	var stored v1alpha1.ObjectStorageBucket
	if err := c.Get(ctx, client.ObjectKeyFromObject(named), &stored); err != nil {
		t.Fatal(err)
	}
	stored.Spec.Versioning = true
	if err := c.Update(ctx, &stored); err != nil {
		t.Fatalf("other fields stay mutable: %v", err)
	}
}
