package objectstorage

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

func backupIdentity() platform.Identity {
	return platform.Identity{
		Store:           Store(),
		Name:            "pg-backup",
		TargetNamespace: "acme-prod",
		AccessKeyNames:  []string{"ACCESS_KEY_ID", "AWS_ACCESS_KEY_ID"},
		SecretKeyNames:  []string{"ACCESS_SECRET_KEY", "AWS_SECRET_ACCESS_KEY"},
		Manager:         "bedrock-test",
	}
}

func TestStoreIdentityMakesAPlainUserAndCopiesItsKeys(t *testing.T) {
	fresh(t)
	identity := backupIdentity()
	ready, err := platform.EnsureStoreIdentity(context.Background(), testClient, identity)
	if err != nil || ready {
		t.Fatalf("ready %v err %v", ready, err)
	}
	rgw := mustRookUser(t, "pg-backup")
	spec := rgw.Object["spec"].(map[string]any)
	if spec["store"] != "bedrock-rgw" || spec["displayName"] != "pg-backup" {
		t.Fatalf("spec %+v", spec)
	}
	if _, found := spec["accountRef"]; found {
		t.Fatal("a plain identity belongs to an account")
	}
	if _, found := spec["capabilities"]; found {
		t.Fatal("a plain identity has capabilities")
	}
	setRookPhase(t, rgw, "Ready")
	if ready, _ := platform.EnsureStoreIdentity(context.Background(), testClient, identity); ready {
		t.Fatal("ready without keys")
	}
	createRookKeys(t, "pg-backup", fakeRookKeys())
	ready, err = platform.EnsureStoreIdentity(context.Background(), testClient, identity)
	if err != nil || !ready {
		t.Fatalf("ready %v err %v", ready, err)
	}
	secret, found := readSecret(t, "acme-prod", "pg-backup")
	if !found {
		t.Fatal("no copied Secret")
	}
	want := map[string]string{"ACCESS_KEY_ID": "AK", "AWS_ACCESS_KEY_ID": "AK", "ACCESS_SECRET_KEY": "SK", "AWS_SECRET_ACCESS_KEY": "SK"}
	if len(secret.Data) != len(want) {
		t.Fatalf("keys %v", secret.Data)
	}
	for name, value := range want {
		if string(secret.Data[name]) != value {
			t.Fatalf("%s = %q", name, secret.Data[name])
		}
	}
}

func TestStoreIdentityIsIdempotentAndFollowsKeyRotation(t *testing.T) {
	fresh(t)
	identity := backupIdentity()
	if _, err := platform.EnsureStoreIdentity(context.Background(), testClient, identity); err != nil {
		t.Fatal(err)
	}
	makeRookUserReady(t, "pg-backup")
	if _, err := platform.EnsureStoreIdentity(context.Background(), testClient, identity); err != nil {
		t.Fatal(err)
	}
	before, _ := readSecret(t, "acme-prod", "pg-backup")
	if _, err := platform.EnsureStoreIdentity(context.Background(), testClient, identity); err != nil {
		t.Fatal(err)
	}
	after, _ := readSecret(t, "acme-prod", "pg-backup")
	if before.ResourceVersion != after.ResourceVersion {
		t.Fatal("the Secret changed without a reason")
	}
	rook, _ := readSecret(t, StoreNamespace, platform.StoreUserSecretName(Store(), "pg-backup"))
	rook.Data = map[string][]byte{"AccessKey": []byte("AK2"), "SecretKey": []byte("SK2")}
	if err := testClient.Update(context.Background(), rook); err != nil {
		t.Fatal(err)
	}
	if _, err := platform.EnsureStoreIdentity(context.Background(), testClient, identity); err != nil {
		t.Fatal(err)
	}
	rotated, _ := readSecret(t, "acme-prod", "pg-backup")
	if string(rotated.Data["AWS_ACCESS_KEY_ID"]) != "AK2" {
		t.Fatalf("data %v", rotated.Data)
	}
}

func TestStoreIdentityNeedsKeyNamesAndATarget(t *testing.T) {
	fresh(t)
	for _, broken := range []platform.Identity{
		{Store: Store(), Name: "x", TargetNamespace: "acme-prod", SecretKeyNames: []string{"S"}},
		{Store: Store(), Name: "x", TargetNamespace: "acme-prod", AccessKeyNames: []string{"A"}},
		{Store: Store(), Name: "x", AccessKeyNames: []string{"A"}, SecretKeyNames: []string{"S"}},
	} {
		if _, err := platform.EnsureStoreIdentity(context.Background(), testClient, broken); err == nil {
			t.Fatalf("accepted %+v", broken)
		}
	}
	if countRook(t, cephObjectStoreUserGVK, StoreNamespace) != 0 {
		t.Fatal("a Rook user was made for a broken identity")
	}
}

func TestStoreIdentityLeavesOtherSecretsAlone(t *testing.T) {
	fresh(t)
	other := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "acme-prod"}, Data: map[string][]byte{"k": []byte("v")}}
	if err := testClient.Create(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	identity := backupIdentity()
	if _, err := platform.EnsureStoreIdentity(context.Background(), testClient, identity); err != nil {
		t.Fatal(err)
	}
	makeRookUserReady(t, "pg-backup")
	if _, err := platform.EnsureStoreIdentity(context.Background(), testClient, identity); err != nil {
		t.Fatal(err)
	}
	var kept corev1.Secret
	if err := testClient.Get(context.Background(), client.ObjectKey{Namespace: "acme-prod", Name: "other"}, &kept); err != nil || string(kept.Data["k"]) != "v" {
		t.Fatalf("other secret %v %v", kept.Data, err)
	}
}

func TestStoreBucketToleratesAnExistingBucket(t *testing.T) {
	store, url := newFakeS3(t)
	keys := s3.Credentials{AccessKey: "AK", SecretKey: "SK"}
	if err := platform.EnsureStoreBucket(context.Background(), fixedClient(), url, Region, keys, "pg-backups"); err != nil {
		t.Fatal(err)
	}
	if !store.has("pg-backups") {
		t.Fatal("the bucket was not made")
	}
	if err := platform.EnsureStoreBucket(context.Background(), fixedClient(), url, Region, keys, "pg-backups"); err != nil {
		t.Fatalf("an existing bucket: %v", err)
	}
}

func TestStoreBucketRefusesABucketOfAnotherOwner(t *testing.T) {
	store, url := newFakeS3(t)
	store.foreign["taken"] = true
	err := platform.EnsureStoreBucket(context.Background(), fixedClient(), url, Region, s3.Credentials{AccessKey: "AK", SecretKey: "SK"}, "taken")
	if !s3.IsCode(err, s3.BucketAlreadyExists) || strings.Contains(err.Error(), "SK") {
		t.Fatalf("error %v", err)
	}
}
