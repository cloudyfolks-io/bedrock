package objectstorage

import (
	"encoding/json"
	"fmt"
	"slices"
)

const (
	PermissionRead      = "read"
	PermissionReadWrite = "readWrite"
	PolicyName          = "access"
	policyVersion       = "2012-10-17"
)

var (
	readActions = []string{
		"s3:GetObject",
		"s3:GetObjectVersion",
		"s3:ListBucket",
		"s3:ListBucketVersions",
		"s3:GetBucketLocation",
	}
	writeActions = []string{
		"s3:PutObject",
		"s3:DeleteObject",
		"s3:DeleteObjectVersion",
		"s3:AbortMultipartUpload",
		"s3:ListMultipartUploadParts",
		"s3:ListBucketMultipartUploads",
		"s3:PutObjectAcl",
	}
)

type policyStatement struct {
	Effect   string   `json:"Effect"`
	Action   []string `json:"Action"`
	Resource []string `json:"Resource"`
}

type policyDocument struct {
	Version   string            `json:"Version"`
	Statement []policyStatement `json:"Statement"`
}

func UserPolicy(permission string, buckets []string) (string, error) {
	actions, err := actionsFor(permission)
	if err != nil {
		return "", err
	}
	document := policyDocument{
		Version:   policyVersion,
		Statement: []policyStatement{{Effect: "Allow", Action: []string{"s3:ListAllMyBuckets"}, Resource: []string{"arn:aws:s3:::*"}}},
	}
	if len(buckets) > 0 {
		document.Statement = append(document.Statement, policyStatement{Effect: "Allow", Action: actions, Resource: bucketResources(buckets)})
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func actionsFor(permission string) ([]string, error) {
	switch permission {
	case PermissionRead:
		return slices.Clone(readActions), nil
	case PermissionReadWrite:
		return slices.Concat(readActions, writeActions), nil
	}
	return nil, fmt.Errorf("unknown permission %q", permission)
}

func bucketResources(buckets []string) []string {
	sorted := slices.Sorted(slices.Values(buckets))
	resources := make([]string, 0, 2*len(sorted))
	for _, bucket := range sorted {
		resources = append(resources, "arn:aws:s3:::"+bucket, "arn:aws:s3:::"+bucket+"/*")
	}
	return resources
}
