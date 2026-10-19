package objectstorage

import "testing"

const (
	goldenRead      = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListAllMyBuckets"],"Resource":["arn:aws:s3:::*"]},{"Effect":"Allow","Action":["s3:GetObject","s3:GetObjectVersion","s3:ListBucket","s3:ListBucketVersions","s3:GetBucketLocation"],"Resource":["arn:aws:s3:::alpha","arn:aws:s3:::alpha/*","arn:aws:s3:::beta","arn:aws:s3:::beta/*"]}]}`
	goldenReadWrite = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListAllMyBuckets"],"Resource":["arn:aws:s3:::*"]},{"Effect":"Allow","Action":["s3:GetObject","s3:GetObjectVersion","s3:ListBucket","s3:ListBucketVersions","s3:GetBucketLocation","s3:PutObject","s3:DeleteObject","s3:DeleteObjectVersion","s3:AbortMultipartUpload","s3:ListMultipartUploadParts","s3:ListBucketMultipartUploads","s3:PutObjectAcl"],"Resource":["arn:aws:s3:::b","arn:aws:s3:::b/*"]}]}`
	goldenNoBuckets = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:ListAllMyBuckets"],"Resource":["arn:aws:s3:::*"]}]}`
)

func TestUserPolicyMatchesTheDocumentOfObjectAPI(t *testing.T) {
	cases := []struct {
		name       string
		permission string
		buckets    []string
		want       string
	}{
		{"read sorted", "read", []string{"beta", "alpha"}, goldenRead},
		{"read write", "readWrite", []string{"b"}, goldenReadWrite},
		{"no buckets", "read", nil, goldenNoBuckets},
		{"empty list", "readWrite", []string{}, goldenNoBuckets},
	}
	for _, tc := range cases {
		got, err := UserPolicy(tc.permission, tc.buckets)
		if err != nil || got != tc.want {
			t.Fatalf("%s: %v\n%s", tc.name, err, got)
		}
	}
}

func TestUserPolicyDoesNotChangeItsInput(t *testing.T) {
	buckets := []string{"beta", "alpha"}
	if _, err := UserPolicy("read", buckets); err != nil {
		t.Fatal(err)
	}
	if buckets[0] != "beta" || buckets[1] != "alpha" {
		t.Fatalf("buckets %v", buckets)
	}
}

func TestUserPolicyRefusesAnUnknownPermission(t *testing.T) {
	for _, permission := range []string{"", "admin", "read_write"} {
		if _, err := UserPolicy(permission, []string{"b"}); err == nil {
			t.Fatalf("%q was accepted", permission)
		}
	}
}
