package objectstorage

import (
	"errors"
	"regexp"
	"strings"
)

const (
	StoreName       = "bedrock-rgw"
	StoreNamespace  = "rook-ceph"
	BucketClassName = "bucket"
	Endpoint        = "http://" + ServiceHost + ":80"
	Region          = "us-east-1"
	AccessKeyKey    = "AccessKey"
	SecretKeyKey    = "SecretKey"
	EndpointKey     = "Endpoint"
	MaxPolicyBytes  = 20480

	AppliedVersioningAnnotation = "bedrock.cloudyfolks.io/applied-versioning"
	AppliedPolicyAnnotation     = "bedrock.cloudyfolks.io/applied-policy"
	AppliedCORSAnnotation       = "bedrock.cloudyfolks.io/applied-cors"

	ReservedNameApprovedAnnotation = "bedrock.cloudyfolks.io/reserved-name-approved"
)

var bucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

var (
	ErrQuotaInvalid  = errors.New("quota must not be negative")
	ErrPolicyInvalid = errors.New("policy is not a JSON policy document")
)

func RGWUserName(namespace, name string) string {
	return namespace + "." + name
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
