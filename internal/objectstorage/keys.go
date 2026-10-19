package objectstorage

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

func keysSecretName(user *v1alpha1.ObjectStorageUser) string {
	return user.Name + "-s3"
}

func UserARN(accountID, rgwName string) string {
	return "arn:aws:iam::" + accountID + ":user/" + rgwName
}

func hasAccountRef(rook *unstructured.Unstructured) bool {
	name, _, _ := unstructured.NestedString(rook.Object, "spec", "accountRef", "name")
	return name != ""
}

func rookKeys(ctx context.Context, c client.Client, rgwName string) (s3.Credentials, bool, error) {
	var secret corev1.Secret
	err := c.Get(ctx, client.ObjectKey{Namespace: StoreNamespace, Name: CredentialsSecretName(rgwName)}, &secret)
	if apierrors.IsNotFound(err) {
		return s3.Credentials{}, false, nil
	}
	if err != nil {
		return s3.Credentials{}, false, err
	}
	keys := s3.Credentials{AccessKey: string(secret.Data[AccessKeyKey]), SecretKey: string(secret.Data[SecretKeyKey])}
	return keys, keys.AccessKey != "" && keys.SecretKey != "", nil
}

func keysSecret(user *v1alpha1.ObjectStorageUser, project Project, keys s3.Credentials, endpoint string) *corev1.Secret {
	owner := metav1.NewControllerRef(user, v1alpha1.GroupVersion.WithKind("ObjectStorageUser"))
	return &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            keysSecretName(user),
			Namespace:       user.Namespace,
			Labels:          platform.Labels("ObjectStorageUser", user.Name, project.Tenant, project.Name),
			OwnerReferences: []metav1.OwnerReference{*owner},
		},
		Data: map[string][]byte{AccessKeyKey: []byte(keys.AccessKey), SecretKeyKey: []byte(keys.SecretKey), EndpointKey: []byte(endpoint)},
	}
}
