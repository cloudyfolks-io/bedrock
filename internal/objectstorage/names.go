package objectstorage

import (
	"errors"
	"regexp"
	"strings"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	StoreName      = "bedrock-rgw"
	StoreNamespace = "rook-ceph"
	Endpoint       = "http://" + ServiceHost + ":80"
	Region         = "us-east-1"
	AccessKeyKey   = "AccessKey"
	SecretKeyKey   = "SecretKey"
	EndpointKey    = "Endpoint"
	MaxPolicyBytes = 20480

	AppliedVersioningAnnotation = "bedrock.cloudyfolks.io/applied-versioning"
	AppliedPolicyAnnotation     = "bedrock.cloudyfolks.io/applied-policy"
	AppliedCORSAnnotation       = "bedrock.cloudyfolks.io/applied-cors"

	ReservedNameApprovedAnnotation = "bedrock.cloudyfolks.io/reserved-name-approved"
	AdoptUserAnnotation            = "bedrock.cloudyfolks.io/adopt-rgw-user"
	AccountNameAnnotation          = "bedrock.cloudyfolks.io/object-storage-account"
)

var bucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

var (
	ErrQuotaInvalid  = errors.New("quota must not be negative")
	ErrPolicyInvalid = errors.New("policy is not a JSON policy document")
)

func RGWUserName(namespace, name string) string {
	return namespace + "." + name
}

func rgwUserOf(user *v1alpha1.ObjectStorageUser) string {
	if adopted := user.Annotations[AdoptUserAnnotation]; adopted != "" {
		return adopted
	}
	return RGWUserName(user.Namespace, user.Name)
}

func SplitRGWUserName(id string) (namespace, name string, ok bool) {
	namespace, name, ok = strings.Cut(id, ".")
	if !ok {
		return "", "", false
	}
	return namespace, name, true
}

func CredentialsSecretName(rgwUser string) string {
	return "rook-ceph-object-user-" + StoreName + "-" + rgwUser
}

func nameProblem(name string, rules Rules, approved bool) string {
	if !bucketNamePattern.MatchString(name) {
		return reasonNameInvalid
	}
	if !approved && reservedName(name, rules) {
		return reasonNameReserved
	}
	return ""
}
