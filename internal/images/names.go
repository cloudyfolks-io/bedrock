package images

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	CatalogNamespace       = "bedrock-images"
	StorageClass           = "block"
	SnapshotClass          = "block"
	DefaultSizeGi    int32 = 10
	DefaultKeep      int32 = 3

	AdditionalCAConfigMap = "platform-additional-ca"
	AdditionalCAKey       = "ca.crt"
	AnnotationSourceHash  = "bedrock.cloudyfolks.io/source-hash"
	AnnotationDigest      = "bedrock.cloudyfolks.io/digest"
	AnnotationAttach      = "bedrock.cloudyfolks.io/attach"
	AnnotationTarget      = "bedrock.cloudyfolks.io/target-image"
	AttachReadOnly        = "read-only"
	UploadTokenTTL        = 5 * time.Minute
)

func ImportName(image string) string {
	return image + "-import"
}

func GoldenName(image, digest string) string {
	sum := sha256.Sum256([]byte(digest))
	return image + "-" + hex.EncodeToString(sum[:])[:12]
}

func goldenNameOf(s Subject, digest string) string {
	return GoldenName(s.Name, digest+"@"+SourceHash(s.Spec))
}

func SourceHash(spec v1alpha1.ImageSpec) string {
	content, err := json.Marshal(struct {
		Type   string               `json:"type"`
		Source v1alpha1.ImageSource `json:"source"`
		SizeGi int32                `json:"sizeGi"`
	}{typeOf(spec), spec.Source, SizeGi(spec)})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])[:16]
}

func typeOf(spec v1alpha1.ImageSpec) string {
	if spec.Type == "" {
		return v1alpha1.ImageTypeDisk
	}
	return spec.Type
}

func SizeGi(spec v1alpha1.ImageSpec) int32 {
	if spec.SizeGi == 0 {
		return DefaultSizeGi
	}
	return spec.SizeGi
}

func Keep(spec v1alpha1.ImageSpec) int32 {
	if spec.Refresh == nil || spec.Refresh.Keep == 0 {
		return DefaultKeep
	}
	return spec.Refresh.Keep
}

func UsesCron(spec v1alpha1.ImageSpec) bool {
	return spec.Source.Registry != nil && spec.Refresh != nil && spec.Refresh.Schedule != ""
}

func ReadOnlyAttach(spec v1alpha1.ImageSpec) bool {
	return spec.Type == v1alpha1.ImageTypeISO
}

func DataSourceKeyOfImage(img v1alpha1.Image) (client.ObjectKey, bool) {
	return dataSourceKey(img.Namespace, img.Status)
}

func DataSourceKeyOfClusterImage(img v1alpha1.ClusterImage) (client.ObjectKey, bool) {
	return dataSourceKey(CatalogNamespace, img.Status)
}

func dataSourceKey(namespace string, status v1alpha1.ImageStatus) (client.ObjectKey, bool) {
	if status.Phase != v1alpha1.ImagePhaseReady || status.DataSource == "" {
		return client.ObjectKey{}, false
	}
	return client.ObjectKey{Namespace: namespace, Name: status.DataSource}, true
}
